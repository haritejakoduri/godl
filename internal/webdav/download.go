// Package webdav is a minimal WebDAV client: enough to stat a remote
// path, list a directory's immediate children, and download a file with
// basic auth and Range-based resume. It intentionally doesn't implement
// the whole RFC 4918 — just what "godl webdav" needs to walk and pull a
// file or folder tree.
package webdav

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"godl/internal/ratelimit"
)

// downloadIdleTimeout bounds silence, not total transfer time — a large
// file may legitimately take hours, so a flat cap would be wrong. It
// catches a connection that goes quiet mid-transfer and never recovers.
// Var for tests.
var downloadIdleTimeout = 90 * time.Second

// How often the watchdog re-checks; only bounds how late a stall is
// noticed, so it's coarse on purpose.
var idleCheckInterval = 10 * time.Second

// idleWatchdog cancels a download that has gone quiet. It exists as a
// type mostly so Download stays readable: the bookkeeping is fiddly and
// none of it is about downloading.
//
// The fiddly part is sawData/enterLimiter: time spent waiting on the
// rate limiter is godl's own doing, and under a low cap one read can be
// held longer than the timeout — counting that as a stall would kill a
// perfectly healthy download.
type idleWatchdog struct {
	timeout   time.Duration
	lastData  atomic.Int64 // unix nanos
	inLimiter atomic.Bool
	tripped   atomic.Bool
	done      chan struct{}
}

// startIdleWatchdog begins watching, and calls cancel if the download
// goes quiet for longer than downloadIdleTimeout. Timeouts are read once
// here rather than in the goroutine: it can still be scheduled after
// Download returns, where reading these package vars would race a test's
// assignment to them.
func startIdleWatchdog(ctx context.Context, cancel context.CancelFunc) *idleWatchdog {
	wd := &idleWatchdog{timeout: downloadIdleTimeout, done: make(chan struct{})}
	wd.sawData()
	checkEvery := idleCheckInterval
	go func() {
		t := time.NewTicker(checkEvery)
		defer t.Stop()
		for {
			select {
			case <-wd.done:
				return
			case <-ctx.Done():
				return
			case now := <-t.C:
				if wd.inLimiter.Load() {
					continue
				}
				if now.Sub(time.Unix(0, wd.lastData.Load())) > wd.timeout {
					wd.tripped.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	return wd
}

func (w *idleWatchdog) sawData()      { w.lastData.Store(time.Now().UnixNano()) }
func (w *idleWatchdog) enterLimiter() { w.inLimiter.Store(true) }
func (w *idleWatchdog) leaveLimiter() { w.inLimiter.Store(false); w.sawData() }
func (w *idleWatchdog) fired() bool   { return w.tripped.Load() }
func (w *idleWatchdog) stop()         { close(w.done) }

// stallErr describes a transfer the watchdog killed, for the two places
// that have to tell it apart from an ordinary read error.
func (w *idleWatchdog) stallErr(remotePath string) error {
	return fmt.Errorf("downloading %s: no data received for %s, giving up", remotePath, w.timeout)
}

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
	wd := startIdleWatchdog(dlCtx, dlCancel)
	defer wd.stop()

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
		if wd.fired() {
			return start, wd.stallErr(remotePath)
		}
		return start, err
	}
	defer resp.Body.Close()
	wd.sawData() // headers arrived; give the body its own full window rather than sharing the one used to wait for them

	start, err = seekForStatus(f, resp, start, remotePath)
	if err != nil {
		return start, err
	}

	total := int64(-1)
	if resp.ContentLength >= 0 {
		total = start + resp.ContentLength
	}
	return bodyCopy{
		dst:        f,
		src:        resp.Body,
		written:    start,
		total:      total,
		limiter:    limiter,
		global:     globalLimiter,
		wd:         wd,
		progress:   progress,
		remotePath: remotePath,
	}.run(ctx)
}

// seekForStatus positions f for the body about to arrive and reports the
// offset that body starts at: a 206 continues from start, while a 200
// means the server is sending the whole file (either no range was asked
// for, or it was ignored), so the file is truncated and restarted.
func seekForStatus(f *os.File, resp *http.Response, start int64, remotePath string) (int64, error) {
	switch resp.StatusCode {
	case http.StatusOK:
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		return 0, f.Truncate(0)
	case http.StatusPartialContent:
		_, err := f.Seek(start, io.SeekStart)
		return start, err
	default:
		return start, fmt.Errorf("unexpected status downloading %s: %s", remotePath, resp.Status)
	}
}

// bodyCopy streams a response body to disk under both rate limiters and
// the idle watchdog, reporting progress as it goes.
type bodyCopy struct {
	dst        *os.File
	src        io.Reader
	written    int64 // bytes already on disk, i.e. where dst is positioned
	total      int64 // -1 when the server didn't say
	limiter    *rate.Limiter
	global     *rate.Limiter
	wd         *idleWatchdog
	progress   func(done, total int64)
	remotePath string
}

// run returns the total bytes on disk once the body is exhausted.
func (cp bodyCopy) run(ctx context.Context) (int64, error) {
	// 256KiB, not a smaller default: fewer Read/Write syscalls per MB
	// transferred (see the matching constant in internal/downloader).
	buf := make([]byte, 256*1024)
	written := cp.written
	lastReport := time.Now()
	report := func() {
		if cp.progress != nil {
			cp.progress(written, cp.total)
		}
	}
	for {
		n, rerr := cp.src.Read(buf)
		if n > 0 {
			cp.wd.sawData()
			cp.wd.enterLimiter()
			werr := ratelimit.WaitAll(ctx, n, cp.limiter, cp.global)
			cp.wd.leaveLimiter()
			if werr != nil {
				return written, werr
			}
			if _, werr := cp.dst.Write(buf[:n]); werr != nil {
				return written, werr
			}
			written += int64(n)
			if time.Since(lastReport) > 200*time.Millisecond {
				report()
				lastReport = time.Now()
			}
		}
		switch {
		case rerr == nil:
		case rerr == io.EOF:
			report()
			return written, nil
		case cp.wd.fired():
			return written, cp.wd.stallErr(cp.remotePath)
		case ctx.Err() != nil:
			return written, ctx.Err()
		default:
			return written, rerr
		}
	}
}
