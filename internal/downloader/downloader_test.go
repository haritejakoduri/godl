package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
