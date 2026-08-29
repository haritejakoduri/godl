package webdav

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// newTestServer serves a small fake WebDAV tree rooted at /dav/:
//
//	/dav/                  (collection)
//	/dav/file1.txt         (12 bytes: "hello world!")
//	/dav/subdir/           (collection)
//	/dav/subdir/file2.txt  (5 bytes: "world")
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	const (
		file1 = "hello world!"
		file2 = "world"
	)

	mux := http.NewServeMux()
	propfind := func(w http.ResponseWriter, r *http.Request, body string) {
		if r.Method != "PROPFIND" {
			http.Error(w, "want PROPFIND", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(body))
	}

	mux.HandleFunc("/dav/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			if r.Header.Get("Depth") == "0" {
				propfind(w, r, `<?xml version="1.0"?>
<D:multistatus xmlns:D="DAV:">
  <D:response><D:href>/dav/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`)
				return
			}
			propfind(w, r, `<?xml version="1.0"?>
<D:multistatus xmlns:D="DAV:">
  <D:response><D:href>/dav/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
  <D:response><D:href>/dav/file1.txt</D:href><D:propstat><D:prop><D:resourcetype/><D:getcontentlength>12</D:getcontentlength></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
  <D:response><D:href>/dav/subdir/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`)
			return
		}
		http.NotFound(w, r)
	})

	mux.HandleFunc("/dav/file1.txt", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			propfind(w, r, `<?xml version="1.0"?>
<D:multistatus xmlns:D="DAV:">
  <D:response><D:href>/dav/file1.txt</D:href><D:propstat><D:prop><D:resourcetype/><D:getcontentlength>12</D:getcontentlength></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`)
			return
		}
		http.ServeContent(w, r, "file1.txt", time.Time{}, strings.NewReader(file1))
	})

	mux.HandleFunc("/dav/subdir/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			propfind(w, r, `<?xml version="1.0"?>
<D:multistatus xmlns:D="DAV:">
  <D:response><D:href>/dav/subdir/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
  <D:response><D:href>/dav/subdir/file2.txt</D:href><D:propstat><D:prop><D:resourcetype/><D:getcontentlength>5</D:getcontentlength></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`)
			return
		}
		http.NotFound(w, r)
	})

	mux.HandleFunc("/dav/subdir/file2.txt", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			propfind(w, r, `<?xml version="1.0"?>
<D:multistatus xmlns:D="DAV:">
  <D:response><D:href>/dav/subdir/file2.txt</D:href><D:propstat><D:prop><D:resourcetype/><D:getcontentlength>5</D:getcontentlength></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`)
			return
		}
		http.ServeContent(w, r, "file2.txt", time.Time{}, strings.NewReader(file2))
	})

	return httptest.NewServer(mux)
}

func TestStatFile(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	c, err := New(srv.URL+"/dav/", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	e, err := c.Stat(context.Background(), "/file1.txt")
	if err != nil {
		t.Fatal(err)
	}
	if e.IsDir || e.Size != 12 {
		t.Errorf("Stat(/file1.txt) = %+v, want a 12-byte file", e)
	}
}

func TestStatDir(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	c, err := New(srv.URL+"/dav/", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	e, err := c.Stat(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if !e.IsDir {
		t.Errorf("Stat(/) = %+v, want a directory", e)
	}
}

func TestList(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	c, err := New(srv.URL+"/dav/", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := c.List(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("List(/) returned %d entries, want 2 (self excluded): %+v", len(entries), entries)
	}
	var paths []string
	for _, e := range entries {
		paths = append(paths, e.Path)
	}
	sort.Strings(paths)
	want := []string{"/file1.txt", "/subdir/"}
	sort.Strings(want)
	if paths[0] != want[0] || paths[1] != want[1] {
		t.Errorf("List(/) paths = %v, want %v", paths, want)
	}
}

func TestWalkRecurses(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	c, err := New(srv.URL+"/dav/", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	files, err := c.Walk(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("Walk(/) returned %d files, want 2: %+v", len(files), files)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
		if f.IsDir {
			t.Errorf("Walk returned a directory entry: %+v", f)
		}
	}
	sort.Strings(paths)
	want := []string{"/file1.txt", "/subdir/file2.txt"}
	if paths[0] != want[0] || paths[1] != want[1] {
		t.Errorf("Walk(/) paths = %v, want %v", paths, want)
	}
}

func TestDownloadAndResume(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	c, err := New(srv.URL+"/dav/", "", "", false)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	local := filepath.Join(dir, "out.txt")

	n, err := c.Download(context.Background(), "/file1.txt", local, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 12 {
		t.Errorf("Download wrote %d bytes, want 12", n)
	}
	data, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world!" {
		t.Errorf("downloaded content = %q, want %q", data, "hello world!")
	}

	// Truncate to simulate a partial download, then re-download: it
	// should resume via Range rather than starting over.
	if err := os.Truncate(local, 6); err != nil {
		t.Fatal(err)
	}
	n, err = c.Download(context.Background(), "/file1.txt", local, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 12 {
		t.Errorf("resumed Download wrote total %d bytes, want 12", n)
	}
	data, err = os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world!" {
		t.Errorf("resumed content = %q, want %q", data, "hello world!")
	}
}

// TestWalkWideTreeFindsEveryFile exercises Walk's concurrent fan-out
// properly: newTestServer's tree has only one subdirectory, never
// stressing walkConcurrency's semaphore or the shared files slice's
// locking. This tree has many sibling directories, each with its own
// files, so multiple PROPFIND requests are genuinely in flight at once.
func TestWalkWideTreeFindsEveryFile(t *testing.T) {
	const dirs = 20
	const filesPerDir = 3

	mux := http.NewServeMux()
	multistatus := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(body))
	}
	mux.HandleFunc("/wide/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			http.NotFound(w, r)
			return
		}
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><D:multistatus xmlns:D="DAV:">`)
		b.WriteString(`<D:response><D:href>/wide/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`)
		for i := 0; i < dirs; i++ {
			fmt.Fprintf(&b, `<D:response><D:href>/wide/d%d/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`, i)
		}
		b.WriteString(`</D:multistatus>`)
		multistatus(w, b.String())
	})
	for i := 0; i < dirs; i++ {
		i := i
		mux.HandleFunc(fmt.Sprintf("/wide/d%d/", i), func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "PROPFIND" {
				http.NotFound(w, r)
				return
			}
			var b strings.Builder
			b.WriteString(`<?xml version="1.0"?><D:multistatus xmlns:D="DAV:">`)
			fmt.Fprintf(&b, `<D:response><D:href>/wide/d%d/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`, i)
			for j := 0; j < filesPerDir; j++ {
				fmt.Fprintf(&b, `<D:response><D:href>/wide/d%d/f%d.txt</D:href><D:propstat><D:prop><D:resourcetype/><D:getcontentlength>7</D:getcontentlength></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`, i, j)
			}
			b.WriteString(`</D:multistatus>`)
			multistatus(w, b.String())
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := New(srv.URL+"/wide/", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	files, err := c.Walk(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != dirs*filesPerDir {
		t.Fatalf("Walk found %d files, want %d", len(files), dirs*filesPerDir)
	}
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f.Path] {
			t.Errorf("duplicate entry for %s", f.Path)
		}
		seen[f.Path] = true
		if f.IsDir {
			t.Errorf("%s reported as a directory", f.Path)
		}
	}
}

// TestPropfindRetriesOn429ThenSucceeds is the core regression test:
// some WebDAV backends (cloud-storage-proxying services in particular)
// rate-limit aggressively enough that even a lone PROPFIND against the
// root can get 429'd — this must be retried rather than surfaced as a
// hard failure on the first response.
func TestPropfindRetriesOn429ThenSucceeds(t *testing.T) {
	orig := retryBackoffUnit
	retryBackoffUnit = time.Millisecond
	t.Cleanup(func() { retryBackoffUnit = orig })

	var attempts int
	mux := http.NewServeMux()
	mux.HandleFunc("/dav/", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(`<?xml version="1.0"?><D:multistatus xmlns:D="DAV:">
  <D:response><D:href>/dav/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := New(srv.URL+"/dav/", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Stat(context.Background(), "/"); err != nil {
		t.Fatalf("Stat after two 429s then success: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("server saw %d attempts, want exactly 3 (2 failed + 1 success)", attempts)
	}
}

// TestPropfindHonorsRetryAfterHeader confirms a numeric Retry-After
// header is actually used to pace the retry, not just ignored in favor
// of the exponential fallback.
func TestPropfindHonorsRetryAfterHeader(t *testing.T) {
	orig := retryBackoffUnit
	retryBackoffUnit = time.Hour // would time the test out if this were used instead of Retry-After
	t.Cleanup(func() { retryBackoffUnit = orig })

	var attempts int
	var firstAttempt, secondAttempt time.Time
	mux := http.NewServeMux()
	mux.HandleFunc("/dav/", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			firstAttempt = time.Now()
			w.Header().Set("Retry-After", "0") // honored as "retry almost immediately"
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		secondAttempt = time.Now()
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(`<?xml version="1.0"?><D:multistatus xmlns:D="DAV:">
  <D:response><D:href>/dav/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := New(srv.URL+"/dav/", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Stat(context.Background(), "/")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stat took too long — Retry-After: 0 was not honored (fell back to the 1-hour exponential unit instead)")
	}
	if secondAttempt.Sub(firstAttempt) > time.Second {
		t.Fatalf("retry took %v, want well under a second (Retry-After: 0 should have been used, not the 1-hour fallback unit)", secondAttempt.Sub(firstAttempt))
	}
}

// TestPropfindGivesUpAfterMaxAttempts confirms a server that never
// stops returning 429 eventually surfaces as a real error instead of
// retrying forever.
func TestPropfindGivesUpAfterMaxAttempts(t *testing.T) {
	orig := retryBackoffUnit
	retryBackoffUnit = time.Millisecond
	t.Cleanup(func() { retryBackoffUnit = orig })

	var attempts int
	mux := http.NewServeMux()
	mux.HandleFunc("/dav/", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := New(srv.URL+"/dav/", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Stat(context.Background(), "/"); err == nil {
		t.Fatal("Stat against a server that always 429s succeeded, want an error")
	}
	if attempts != retry429Max {
		t.Fatalf("server saw %d attempts, want exactly retry429Max (%d)", attempts, retry429Max)
	}
}

// TestDownloadRetriesOn429ThenSucceeds confirms the retry applies to
// Download's GET request too, not just PROPFIND.
func TestDownloadRetriesOn429ThenSucceeds(t *testing.T) {
	orig := retryBackoffUnit
	retryBackoffUnit = time.Millisecond
	t.Cleanup(func() { retryBackoffUnit = orig })

	const content = "hello from behind a rate limit"
	var attempts int
	mux := http.NewServeMux()
	mux.HandleFunc("/dav/file.txt", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(content))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := New(srv.URL+"/dav/", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "file.txt")
	n, err := c.Download(context.Background(), "/file.txt", local, nil, nil, nil)
	if err != nil {
		t.Fatalf("Download after one 429 then success: %v", err)
	}
	if n != int64(len(content)) {
		t.Fatalf("Download returned %d bytes, want %d", n, len(content))
	}
	got, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("downloaded content = %q, want %q", got, content)
	}
}
