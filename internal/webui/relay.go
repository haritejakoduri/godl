package webui

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A relay sits between ffmpeg and a site that slows down long downloads.
//
// YouTube serves the first part of any one request quickly and then
// drips the rest out at about the video's own bitrate. A player reading
// one long request therefore never gets ahead of playback, and any
// hiccup on the network shows as a stutter. yt-dlp's answer is to fetch
// in pieces — one request per few MB — and it marks the streams that
// need it (downloader_options.http_chunk_size). ffmpeg has no such
// mode, so godl does it here: ffmpeg reads a plain loopback URL, and
// each read is served by a run of upstream range requests of chunk
// bytes each.
//
// It listens on 127.0.0.1 only, behind a random token per stream, and
// buffers nothing beyond what's in flight: each piece is copied through
// as it arrives, so ffmpeg's own pace (and through it the browser's)
// still sets the speed.
type relay struct {
	mu      sync.Mutex
	srv     *http.Server
	addr    string
	targets map[string]*relayTarget
}

type relayTarget struct {
	url     string
	header  http.Header
	ua      string
	chunk   int64
	created time.Time

	// length is the stream's total size, learned from the first answer.
	mu     sync.Mutex
	length int64
}

// relayClient fetches the pieces. Each piece is small, so a stalled
// one is bounded rather than left to hang the stream.
var relayPieceClient = &http.Client{Timeout: 60 * time.Second}

// url registers target and returns the loopback address ffmpeg should
// read instead, starting the listener on first use.
func (r *relay) url(target, name string, header http.Header, ua string, chunk int64) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.srv == nil {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", fmt.Errorf("starting the stream relay: %w", err)
		}
		r.addr = l.Addr().String()
		r.targets = map[string]*relayTarget{}
		r.srv = &http.Server{Handler: http.HandlerFunc(r.serve), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := r.srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("stream relay: %v", err)
			}
		}()
	}
	for tok, t := range r.targets {
		if time.Since(t.created) > linkTTL {
			delete(r.targets, tok)
		}
	}
	tok := randomID()
	r.targets[tok] = &relayTarget{url: target, header: header, ua: ua, chunk: chunk, created: time.Now()}
	return "http://" + r.addr + "/" + tok + "/" + name, nil
}

func (r *relay) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.srv != nil {
		r.srv.Close()
		r.srv = nil
	}
}

func (r *relay) serve(w http.ResponseWriter, req *http.Request) {
	tok, _, _ := strings.Cut(strings.TrimPrefix(req.URL.Path, "/"), "/")
	r.mu.Lock()
	t := r.targets[tok]
	r.mu.Unlock()
	if t == nil {
		http.NotFound(w, req)
		return
	}

	start, end, ranged := parseRange(req.Header.Get("Range"))
	if req.Method == http.MethodHead {
		start, end = 0, -1
	}

	// The first piece also tells us the total size, which the answer's
	// headers need before any body goes out.
	first, err := t.fetch(req, start, end)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer first.Body.Close()
	length := t.total()
	if end < 0 || (length > 0 && end >= length) {
		end = length - 1
	}

	h := w.Header()
	h.Set("Accept-Ranges", "bytes")
	if ct := first.Header.Get("Content-Type"); ct != "" {
		h.Set("Content-Type", ct)
	}
	if length > 0 {
		h.Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	}
	if ranged && length > 0 {
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, length))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	if req.Method == http.MethodHead {
		return
	}

	pos := start
	body := first.Body
	for {
		n, err := io.Copy(w, body)
		body.Close()
		pos += n
		if err != nil || req.Context().Err() != nil {
			return // the client went away, or the site did
		}
		if length <= 0 || pos > end {
			return
		}
		next, err := t.fetch(req, pos, end)
		if err != nil {
			return
		}
		body = next.Body
	}
}

// fetch asks the site for the next piece: chunk bytes from pos, or up to
// end if that comes first. It records the total size the site reports.
func (t *relayTarget) fetch(req *http.Request, pos, end int64) (*http.Response, error) {
	last := pos + t.chunk - 1
	if end >= 0 && end < last {
		last = end
	}
	up, err := http.NewRequestWithContext(req.Context(), http.MethodGet, t.url, nil)
	if err != nil {
		return nil, err
	}
	for name, vals := range t.header {
		up.Header[name] = vals
	}
	if t.ua != "" {
		up.Header.Set("User-Agent", t.ua)
	}
	up.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", pos, last))
	resp, err := relayPieceClient.Do(up)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		if _, _, total, ok := parseContentRange(resp.Header.Get("Content-Range")); ok {
			t.mu.Lock()
			t.length = total
			t.mu.Unlock()
		}
	case http.StatusOK:
		// The site ignored the range: this one answer is the whole file.
		if resp.ContentLength > 0 && pos == 0 {
			t.mu.Lock()
			t.length = resp.ContentLength
			t.mu.Unlock()
		}
	case http.StatusRequestedRangeNotSatisfiable:
		resp.Body.Close()
		return nil, fmt.Errorf("past the end of the stream")
	default:
		resp.Body.Close()
		return nil, fmt.Errorf("the site answered %s", resp.Status)
	}
	return resp, nil
}

func (t *relayTarget) total() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.length
}

// parseRange reads a single-range "bytes=start-[end]" header; anything
// else means the whole stream.
func parseRange(h string) (start, end int64, ok bool) {
	spec, found := strings.CutPrefix(h, "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, -1, false
	}
	a, b, found := strings.Cut(spec, "-")
	if !found {
		return 0, -1, false
	}
	start, err := strconv.ParseInt(a, 10, 64)
	if err != nil || start < 0 {
		return 0, -1, false
	}
	end = -1
	if b != "" {
		if end, err = strconv.ParseInt(b, 10, 64); err != nil || end < start {
			return 0, -1, false
		}
	}
	return start, end, true
}

// parseContentRange reads "bytes start-end/total".
func parseContentRange(h string) (start, end, total int64, ok bool) {
	spec, found := strings.CutPrefix(h, "bytes ")
	if !found {
		return 0, 0, 0, false
	}
	rng, tot, found := strings.Cut(spec, "/")
	a, b, found2 := strings.Cut(rng, "-")
	if !found || !found2 {
		return 0, 0, 0, false
	}
	var e1, e2, e3 error
	start, e1 = strconv.ParseInt(a, 10, 64)
	end, e2 = strconv.ParseInt(b, 10, 64)
	total, e3 = strconv.ParseInt(tot, 10, 64)
	return start, end, total, e1 == nil && e2 == nil && e3 == nil
}
