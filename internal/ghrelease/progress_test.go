package ghrelease

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestDownloadVerifiedReportsProgress wires the real download against a
// real server, because the thing that goes wrong here is plumbing: a
// reader wrapped in the wrong place reports the wrong bytes, or none,
// and the download still succeeds so nothing fails.
func TestDownloadVerifiedReportsProgress(t *testing.T) {
	body := make([]byte, 512*1024)
	for i := range body {
		body[i] = byte(i)
	}
	sum := sha256.Sum256(body)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Write(body)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "thing")
	var calls int
	var lastDone, lastTotal int64
	err := DownloadVerified(context.Background(), srv.Client(), srv.URL, dest, ".part",
		hex.EncodeToString(sum[:]),
		func(done, total int64) {
			if done < lastDone {
				t.Errorf("progress went backwards: %d after %d", done, lastDone)
			}
			calls++
			lastDone, lastTotal = done, total
		})
	if err != nil {
		t.Fatal(err)
	}

	if calls == 0 {
		t.Fatal("no progress reported; the reader is not wrapped where the bytes flow")
	}
	if lastDone != int64(len(body)) {
		t.Errorf("final done = %d, want %d — progress stops short of what arrived", lastDone, len(body))
	}
	if lastTotal != int64(len(body)) {
		t.Errorf("total = %d, want %d from Content-Length", lastTotal, len(body))
	}
	// And the file is still correct: progress must not disturb the
	// bytes or the hash they are verified against.
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(body) {
		t.Errorf("downloaded %d bytes, want %d", len(got), len(body))
	}
}

// A server that sends no Content-Length reports total 0, which callers
// render as "no denominator" rather than drawing a bar against a
// number nobody gave them.
func TestProgressWithoutContentLength(t *testing.T) {
	body := []byte("hello, world")
	sum := sha256.Sum256(body)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Transfer-Encoding", "chunked")
		w.Write(body)
	}))
	defer srv.Close()

	var sawTotal int64 = -1
	err := DownloadVerified(context.Background(), srv.Client(), srv.URL,
		filepath.Join(t.TempDir(), "thing"), ".part", hex.EncodeToString(sum[:]),
		func(done, total int64) { sawTotal = total })
	if err != nil {
		t.Fatal(err)
	}
	if sawTotal != 0 {
		t.Errorf("total = %d with no Content-Length, want 0 (meaning unknown)", sawTotal)
	}
}

// A nil callback is the common case — the daemon and the player pass
// none — and must not cost anything or crash.
func TestDownloadVerifiedWithoutProgress(t *testing.T) {
	body := []byte("x")
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()

	if err := DownloadVerified(context.Background(), srv.Client(), srv.URL,
		filepath.Join(t.TempDir(), "thing"), ".part", hex.EncodeToString(sum[:]), nil); err != nil {
		t.Fatal(err)
	}
}
