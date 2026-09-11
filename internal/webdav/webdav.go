// Package webdav is a minimal WebDAV client: enough to stat a remote
// path, list a directory's immediate children, and download a file with
// basic auth and Range-based resume. It intentionally doesn't implement
// the whole RFC 4918 — just what "godl webdav" needs to walk and pull a
// file or folder tree.
package webdav

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"godl/internal/httpx"
	"godl/internal/ratelimit"
)

// Entry describes one file or directory found via PROPFIND. Path is
// relative to the Client's base URL and always starts with "/".
type Entry struct {
	Path  string
	IsDir bool
	// Size is -1 when unknown (always true for directories; WebDAV
	// servers don't consistently report getcontentlength for files
	// either).
	Size int64
}

type Client struct {
	base     *url.URL
	Username string
	Password string
	HTTP     *http.Client
}

// New builds a Client for the given base URL (must be http:// or
// https://). insecureSkipVerify disables TLS certificate verification,
// for self-signed https servers.
func New(baseURL, username, password string, insecureSkipVerify bool) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid webdav url %q: %w", baseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("webdav url must be http:// or https://, got %q", baseURL)
	}
	return &Client{
		base:     u,
		Username: username,
		Password: password,
		HTTP:     httpx.TransferClient(insecureSkipVerify),
	}, nil
}

func (c *Client) resolve(remotePath string) *url.URL {
	if !strings.HasPrefix(remotePath, "/") {
		remotePath = "/" + remotePath
	}
	// path.Join below cleans away a trailing slash; put it back if the
	// caller asked for one. Some WebDAV servers only recognize a
	// collection resource when addressed with its trailing slash, so
	// dropping it silently (e.g. for a PROPFIND on a subdirectory found
	// while walking) can 404 or, worse, get redirected somewhere
	// unexpected.
	trailingSlash := strings.HasSuffix(remotePath, "/")
	u := *c.base
	u.Path = path.Join(strings.TrimSuffix(u.Path, "/"), remotePath)
	if trailingSlash && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return &u
}

func (c *Client) setAuth(req *http.Request) {
	if c.Username != "" || c.Password != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
}

// URLFor exposes resolve for callers outside this package that need the
// literal URL for a remote path without issuing a request against it —
// e.g. handing it straight to an external player to stream directly,
// rather than downloading through this Client first.
func (c *Client) URLFor(remotePath string) *url.URL {
	return c.resolve(remotePath)
}

// AuthHeader returns this Client's credentials as an HTTP Basic
// Authorization header value ("Basic <base64>"), or "" if none are
// set — for a caller (like URLFor's) that needs to authenticate a
// request itself instead of going through Client.Download, without
// embedding the password in a URL where it'd be more widely exposed
// (e.g. in another process's argv).
func (c *Client) AuthHeader() string {
	if c.Username == "" && c.Password == "" {
		return ""
	}
	token := c.Username + ":" + c.Password
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(token))
}

// relativePath turns a (possibly percent-encoded, possibly absolute-URL)
// href from a PROPFIND response back into a path relative to the
// Client's base URL, e.g. "/dav/files/alice/Docs/a.txt" -> "/Docs/a.txt"
// when the base URL is ".../dav/files/alice/".
func (c *Client) relativePath(href string) string {
	p := href
	if u, err := url.Parse(href); err == nil && u.Path != "" {
		p = u.Path
	}
	if decoded, err := url.PathUnescape(p); err == nil {
		p = decoded
	}
	base := strings.TrimSuffix(c.base.Path, "/")
	rel := strings.TrimPrefix(p, base)
	if !strings.HasPrefix(rel, "/") {
		rel = "/" + rel
	}
	return rel
}

const propfindBody = `<?xml version="1.0" encoding="utf-8" ?>
<D:propfind xmlns:D="DAV:">
  <D:prop>
    <D:resourcetype/>
    <D:getcontentlength/>
  </D:prop>
</D:propfind>`

// Cloud-storage-proxying backends rate-limit hard enough that even a
// lone PROPFIND can be 429'd; without a retry that looks like a broken
// connection rather than the "back off a moment" it is.
const retry429Max = 5

// maxRetryDelay caps one wait, however long a Retry-After asks for: a
// download shouldn't hang that long, and retry429Max ends things if the
// server really is down.
const maxRetryDelay = 30 * time.Second

// Base of the exponential fallback when a 429 carries no Retry-After.
// Var so tests don't sleep through real backoff.
var retryBackoffUnit = time.Second

