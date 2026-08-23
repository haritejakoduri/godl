package webdav

import (
	"context"
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

	n, err := c.Download(context.Background(), "/file1.txt", local, nil)
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
	n, err = c.Download(context.Background(), "/file1.txt", local, nil)
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
