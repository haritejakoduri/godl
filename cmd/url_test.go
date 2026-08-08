package cmd

import "testing"

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

func TestFilenameFromURLKeepsExistingExtension(t *testing.T) {
	// Path already has an extension, so no network probe should be needed.
	got := filenameFromURL("https://example.com/files/report.pdf?x=1")
	if got != "report.pdf" {
		t.Errorf("filenameFromURL = %q, want %q", got, "report.pdf")
	}
}
