package daemon

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"godl/internal/store"
)

func TestPickStreamFilePrefersTheLargestSelectedMedia(t *testing.T) {
	files := []TorrentFile{
		{Index: 0, Path: "Show/info.nfo", Length: 900 << 20}, // largest, but not media
		{Index: 1, Path: "Show/E01.mkv", Length: 400 << 20},
		{Index: 2, Path: "Show/E02.MKV", Length: 500 << 20},
		{Index: 3, Path: "Show/sample.mp4", Length: 10 << 20},
	}
	if got := pickStreamFile(files, []bool{true, true, true, true}); got != 2 {
		t.Errorf("picked file %d, want 2 (the largest video)", got)
	}
	if got := pickStreamFile(files, []bool{true, true, false, true}); got != 1 {
		t.Errorf("picked file %d, want 1 (file 2 isn't selected)", got)
	}
	if got := pickStreamFile(files[:1], nil); got != 0 {
		t.Errorf("with no media at all picked %d, want the largest file (0)", got)
	}
}

func TestStreamReadaheadIsBounded(t *testing.T) {
	for length, want := range map[int64]int64{
		1 << 20:  8 << 20,
		2 << 30:  (2 << 30) / 100,
		50 << 30: 64 << 20,
	} {
		if got := streamReadahead(length); got != want {
			t.Errorf("streamReadahead(%d) = %d, want %d", length, got, want)
		}
	}
}

// The stream server is loopback-only, but another local user could
// still reach it: without the token, nothing is served.
func TestStreamServerRejectsAWrongToken(t *testing.T) {
	d := &Daemon{}
	s := &streamServer{token: "right"}
	for _, path := range []string{"/wrong/job/0/a.mkv", "/", "/right", "/right/job/notanumber/a.mkv"} {
		rec := httptest.NewRecorder()
		d.serveStream(s, rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	d.serveStream(s, rec, httptest.NewRequest(http.MethodPost, "/right/job/0/a.mkv", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rec.Code)
	}
}

func TestYtdlpAuthArgs(t *testing.T) {
	got := YtdlpAuthArgs(store.JobOptions{
		Headers:            []string{"Referer: https://example.com/", "not a header"},
		CookiesFile:        "/tmp/c.txt",
		CookiesFromBrowser: "firefox",
	})
	want := []string{"--add-headers", "Referer:https://example.com/", "--cookies", "/tmp/c.txt", "--cookies-from-browser", "firefox"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("YtdlpAuthArgs = %q, want %q", got, want)
	}
}
