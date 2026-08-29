// Package downloader implements the shared HTTP download logic for `godl
// url`: concurrent ranged chunking when the server supports it, a
// single-stream fallback otherwise, pause via context cancellation, and
// resume from a saved offset (single stream) or a chunk sidecar file
// (concurrent).
package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// ProgressFunc is called periodically (roughly every 250ms) with the bytes
// downloaded so far and the total, if known (-1 if unknown).
type ProgressFunc func(done, total int64)

type Options struct {
	URL         string
	OutputPath  string
	Concurrency int
	// StartOffset resumes the single-stream path from a previously saved
	// byte offset. Ignored on the concurrent path, which resumes from its
	// own sidecar file instead.
	StartOffset int64
	Progress    ProgressFunc
	// Limiter, if non-nil, caps this job's total transfer rate across
	// every chunk goroutine combined — the same *rate.Limiter instance
	// is shared by all of them (rate.Limiter is safe for concurrent
	// use), so splitting into more chunks doesn't multiply the cap.
	// nil means unlimited.
	Limiter *rate.Limiter
	// Sha256, if set, is the expected hex digest of the completed file.
	// Verified once after the download reaches 100% (not per-chunk —
	// see verifyChecksum for why a mismatch means starting over rather
	// than a partial repair). Empty means no verification.
	Sha256 string
}

// Result reports how much was written and whether the download reached
// completion. On pause (ctx canceled) or a transient error, Done is
// non-zero progress the caller should persist as the new resume point.
type Result struct {
	BytesDone int64
	Completed bool
}

func sidecarPath(output string) string { return output + ".godl-progress.json" }

// waitLimiter blocks until l's token bucket allows n more bytes through,
// pacing the read loop to opt.Limiter's rate; a nil limiter (unlimited)
// or non-positive n returns immediately.
func waitLimiter(ctx context.Context, l *rate.Limiter, n int) error {
	if l == nil || n <= 0 {
		return nil
	}
	return l.WaitN(ctx, n)
}

// copyBufSize is the read/write buffer size for the streaming copy loops
// below. 256KiB rather than a smaller default (e.g. 32KiB) cuts the
// number of Read/WriteAt syscalls (and the goroutine wakeups that go
// with them) per MB transferred by 8x, which matters most on a
// concurrently chunked download where several goroutines are each
// doing this in parallel.
const copyBufSize = 256 * 1024

func Run(ctx context.Context, opt Options) (Result, error) {
	if opt.Concurrency < 1 {
		opt.Concurrency = 1
	}
	client := &http.Client{}
	supportsRange, total, err := probe(ctx, client, opt.URL)
	if err != nil {
		return Result{}, err
	}

	var res Result
	if opt.Concurrency > 1 && supportsRange && total > 0 {
		res, err = runChunked(ctx, client, opt, total)
	} else {
		res, err = runSingle(ctx, client, opt, supportsRange, total)
	}
	if err != nil || !res.Completed || opt.Sha256 == "" {
		return res, err
	}
	if verr := verifyChecksum(opt.OutputPath, opt.Sha256); verr != nil {
		os.Remove(opt.OutputPath)
		os.Remove(sidecarPath(opt.OutputPath))
		return Result{}, verr
	}
	return res, nil
}

// verifyChecksum hashes the completed download at path and compares it
// (case-insensitively) against wantHex. A mismatch means the source
// served bytes that don't match what the caller expected — most likely
// corruption or tampering in transit, not a bug in godl's own transfer
// path, which is exactly what this check exists to catch. But the
// digest covers the whole file, so a mismatch can't be narrowed down to
// which byte range is wrong: the caller removes the file (and any
// resume sidecar) and the next attempt has no choice but to redownload
// everything, the same tradeoff every whole-file-checksum tool makes
// (curl/wget/aria2 included). True partial repair would need the
// source to publish per-chunk hashes (as BitTorrent does), which plain
// HTTP downloads generally don't have.
func verifyChecksum(path, wantHex string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	gotHex := hex.EncodeToString(h.Sum(nil))
	wantHex = strings.ToLower(strings.TrimSpace(wantHex))
	if gotHex != wantHex {
		return fmt.Errorf("sha256 mismatch (got %s, want %s): the downloaded file doesn't match the expected checksum — likely corrupted or altered in transit, not a godl error; deleted it and a retry will redownload the whole file, since a whole-file checksum can't tell which part was bad", gotHex, wantHex)
	}
	return nil
}

