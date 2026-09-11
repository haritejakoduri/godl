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
	"errors"
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

	"godl/internal/httpx"
	"godl/internal/ratelimit"
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
	// GlobalLimiter is the daemon-wide cap: one instance shared by every
	// url/webdav job, waited on in addition to Limiter.
	GlobalLimiter *rate.Limiter
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

// waitLimiters blocks until both opt.Limiter (this job's own cap) and
// opt.GlobalLimiter (the daemon-wide shared cap) allow n more bytes
// through — either being nil (unlimited) is a no-op for that one, same
// as n<=0 is for both.
func waitLimiters(ctx context.Context, opt Options, n int) error {
	return ratelimit.WaitAll(ctx, n, opt.Limiter, opt.GlobalLimiter)
}

// 256KiB rather than the usual 32KiB: ~8x fewer syscalls per MB, which
// matters most when several chunk goroutines are copying at once.
const copyBufSize = 256 * 1024

func Run(ctx context.Context, opt Options) (Result, error) {
	if opt.Concurrency < 1 {
		opt.Concurrency = 1
	}
	// See internal/httpx: pooled, no whole-request deadline.
	client := httpx.TransferClient(false)
	supportsRange, total, err := probe(ctx, client, opt.URL)
	if err != nil {
		return Result{}, err
	}

	var res Result
	if opt.Concurrency > 1 && supportsRange && total > 0 {
		res, err = runChunked(ctx, client, opt, total)
		if errors.Is(err, errRangeIgnored) {
			// probe() said this server supports ranges (it advertised
			// Accept-Ranges on HEAD), but it ignored the Range header on
			// the actual GET — so chunking can't work here. Whatever the
			// chunk goroutines managed to write is garbage and can't be
			// resumed from, so drop the sidecar and start over as a
			// single stream (which truncates the file itself). Falling
			// back rather than failing keeps such servers working at all,
			// just without the concurrency.
			os.Remove(sidecarPath(opt.OutputPath))
			single := opt
			single.StartOffset = 0
			res, err = runSingle(ctx, client, single, false, total)
		}
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

// verifyChecksum compares the finished file against wantHex. A whole-file
// digest can't localize the bad range, so a mismatch means the caller
// deletes and redownloads everything — the same tradeoff curl, wget and
// aria2 make. Partial repair would need per-chunk hashes.
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
			if werr := waitLimiters(ctx, opt, n); werr != nil {
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

// errRangeIgnored means the server answered a ranged chunk request with
// 200 (the whole file) instead of 206 (just the requested window). Every
// chunk goroutine would then be handed a full copy of the file and write
// it at its own offset, overwriting its neighbours — so chunking has to
// be abandoned entirely rather than retried. Run catches this and starts
// over as a single stream.
var errRangeIgnored = errors.New("server ignored the Range header")

type chunkState struct {
	Start, End, Done int64
}

type sidecar struct {
	URL    string
	Total  int64
	Chunks []chunkState
}

// fetchChunk downloads one chunk's remaining bytes straight into its
// final offset in f. It returns nil when the chunk is satisfied, when
// the context is canceled, or when the server turned out to be ignoring
// Range headers — in that last case it sets rangeIgnored and cancels the
// siblings, since every one of them is about to write a full copy of the
// file at its own offset.
func fetchChunk(ctx context.Context, client *http.Client, opt Options, f *os.File, c *chunkState,
	mu *sync.Mutex, doneCounter *atomic.Int64, rangeIgnored *atomic.Bool, cancelSiblings func()) error {
	rangeStart := c.Start + c.Done
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opt.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", rangeStart, c.End-1))
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		// Not 206: the server is sending the whole file, not the window
		// we asked for. See errRangeIgnored.
		rangeIgnored.Store(true)
		cancelSiblings()
		return nil
	}
	if resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("chunk [%d,%d): unexpected status %s", c.Start, c.End, resp.Status)
	}

	buf := make([]byte, copyBufSize)
	pos := rangeStart
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			// Never write outside this chunk's own [Start,End) window,
			// whatever the server sends. Extra bytes would overwrite the
			// next chunk's region, and since c.Done is derived from pos
			// they would also push the completion check past this chunk's
			// length and report a corrupt file as finished.
			if over := pos + int64(n) - c.End; over > 0 {
				n -= int(over)
			}
			if n > 0 {
				if werr := waitLimiters(ctx, opt, n); werr != nil {
					return werr
				}
				if _, werr := f.WriteAt(buf[:n], pos); werr != nil {
					return werr
				}
				pos += int64(n)
				mu.Lock()
				c.Done = pos - c.Start
				mu.Unlock()
				doneCounter.Add(int64(n))
			}
			if pos >= c.End {
				return nil // satisfied; ignore any trailing bytes
			}
		}
		if rerr != nil {
			if rerr == io.EOF || ctx.Err() != nil {
				return nil
			}
			return rerr
		}
	}
}

