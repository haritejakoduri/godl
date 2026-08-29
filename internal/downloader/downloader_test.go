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
