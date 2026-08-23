package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFilenameFromContentDisposition(t *testing.T) {
	cases := []struct {
		name string
		cd   string
		want string
	}{
		{"basic", `attachment; filename="movie.mp4"`, "movie.mp4"},
		{"unquoted", `attachment; filename=movie.mp4`, "movie.mp4"},
		{"rfc5987", `attachment; filename*=UTF-8''some%20file.mp4`, "some file.mp4"},
		{"prefers filename over filename*", `attachment; filename="plain.mp4"; filename*=UTF-8''fancy.mp4`, "fancy.mp4"},
		{"path traversal stripped", `attachment; filename="../../etc/passwd"`, "passwd"},
		{"empty", ``, ""},
		{"garbage", `not a valid header`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := filenameFromContentDisposition(c.cd)
			if got != c.want {
				t.Errorf("filenameFromContentDisposition(%q) = %q, want %q", c.cd, got, c.want)
			}
		})
	}
}

func TestExtByContentType(t *testing.T) {
	cases := []struct {
		ct   string
		want string
	}{
		{"video/mp4", ".mp4"},
		{"video/mp4; charset=binary", ".mp4"},
		{"application/zip", ".zip"},
		{"image/jpeg", ".jpg"},
		{"application/octet-stream", ""},
		{"nonsense/type-that-does-not-exist", ""},
	}
	for _, c := range cases {
		got := extByContentType(c.ct)
		if got != c.want {
			t.Errorf("extByContentType(%q) = %q, want %q", c.ct, got, c.want)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"movie.mp4", "movie.mp4"},
		{"../../etc/passwd", "passwd"},
		{"  spaced.mp4  ", "spaced.mp4"},
		{"", ""},
		{".", ""},
		{"/", ""},
	}
	for _, c := range cases {
		got := sanitizeFilename(c.in)
		if got != c.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSniffExt(t *testing.T) {
	// A server that responds with a generic/missing Content-Type (e.g.
	// application/octet-stream) still lets us recover a real extension
	// via magic-byte sniffing of the body — the fix for links that used
	// to end up with no file extension at all.
	png := []byte("\x89PNG\r\n\x1a\n" + "rest of a fake png file's bytes")
	if got := sniffExt(bytes.NewReader(png)); got != ".png" {
		t.Errorf("sniffExt(png bytes) = %q, want %q", got, ".png")
	}

	if got := sniffExt(bytes.NewReader(nil)); got != "" {
		t.Errorf("sniffExt(empty) = %q, want empty", got)
	}
}

func TestFilenameFromURLSniffsExtensionWhenContentTypeIsGeneric(t *testing.T) {
	// Regression test for links whose server gives no usable extension
	// hint at all (opaque path, generic/octet-stream Content-Type, no
	// Content-Disposition) — filenameFromURL should still recover a
	// real extension by sniffing the body's magic bytes rather than
	// saving the file with none.
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("...fake png payload...")...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		if r.Method == http.MethodHead {
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		w.Write(png)
	}))
	defer srv.Close()

	got := filenameFromURL(srv.URL + "/dld/opaque-id-with-no-extension")
	if got != "opaque-id-with-no-extension.png" {
		t.Errorf("filenameFromURL = %q, want %q", got, "opaque-id-with-no-extension.png")
	}
}

func TestFilenameFromURLKeepsExistingExtension(t *testing.T) {
	// Path already has an extension, so no network probe should be needed.
	got := filenameFromURL("https://example.com/files/report.pdf?x=1")
	if got != "report.pdf" {
		t.Errorf("filenameFromURL = %q, want %q", got, "report.pdf")
	}
}