// loadOrInitSidecar returns the resume state for this download: the
// existing sidecar when it still describes the same URL and size, or a
// fresh even split of total across opt.Concurrency chunks, with the
// output file pre-truncated to its final size so every chunk can WriteAt
// straight into its own window.
func loadOrInitSidecar(opt Options, scPath string, total int64) (sidecar, error) {
	var sc sidecar
	if data, err := os.ReadFile(scPath); err == nil {
		if json.Unmarshal(data, &sc) != nil || sc.URL != opt.URL || sc.Total != total || len(sc.Chunks) == 0 {
			sc = sidecar{}
		}
	}
	if len(sc.Chunks) > 0 {
		return sc, nil
	}

	n := opt.Concurrency
	chunkSize := total / int64(n)
	chunks := make([]chunkState, 0, n)
	for i, start := 0, int64(0); i < n; i++ {
		end := start + chunkSize
		if i == n-1 {
			end = total
		}
		chunks = append(chunks, chunkState{Start: start, End: end})
		start = end
	}

	f, err := os.OpenFile(opt.OutputPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return sidecar{}, err
	}
	defer f.Close()
	if err := f.Truncate(total); err != nil {
		return sidecar{}, err
	}
	return sidecar{URL: opt.URL, Total: total, Chunks: chunks}, nil
}

// writeSidecar persists resume state via temp file + rename: this runs
// every 250ms for the life of the download, and a crash mid-write leaves
// truncated JSON that the resume path discards outright — losing a whole
// multi-GB download.
func writeSidecar(scPath string, data []byte) {
	tmp := scPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, scPath); err != nil {
		os.Remove(tmp)
	}
}

// startTicker runs tick every 250ms until the returned stop func is
// called, which also waits for any in-flight tick to finish.
func startTicker(tick func()) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				tick()
			case <-done:
				return
			}
		}
	}()
	return func() { close(done); wg.Wait() }
}

// runChunked splits the remaining bytes into opt.Concurrency ranged
// requests written directly into their final offsets via WriteAt, so no
// merge step is needed. Progress per chunk is checkpointed to a JSON
// sidecar file every 250ms so a paused/killed download can resume only
// the incomplete parts of each chunk.
func runChunked(ctx context.Context, client *http.Client, opt Options, total int64) (Result, error) {
	scPath := sidecarPath(opt.OutputPath)
	sc, err := loadOrInitSidecar(opt, scPath, total)
	if err != nil {
		return Result{}, err
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
		writeSidecar(scPath, data)
	}

	var doneCounter atomic.Int64
	for _, c := range sc.Chunks {
		doneCounter.Add(c.Done)
	}

	stopTicker := startTicker(func() {
		if opt.Progress != nil {
			opt.Progress(doneCounter.Load(), total)
		}
		saveSidecar()
	})

	// cctx cancels the sibling chunk goroutines the moment one of them
	// discovers the server is ignoring Range headers — there's no point
	// letting the rest keep streaming whole-file copies that are about to
	// be thrown away.
	cctx, cancelChunks := context.WithCancel(ctx)
	defer cancelChunks()
	var rangeIgnored atomic.Bool

	errCh := make(chan error, len(sc.Chunks))
	var wg sync.WaitGroup
	for i := range sc.Chunks {
		c := &sc.Chunks[i]
		if c.Done >= c.End-c.Start {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fetchChunk(cctx, client, opt, f, c, &mu, &doneCounter, &rangeIgnored, cancelChunks); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	stopTicker()

	// Checked before saving the sidecar and before the error/cancellation
	// paths below: cancelChunks above makes cctx (and any sibling's
	// error) report cancellation, which would otherwise mask the real
	// reason. The partial file is unusable, so its resume state must not
	// be persisted either.
	if rangeIgnored.Load() {
		return Result{}, errRangeIgnored
	}
	saveSidecar()

	select {
	case e := <-errCh:
		return Result{BytesDone: doneCounter.Load()}, e
	default:
	}
	if ctx.Err() != nil {
		return Result{BytesDone: doneCounter.Load()}, ctx.Err()
	}

	for _, c := range sc.Chunks {
		if c.Done < c.End-c.Start {
			return Result{BytesDone: doneCounter.Load()}, fmt.Errorf("download did not complete")
		}
	}
	os.Remove(scPath)
	if opt.Progress != nil {
		opt.Progress(total, total)
	}
	return Result{BytesDone: total, Completed: true}, nil
}