// doRetrying429 runs one round trip, retrying on 429 — honoring
// Retry-After when present, else backing off exponentially. do is called
// afresh each attempt, since a request body can't be replayed.
func (c *Client) doRetrying429(ctx context.Context, do func() (*http.Response, error)) (*http.Response, error) {
	gate := gateFor(c.base.Host)
	var resp *http.Response
	var err error
	for attempt := 0; attempt < retry429Max; attempt++ {
		// The slot covers the request and its response headers, not the
		// body: what draws 429s is the rate of new requests, and holding
		// a slot through a multi-minute transfer would let one download
		// starve every PROPFIND behind it. See hostgate.go.
		release, aerr := gate.acquire(ctx)
		if aerr != nil {
			return nil, aerr
		}
		resp, err = do()
		release()
		if err != nil || resp.StatusCode != http.StatusTooManyRequests {
			return resp, err
		}
		delay, ok := retryAfterDelay(resp.Header.Get("Retry-After"))
		resp.Body.Close()
		if !ok {
			delay = time.Duration(1<<attempt) * retryBackoffUnit
		}
		if delay > maxRetryDelay {
			delay = maxRetryDelay
		}
		// Tell the gate before sleeping, so requests that haven't been
		// sent yet also hold off instead of walking into the same limit.
		gate.throttled(delay)
		select {
		case <-time.After(jitter(delay)):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return resp, err
}

// retryAfterDelay parses Retry-After's seconds form. ok is false only
// when the header is missing, negative, or in HTTP-date form. "0" is a
// legitimate value — retry immediately — and must not be read as
// "absent".
func retryAfterDelay(v string) (delay time.Duration, ok bool) {
	if v == "" {
		return 0, false
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}

// WebDAV multistatus response shapes. Matching is by {namespace, local
// name} regardless of the namespace prefix a given server chooses
// ("D:", "d:", ...), since they all declare xmlns:*="DAV:".
type multistatus struct {
	XMLName   xml.Name   `xml:"DAV: multistatus"`
	Responses []response `xml:"DAV: response"`
}

type response struct {
	Href     string     `xml:"DAV: href"`
	Propstat []propstat `xml:"DAV: propstat"`
}

type propstat struct {
	Prop   prop   `xml:"DAV: prop"`
	Status string `xml:"DAV: status"`
}

type prop struct {
	ResourceType  resourceType `xml:"DAV: resourcetype"`
	ContentLength string       `xml:"DAV: getcontentlength"`
}

type resourceType struct {
	Collection *struct{} `xml:"DAV: collection"`
}

// propfindTimeout bounds one propfind() call end to end, 429 retries
// included. Without it a connection that never answers at all — not a
// 429, just silence — hangs forever, and since Walk waits on every
// request it started, one wedge stalls the whole walk. Var for tests.
var propfindTimeout = 3 * time.Minute

func (c *Client) propfind(ctx context.Context, remotePath, depth string) (*multistatus, error) {
	ctx, cancel := context.WithTimeout(ctx, propfindTimeout)
	defer cancel()

	target := c.resolve(remotePath)
	resp, err := c.doRetrying429(ctx, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, "PROPFIND", target.String(), strings.NewReader(propfindBody))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Depth", depth)
		req.Header.Set("Content-Type", "application/xml; charset=utf-8")
		c.setAuth(req)
		return c.HTTP.Do(req)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		return nil, fmt.Errorf("unexpected status from PROPFIND %s: %s", remotePath, resp.Status)
	}
	var ms multistatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, fmt.Errorf("parsing PROPFIND response for %s: %w", remotePath, err)
	}
	return &ms, nil
}

func (c *Client) entries(ms *multistatus) []Entry {
	out := make([]Entry, 0, len(ms.Responses))
	for _, r := range ms.Responses {
		var p *prop
		for i := range r.Propstat {
			if strings.Contains(r.Propstat[i].Status, "200") {
				p = &r.Propstat[i].Prop
				break
			}
		}
		if p == nil {
			continue
		}
		e := Entry{Path: c.relativePath(r.Href), IsDir: p.ResourceType.Collection != nil, Size: -1}
		if p.ContentLength != "" {
			if n, err := strconv.ParseInt(p.ContentLength, 10, 64); err == nil {
				e.Size = n
			}
		}
		out = append(out, e)
	}
	return out
}

// Stat fetches metadata for exactly one remote path.
func (c *Client) Stat(ctx context.Context, remotePath string) (Entry, error) {
	ms, err := c.propfind(ctx, remotePath, "0")
	if err != nil {
		return Entry{}, err
	}
	entries := c.entries(ms)
	if len(entries) == 0 {
		return Entry{}, fmt.Errorf("no such remote path: %s", remotePath)
	}
	return entries[0], nil
}

// List returns the immediate children of a directory (not the directory
// itself). remotePath is always treated as a directory: a trailing
// slash is added if missing, since some servers only recognize a
// collection resource in its slash-terminated form.
func (c *Client) List(ctx context.Context, remotePath string) ([]Entry, error) {
	if !strings.HasSuffix(remotePath, "/") {
		remotePath += "/"
	}
	ms, err := c.propfind(ctx, remotePath, "1")
	if err != nil {
		return nil, err
	}
	self := normalizeDirPath(remotePath)
	var out []Entry
	for _, e := range c.entries(ms) {
		if normalizeDirPath(e.Path) == self {
			continue // the directory's own entry, always included at depth 1
		}
		out = append(out, e)
	}
	return out, nil
}

// normalizeDirPath drops a directory path's trailing slash for
// comparison purposes, treating "/" itself as the (irreducible) root.
func normalizeDirPath(p string) string {
	trimmed := strings.TrimSuffix(p, "/")
	if trimmed == "" {
		return "/"
	}
	return trimmed
}

// Matches hostGateDefaultLimit: the gate is what actually caps requests,
// so a looser bound here would only queue goroutines behind it.
const walkConcurrency = hostGateDefaultLimit

// Walk recursively lists every file (not directory) under root, using
// Depth:1 PROPFIND at each level rather than Depth:infinity — many
// WebDAV servers reject or cap infinite-depth requests on large trees.
// Sibling and cross-level directories are listed concurrently rather
// than one at a time, so a deep or wide tree doesn't pay for its
// PROPFIND round-trips serially.
//
// A fixed worker pool draining a shared queue, not a goroutine per
// directory: the latter parks tens of thousands of goroutines on a large
// share for no gain (the host gate caps requests anyway), and recursive
// spawning against a semaphore can deadlock when a parent holds the slot
// its child needs.
func (c *Client) Walk(ctx context.Context, root string) ([]Entry, error) {
	// Cancelable so the first failure stops PROPFINDs already in flight
	// (and unblocks parents waiting for a slot) instead of letting the
	// whole tree finish walking just to throw the result away.
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu       sync.Mutex
		cond     = sync.NewCond(&mu)
		queue    = []string{root} // directories found but not yet listed
		active   int              // workers currently inside a PROPFIND
		files    []Entry
		firstErr error
	)

	// Cond can't wait on a context, so cancellation wakes the workers
	// explicitly. Without this, a canceled walk would sit blocked in
	// cond.Wait until some other worker happened to broadcast.
	go func() {
		<-wctx.Done()
		mu.Lock()
		cond.Broadcast()
		mu.Unlock()
	}()

	var wg sync.WaitGroup
	for i := 0; i < walkConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				// Nothing to take, but a peer is still listing and may
				// yet queue more: wait rather than exit. Once the queue
				// is empty and nobody is active, the tree is exhausted.
				for len(queue) == 0 && active > 0 && firstErr == nil && wctx.Err() == nil {
					cond.Wait()
				}
				if len(queue) == 0 || firstErr != nil || wctx.Err() != nil {
					mu.Unlock()
					cond.Broadcast() // let the other workers reach the same conclusion
					return
				}
				p := queue[len(queue)-1]
				queue = queue[:len(queue)-1]
				active++
				mu.Unlock()

				children, err := c.List(wctx, p)

				mu.Lock()
				active--
				if err != nil {
					// A cancellation here is a consequence of some other
					// worker's failure, not a failure of its own —
					// recording it would mask the real error.
					if wctx.Err() == nil && firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					cancel()
					cond.Broadcast()
					return
				}
				for _, e := range children {
					if e.IsDir {
						queue = append(queue, e.Path)
					} else {
						files = append(files, e)
					}
				}
				mu.Unlock()
				cond.Broadcast()
			}
		}()
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return files, nil
}

