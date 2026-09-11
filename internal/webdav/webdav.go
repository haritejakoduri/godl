// Package webdav is a minimal WebDAV client: enough to stat a remote
// path, list a directory's immediate children, and download a file with
// basic auth and Range-based resume. It intentionally doesn't implement
// the whole RFC 4918 — just what "godl webdav" needs to walk and pull a
// file or folder tree.
package webdav

import (
	"context"
	"crypto/tls"
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
	// A bespoke Transport (needed for InsecureSkipVerify) doesn't pick
	// up proxy env vars the way http.DefaultTransport does unless told
	// to explicitly.
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if insecureSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &Client{base: u, Username: username, Password: password, HTTP: &http.Client{Transport: tr}}, nil
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

// retry429Max bounds how many times a request that comes back 429 (Too
// Many Requests) is retried before giving up. Some WebDAV backends —
// cloud-storage-proxying services like TorBox in particular — rate-limit
// aggressively enough that even a single PROPFIND against the root can
// get 429'd, especially right after a burst of activity (Walk fanning
// out across a folder tree, a previous browse session, ...); without a
// retry, that looks exactly like a broken connection instead of the
// transient "back off a moment" it actually is.
const retry429Max = 5

// maxRetryDelay caps how long a single wait is, whether it comes from
// the server's own Retry-After header or godl's own exponential
// fallback — a server advertising a very long Retry-After shouldn't
// hang a download that long; better to retry sooner and let
// retry429Max end things if the server really is unavailable.
const maxRetryDelay = 30 * time.Second

// retryBackoffUnit is the base of the exponential fallback used when a
// 429 carries no Retry-After header: 1x, 2x, 4x, 8x, 16x this value. A
// var (not a const), purely so a test can shrink it to a few
// milliseconds instead of a test actually sleeping through real
// backoff delays.
var retryBackoffUnit = time.Second

// doRetrying429 runs do (one HTTP round trip) and, on a 429 response,
// waits and retries — honoring the server's Retry-After header
// (seconds form; that's the only form real rate-limiting backends send
// in practice) when present, otherwise backing off exponentially (1s,
// 2s, 4s, ...) — up to retry429Max attempts total. Any other status or
// a transport error is returned as-is on the first try. do is called
// again on each retry (not just its response re-read), so a caller
// building a fresh *http.Request inside it — required anyway, since a
// request's body reader can't be replayed — gets one naturally.
func (c *Client) doRetrying429(ctx context.Context, do func() (*http.Response, error)) (*http.Response, error) {
	gate := gateFor(c.base.Host)
	var resp *http.Response
	var err error
	for attempt := 0; attempt < retry429Max; attempt++ {
		// Every request to this host — PROPFIND and GET, across every
		// Client and every job in the process — passes through here, so
		// the host's ceiling holds however much work is queued. See
		// hostgate.go.
		//
		// The slot covers issuing the request and getting its response
		// headers back, not streaming the body: do returns as soon as
		// the headers land, and Download then reads the body outside the
		// gate. That's deliberate — what draws 429s is the rate of new
		// requests (a Walk's PROPFIND fan-out above all), not bytes in
		// flight, and holding a slot for the whole of a multi-minute
		// file transfer would let one download starve every PROPFIND
		// queued behind it.
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

// retryAfterDelay parses a Retry-After header's seconds form into a
// duration. ok is false — meaning "fall back to exponential backoff
// instead" — only when the header is missing, negative, or in the less
// common HTTP-date form (not worth the extra parsing given how rarely
// real servers send that form for a rate-limit response); "0" is a
// legitimate value (retry essentially immediately) and must return
// (0, true), not be mistaken for "absent".
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

// propfindTimeout bounds one propfind() call end to end, including
// whatever 429 retries doRetrying429 performs inside it (worst case
// retry429Max * maxRetryDelay ≈ 150s) plus margin for the response
// itself. Without this, a connection that never gets a response at
// all — not a 429, just silence — hangs forever: neither the job's own
// context nor http.Client enforce any timeout on their own. That
// mattered little for a single PROPFIND, but Walk fans out up to
// walkConcurrency requests at once for a deep or wide folder tree, so
// the more there is to walk, the higher the odds of hitting one
// wedged connection — and since Walk waits on every request it
// started, one wedge stalls the whole recursive walk. A var (not a
// const), purely so a test can shrink it instead of actually waiting
// out the timeout.
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

// walkConcurrency bounds how many directories Walk has goroutines for at
// once. It matches hostGateDefaultLimit because the gate in hostgate.go
// is what actually caps requests to the server: a second, looser bound
// here would only queue goroutines up behind it. Note this is a bound on
// goroutines, not just on requests — see Walk.
const walkConcurrency = hostGateDefaultLimit

// Walk recursively lists every file (not directory) under root, using
// Depth:1 PROPFIND at each level rather than Depth:infinity — many
// WebDAV servers reject or cap infinite-depth requests on large trees.
// Sibling and cross-level directories are listed concurrently rather
// than one at a time, so a deep or wide tree doesn't pay for its
// PROPFIND round-trips serially.
//
// Structured as a fixed pool of workers draining a shared queue, rather
// than a goroutine per directory. On a large share the latter parks tens
// of thousands of goroutines — one per directory found, each holding its
// parent's children alive — for no gain, since the host gate caps the
// requests anyway. A pool keeps the goroutine count flat no matter how
// big the tree is, and can't deadlock the way recursive spawning against
// a semaphore can when a parent holds the slot its child needs.
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

// downloadIdleTimeout bounds how long Download will wait without
// receiving any new response data before giving up — distinct from the
// overall transfer time, which is intentionally unbounded (a large
// file can legitimately take far longer than this to finish, so a flat
// cap on the whole download would be wrong). What this catches is a
// connection that goes silent mid-transfer and never recovers — cloud-
// storage-proxying backends like TorBox can do this under load — which
// would otherwise hang the download forever, since neither the job's
// own context nor http.Client enforce any timeout of their own. A var
// (not a const), purely so a test can shrink it instead of actually
// waiting out the timeout.
var downloadIdleTimeout = 90 * time.Second

// idleCheckInterval is how often the watchdog above re-checks. It only
// bounds how late a stall is noticed (up to this much past
// downloadIdleTimeout), so it's coarse on purpose. A var for the same
// test reason as downloadIdleTimeout.
var idleCheckInterval = 10 * time.Second

// Download fetches remotePath to localPath, resuming from localPath's
// existing size via a Range request if it's already partially present.
// Returns the total bytes now on disk (not just bytes newly written).
// limiter, if non-nil, caps this download's rate — the same
// *rate.Limiter instance passed by a caller downloading several files
// of one job concurrently (see internal/daemon's startWebDAV) shares
// one cap across all of them, so the job's own concurrency doesn't
// multiply it. globalLimiter, if non-nil, is the Settings tab's shared
// bandwidth cap — the same instance handed to every webdav (and url)
// job's download loop across the whole daemon, waited on in addition
// to limiter rather than instead of it.
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

	// dlCtx (derived from ctx, not ctx itself) is what the request and
	// its body reads run under, so the idle watchdog below can abort a
	// stalled transfer without touching the caller's own ctx — a
	// distinction the error handling after both doRetrying429 and the
	// read loop relies on to tell "genuinely canceled" (ctx.Err() set)
	// apart from "our own idle timer fired" (idleFired set).
	dlCtx, dlCancel := context.WithCancel(ctx)
	defer dlCancel()
	// The watchdog measures time since the connection last produced data,
	// but deliberately does not count time spent waiting on the rate
	// limiter: that wait is godl's own doing, and under a low --limit-rate
	// (or a global cap shared across many concurrent files) a single
	// 256KiB read can legitimately be held longer than downloadIdleTimeout,
	// which would kill a perfectly healthy download. inLimiter marks those
	// stretches so the watchdog skips them.
	//
	// A ticker rather than a time.AfterFunc reset on each read: resetting
	// a timer per 256KiB chunk is pure timer-heap churn at any real
	// transfer rate, and the reset-before-vs-after-the-limiter ordering
	// is exactly what made this subtle in the first place.
	// Both knobs are read once, here, and handed to the goroutine as
	// values: they're package vars so tests can shrink them, and a
	// goroutine reading them directly would still be doing so after
	// Download returns (it can be scheduled late), racing the next test's
	// assignment. Capturing them also means an in-flight download keeps
	// the settings it started with.
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
