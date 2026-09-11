package downloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func digestHex(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

func TestRunVerifiesChecksumSuccess(t *testing.T) {
	body := []byte("hello godl checksum verification, this is the file content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Write(body)
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "out.bin")
	res, err := Run(context.Background(), Options{
		URL:        srv.URL,
		OutputPath: out,
		Sha256:     digestHex(body),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("expected Completed=true, got %+v", res)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("output content mismatch: got %q want %q", got, body)
	}
}

func TestRunChecksumMismatchDeletesFileAndFails(t *testing.T) {
	body := []byte("some file content that will not match the expected digest")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Write(body)
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "out.bin")
	wrongHex := strings.Repeat("0", 64)
	res, err := Run(context.Background(), Options{
		URL:        srv.URL,
		OutputPath: out,
		Sha256:     wrongHex,
	})
	if err == nil {
		t.Fatal("expected an error on checksum mismatch, got nil")
	}
	if res.Completed {
		t.Fatalf("expected Completed=false on mismatch, got %+v", res)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("expected output file to be removed on mismatch, stat err = %v", statErr)
	}
}

func TestRunSkipsVerificationWhenSha256Empty(t *testing.T) {
	body := []byte("content that is never hashed since Sha256 is empty")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Write(body)
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "out.bin")
	res, err := Run(context.Background(), Options{URL: srv.URL, OutputPath: out})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("expected Completed=true, got %+v", res)
	}
	if _, statErr := os.Stat(out); statErr != nil {
		t.Fatalf("expected output file to remain, stat err = %v", statErr)
	}
}

// TestRunHonorsGlobalLimiter confirms Options.GlobalLimiter actually
// participates in the copy loop (via waitLimiters), not just Limiter —
// an exhausted global limiter with a short-deadline context must make
// Run fail rather than complete instantly, even though the per-job
// Limiter field is left nil (unlimited).
func TestRunHonorsGlobalLimiter(t *testing.T) {
	body := []byte("this file would download instantly if GlobalLimiter were ignored")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Write(body)
	}))
	defer srv.Close()

	exhausted := rate.NewLimiter(rate.Limit(0.0001), 1) // ~1 token per ~3 hours
	exhausted.AllowN(time.Now(), 1)                     // drain the one burst token

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	out := filepath.Join(t.TempDir(), "out.bin")
	_, err := Run(ctx, Options{URL: srv.URL, OutputPath: out, GlobalLimiter: exhausted})
	if err == nil {
		t.Fatal("Run with an exhausted GlobalLimiter and a short deadline succeeded, want an error (GlobalLimiter was not consulted)")
	}
}

// rangeIgnoringServer advertises range support on HEAD (which is what
// probe() trusts to choose the chunked path) but answers every ranged
// GET with 200 and the whole body — the exact shape of server that used
// to make every chunk goroutine write a full copy of the file at its own
// offset, silently producing a corrupt result that still reported
// success.
func rangeIgnoringServer(body []byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK) // 200 even though a Range was asked for
		w.Write(body)
	}))
}

func TestRunFallsBackWhenServerIgnoresRange(t *testing.T) {
	// Large enough that the chunks are distinct multi-byte windows, so a
	// regression really does interleave garbage rather than coincidentally
	// landing the right bytes.
	body := []byte(strings.Repeat("godl-range-ignored-payload-", 4000))
	srv := rangeIgnoringServer(body)
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "out.bin")
	res, err := Run(context.Background(), Options{
		URL:         srv.URL,
		OutputPath:  out,
		Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("expected Completed=true after the single-stream fallback, got %+v", res)
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// Compared byte-for-byte, not by length: the corruption this guards
	// against produced a file of plausible (even exact) size whose
	// contents were several overlapping copies.
	if !bytes.Equal(got, body) {
		t.Errorf("downloaded file doesn't match the source (%d bytes vs %d) — chunks wrote outside their own ranges", len(got), len(body))
	}
	if _, err := os.Stat(sidecarPath(out)); !os.IsNotExist(err) {
		t.Errorf("resume sidecar still present after a completed download: %v", err)
	}
}

// TestChunkWritesAreClampedToTheirRange covers the other half of the same
// bug: a server that honors Range with a 206 but then sends more bytes
// than the window asked for. Without the clamp those extra bytes land in
// the next chunk's region, and c.Done runs past the chunk length so the
// completion check passes on a corrupt file.
func TestChunkWritesAreClampedToTheirRange(t *testing.T) {
	body := []byte(strings.Repeat("0123456789abcdef", 2000))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		rangeHdr := r.Header.Get("Range")
		if rangeHdr == "" {
			w.WriteHeader(http.StatusOK)
			w.Write(body)
			return
		}
		var start, end int64
		if _, err := fmt.Sscanf(rangeHdr, "bytes=%d-%d", &start, &end); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Overrun the requested window by 512 bytes of filler that does
		// NOT match the real file. Sending the correct trailing bytes
		// would make an unclamped write harmless by luck — the neighbour
		// would be overwritten with exactly what belonged there — and the
		// test would pass with or without the clamp.
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body[start : end+1])
		w.Write(bytes.Repeat([]byte{0xFF}, 512))
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "out.bin")
	res, err := Run(context.Background(), Options{
		URL:         srv.URL,
		OutputPath:  out,
		Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Completed {
		t.Fatalf("expected Completed=true, got %+v", res)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("output differs from source (%d bytes vs %d) — an over-long 206 wrote past its chunk", len(got), len(body))
	}
}

// TestSidecarSurvivesATruncatedWrite is the resume-durability half: a
// sidecar left truncated by a crash mid-write used to be discarded
// wholesale, resetting a multi-GB download to zero. The temp-file+rename
// write means a reader only ever sees a complete file, so this asserts
// the recovery path still works when one is left behind mid-download.
func TestSidecarSurvivesATruncatedWrite(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.bin")
	sc := sidecarPath(out)
	if err := os.WriteFile(sc, []byte(`{"URL":"x","Total":10,"Chun`), 0o644); err != nil {
		t.Fatal(err)
	}

	body := []byte(strings.Repeat("resume-after-truncated-sidecar-", 500))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.ServeContent(w, r, "out.bin", time.Time{}, bytes.NewReader(body))
	}))
	defer srv.Close()

	res, err := Run(context.Background(), Options{URL: srv.URL, OutputPath: out, Concurrency: 4})
	if err != nil {
		t.Fatalf("Run with a truncated sidecar present: %v", err)
	}
	if !res.Completed {
		t.Fatalf("expected Completed=true, got %+v", res)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("output differs from source after recovering from a truncated sidecar")
	}
}