// probe determines whether the server honors byte ranges and, if possible,
// the total content length, via HEAD first and a 1-byte ranged GET as a
// fallback for servers that mishandle HEAD.
func probe(ctx context.Context, client *http.Client, url string) (supportsRange bool, total int64, err error) {
	if req, herr := http.NewRequestWithContext(ctx, http.MethodHead, url, nil); herr == nil {
		if resp, derr := client.Do(req); derr == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				return resp.Header.Get("Accept-Ranges") == "bytes", resp.ContentLength, nil
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, -1, err
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := client.Do(req)
	if err != nil {
		return false, -1, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusPartialContent {
		total := int64(-1)
		if cr := resp.Header.Get("Content-Range"); cr != "" {
			if idx := strings.LastIndex(cr, "/"); idx != -1 {
				if t, perr := strconv.ParseInt(cr[idx+1:], 10, 64); perr == nil {
					total = t
				}
			}
		}
		return true, total, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, -1, fmt.Errorf("unexpected status probing %s: %s", url, resp.Status)
	}
	return false, resp.ContentLength, nil
}

// runSingle streams the whole file (or the remainder, if StartOffset is
// set and the server supports ranges) through one connection.
func runSingle(ctx context.Context, client *http.Client, opt Options, supportsRange bool, total int64) (Result, error) {
	start := int64(0)
	if opt.StartOffset > 0 && supportsRange {
		if fi, serr := os.Stat(opt.OutputPath); serr == nil && fi.Size() >= opt.StartOffset {
			start = opt.StartOffset
		}
	}

	f, err := os.OpenFile(opt.OutputPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()

	if start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return Result{}, err
		}
	} else if err := f.Truncate(0); err != nil {
		return Result{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opt.URL, nil)
	if err != nil {
		return Result{}, err
	}
	if start > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{BytesDone: start}, err
	}
	defer resp.Body.Close()

	if start > 0 && resp.StatusCode != http.StatusPartialContent {
		// Server ignored our range; restart from scratch.
		start = 0
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return Result{}, err
		}
		if err := f.Truncate(0); err != nil {
			return Result{}, err
		}
	} else if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return Result{BytesDone: start}, fmt.Errorf("unexpected status: %s", resp.Status)
	}

	buf := make([]byte, copyBufSize)
	written := start
	lastReport := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if werr := waitLimiter(ctx, opt.Limiter, n); werr != nil {
				return Result{BytesDone: written}, werr
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				return Result{BytesDone: written}, werr
			}
			written += int64(n)
			if opt.Progress != nil && time.Since(lastReport) > 200*time.Millisecond {
				opt.Progress(written, total)
				lastReport = time.Now()
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				if opt.Progress != nil {
					opt.Progress(written, total)
				}
				return Result{BytesDone: written, Completed: true}, nil
			}
			if ctx.Err() != nil {
				return Result{BytesDone: written}, ctx.Err()
			}
			return Result{BytesDone: written}, rerr
		}
	}
}

type chunkState struct {
	Start, End, Done int64
}

type sidecar struct {
	URL    string
	Total  int64
	Chunks []chunkState
}

