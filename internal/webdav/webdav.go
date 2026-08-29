// Package webdav is a minimal WebDAV client: enough to stat a remote
// path, list a directory's immediate children, and download a file with
// basic auth and Range-based resume. It intentionally doesn't implement
// the whole RFC 4918 — just what "godl webdav" needs to walk and pull a
// file or folder tree.
package webdav

import (
	"context"
	"crypto/tls"
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
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", target.String(), strings.NewReader(propfindBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", depth)
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	c.setAuth(req)

	resp, err := c.HTTP.Do(req)
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.resolve(remotePath).String(), nil)
	if err != nil {
		return 0, err
	}
	c.setAuth(req)
	if start > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))
	}

	resp, err := c.HTTP.Do(req)
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
