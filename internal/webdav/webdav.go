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
func doRetrying429(ctx context.Context, do func() (*http.Response, error)) (*http.Response, error) {
	var resp *http.Response
	var err error
	for attempt := 0; attempt < retry429Max; attempt++ {
		resp, err = do()
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
		select {
		case <-time.After(delay):
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

func (c *Client) propfind(ctx context.Context, remotePath, depth string) (*multistatus, error) {
	target := c.resolve(remotePath)
	resp, err := doRetrying429(ctx, func() (*http.Response, error) {
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

// walkConcurrency bounds how many PROPFIND requests Walk has in flight
// at once. The tree can fan out arbitrarily wide (spawning a goroutine
// per subdirectory found costs only a few KB of stack each, so that
// part is unbounded), but actual network requests are capped here —
// matching godl url's default chunk concurrency, and staying polite to
// WebDAV servers that may not expect a flood of concurrent requests.
const walkConcurrency = 8

// Walk recursively lists every file (not directory) under root, using
// Depth:1 PROPFIND at each level rather than Depth:infinity — many
// WebDAV servers reject or cap infinite-depth requests on large trees.
// Sibling and cross-level directories are listed concurrently (up to
// walkConcurrency at once) rather than one at a time, so a deep or wide
// tree doesn't pay for its PROPFIND round-trips serially.
func (c *Client) Walk(ctx context.Context, root string) ([]Entry, error) {
	sem := make(chan struct{}, walkConcurrency)
	var (
		mu       sync.Mutex
		files    []Entry
		firstErr error
		wg       sync.WaitGroup
	)

	var walk func(p string)
	walk = func(p string) {
		defer wg.Done()

		mu.Lock()
		stop := firstErr != nil
		mu.Unlock()
		if stop || ctx.Err() != nil {
			return
		}

		sem <- struct{}{}
		children, err := c.List(ctx, p)
		<-sem
		if err != nil {
			mu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
			return
		}

		var dirs []string
		mu.Lock()
		for _, e := range children {
			if e.IsDir {
				dirs = append(dirs, e.Path)
			} else {
				files = append(files, e)
			}
		}
		mu.Unlock()

		for _, d := range dirs {
			wg.Add(1)
			go walk(d)
		}
	}

	wg.Add(1)
	go walk(root)
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return files, nil
}

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

	resp, err := doRetrying429(ctx, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.resolve(remotePath).String(), nil)
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
		return start, err
	}
	defer resp.Body.Close()

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
			if werr := ratelimit.WaitAll(ctx, n, limiter, globalLimiter); werr != nil {
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
			if ctx.Err() != nil {
				return written, ctx.Err()
			}
			return written, rerr
		}
	}
}