// runChunked splits the remaining bytes into opt.Concurrency ranged
// requests written directly into their final offsets via WriteAt, so no
// merge step is needed. Progress per chunk is checkpointed to a JSON
// sidecar file every 250ms so a paused/killed download can resume only
// the incomplete parts of each chunk.
func runChunked(ctx context.Context, client *http.Client, opt Options, total int64) (Result, error) {
	scPath := sidecarPath(opt.OutputPath)
	var sc sidecar
	if data, err := os.ReadFile(scPath); err == nil {
		if json.Unmarshal(data, &sc) != nil || sc.URL != opt.URL || sc.Total != total || len(sc.Chunks) == 0 {
			sc = sidecar{}
		}
	}

	if len(sc.Chunks) == 0 {
		n := opt.Concurrency
		chunkSize := total / int64(n)
		chunks := make([]chunkState, 0, n)
		start := int64(0)
		for i := 0; i < n; i++ {
			end := start + chunkSize
			if i == n-1 {
				end = total
			}
			chunks = append(chunks, chunkState{Start: start, End: end})
			start = end
		}
		sc = sidecar{URL: opt.URL, Total: total, Chunks: chunks}

		f, err := os.OpenFile(opt.OutputPath, os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return Result{}, err
		}
		if err := f.Truncate(total); err != nil {
			f.Close()
			return Result{}, err
		}
		f.Close()
	}

	f, err := os.OpenFile(opt.OutputPath, os.O_WRONLY, 0o644)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()

	var mu sync.Mutex
	saveSidecar := func() {
		mu.Lock()
		data, _ := json.Marshal(sc)
		mu.Unlock()
		os.WriteFile(scPath, data, 0o644)
	}

	var doneCounter atomic.Int64
	for _, c := range sc.Chunks {
		doneCounter.Add(c.Done)
	}

	errCh := make(chan error, len(sc.Chunks))
	stopTicker := make(chan struct{})
	var tickWG sync.WaitGroup
	tickWG.Add(1)
	go func() {
		defer tickWG.Done()
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if opt.Progress != nil {
					opt.Progress(doneCounter.Load(), total)
				}
				saveSidecar()
			case <-stopTicker:
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := range sc.Chunks {
		c := &sc.Chunks[i]
		if c.Done >= c.End-c.Start {
			continue
		}
		wg.Add(1)
		go func(c *chunkState) {
			defer wg.Done()
			rangeStart := c.Start + c.Done
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, opt.URL, nil)
			if err != nil {
				errCh <- err
				return
			}
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", rangeStart, c.End-1))
			resp, err := client.Do(req)
			if err != nil {
				if ctx.Err() == nil {
					errCh <- err
				}
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
				errCh <- fmt.Errorf("chunk [%d,%d): unexpected status %s", c.Start, c.End, resp.Status)
				return
			}

			buf := make([]byte, copyBufSize)
			pos := rangeStart
			for {
				n, rerr := resp.Body.Read(buf)
				if n > 0 {
					if werr := waitLimiter(ctx, opt.Limiter, n); werr != nil {
						errCh <- werr
						return
					}
					if _, werr := f.WriteAt(buf[:n], pos); werr != nil {
						errCh <- werr
						return
					}
					pos += int64(n)
					mu.Lock()
					c.Done = pos - c.Start
					mu.Unlock()
					doneCounter.Add(int64(n))
				}
				if rerr != nil {
					if rerr == io.EOF {
						return
					}
					if ctx.Err() != nil {
						return
					}
					errCh <- rerr
					return
				}
			}
		}(c)
	}
	wg.Wait()
	close(stopTicker)
	tickWG.Wait()
	saveSidecar()

	select {
	case e := <-errCh:
		return Result{BytesDone: doneCounter.Load()}, e
	default:
	}
	if ctx.Err() != nil {
		return Result{BytesDone: doneCounter.Load()}, ctx.Err()
	}

	allDone := true
	for _, c := range sc.Chunks {
		if c.Done < c.End-c.Start {
			allDone = false
			break
		}
	}
	if allDone {
		os.Remove(scPath)
		if opt.Progress != nil {
			opt.Progress(total, total)
		}
		return Result{BytesDone: total, Completed: true}, nil
	}
	return Result{BytesDone: doneCounter.Load()}, fmt.Errorf("download did not complete")
}