// downloadIdleTimeout bounds silence, not total transfer time — a large
// file may legitimately take hours, so a flat cap would be wrong. It
// catches a connection that goes quiet mid-transfer and never recovers.
// Var for tests.
var downloadIdleTimeout = 90 * time.Second

// How often the watchdog re-checks; only bounds how late a stall is
// noticed, so it's coarse on purpose.
var idleCheckInterval = 10 * time.Second

// Download fetches remotePath to localPath, resuming via Range if it's
// already partially present, and returns the total bytes now on disk.
// Both limiters are waited on, and both are shared instances — see
// Daemon.globalLimiter for what that sharing buys.
func (c *Client) Download(ctx context.Context, remotePath, localPath string, limiter, globalLimiter *rate.Limiter, progress func(done, total int64)) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return 0, err
	}

	var start int64
	if fi, err := os.Stat(localPath); err == nil {
		start = fi.Size()
	}

	f, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	// Derived from ctx so the watchdog can abort a stalled transfer
	// without touching the caller's own — which is how the error handling
	// below tells "canceled" apart from "our idle timer fired".
	dlCtx, dlCancel := context.WithCancel(ctx)
	defer dlCancel()
	// The watchdog must not count time spent waiting on the rate limiter:
	// that wait is godl's own doing, and under a low cap a single read
	// can be held longer than downloadIdleTimeout, killing a healthy
	// download. inLimiter marks those stretches so the watchdog skips
	// them.
	// Read once and passed by value: the goroutine can be scheduled after
	// Download returns, and reading these package vars there would race
	// the next test's assignment.
	idleTimeout := downloadIdleTimeout
	checkEvery := idleCheckInterval

	var idleFired atomic.Bool
	var inLimiter atomic.Bool
	lastData := &atomic.Int64{}
	lastData.Store(time.Now().UnixNano())
	watchdogDone := make(chan struct{})
	defer close(watchdogDone)
	go func() {
		t := time.NewTicker(checkEvery)
		defer t.Stop()
		for {
			select {
			case <-watchdogDone:
				return
			case <-dlCtx.Done():
				return
			case now := <-t.C:
				if inLimiter.Load() {
					continue
				}
				if now.Sub(time.Unix(0, lastData.Load())) > idleTimeout {
					idleFired.Store(true)
					dlCancel()
					return
				}
			}
		}
	}()

	resp, err := c.doRetrying429(dlCtx, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, c.resolve(remotePath).String(), nil)
		if err != nil {
			return nil, err
		}
		c.setAuth(req)
		if start > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))
		}
		return c.HTTP.Do(req)
	})
	if err != nil {
		if idleFired.Load() {
			return start, fmt.Errorf("downloading %s: no data received for %s, giving up", remotePath, idleTimeout)
		}
		return start, err
	}
	defer resp.Body.Close()
	lastData.Store(time.Now().UnixNano()) // headers arrived; give the body its own full window rather than sharing the one used to wait for them

	switch resp.StatusCode {
	case http.StatusOK:
		// Either we didn't ask for a range, or the server ignored it —
		// either way it's sending the whole file, so start over.
		start = 0
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		if err := f.Truncate(0); err != nil {
			return 0, err
		}
	case http.StatusPartialContent:
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return start, err
		}
	default:
		return start, fmt.Errorf("unexpected status downloading %s: %s", remotePath, resp.Status)
	}

	total := int64(-1)
	if resp.ContentLength >= 0 {
		total = start + resp.ContentLength
	}

	// 256KiB, not a smaller default: fewer Read/Write syscalls per MB
	// transferred (see the matching constant in internal/downloader).
	buf := make([]byte, 256*1024)
	written := start
	lastReport := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			lastData.Store(time.Now().UnixNano())
			inLimiter.Store(true)
			werr := ratelimit.WaitAll(ctx, n, limiter, globalLimiter)
			inLimiter.Store(false)
			// Count the wait as progress too: the bytes did arrive, godl
			// just chose to hold them.
			lastData.Store(time.Now().UnixNano())
			if werr != nil {
				return written, werr
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				return written, werr
			}
			written += int64(n)
			if progress != nil && time.Since(lastReport) > 200*time.Millisecond {
				progress(written, total)
				lastReport = time.Now()
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				if progress != nil {
					progress(written, total)
				}
				return written, nil
			}
			if idleFired.Load() {
				return written, fmt.Errorf("downloading %s: no data received for %s, giving up", remotePath, idleTimeout)
			}
			if ctx.Err() != nil {
				return written, ctx.Err()
			}
			return written, rerr
		}
	}
}
