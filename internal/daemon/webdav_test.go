package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"godl/internal/connections"
	"godl/internal/store"
)

// newTestWebDAVServer serves a tiny fake WebDAV tree rooted at /dav/:
//
//	/dav/            (collection)
//	/dav/a.txt       ("hello root\n")
//	/dav/sub/        (collection)
//	/dav/sub/b.txt   ("hello sub\n")
func newTestWebDAVServer(t *testing.T) *httptest.Server {
	t.Helper()
	const (
		fileA = "hello root\n"
		fileB = "hello sub\n"
	)
	multistatus := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(body))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/dav/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Depth") == "0" {
			multistatus(w, `<?xml version="1.0"?><D:multistatus xmlns:D="DAV:">
  <D:response><D:href>/dav/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`)
			return
		}
		multistatus(w, `<?xml version="1.0"?><D:multistatus xmlns:D="DAV:">
  <D:response><D:href>/dav/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
  <D:response><D:href>/dav/a.txt</D:href><D:propstat><D:prop><D:resourcetype/><D:getcontentlength>11</D:getcontentlength></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
  <D:response><D:href>/dav/sub/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`)
	})
	mux.HandleFunc("/dav/a.txt", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			multistatus(w, `<?xml version="1.0"?><D:multistatus xmlns:D="DAV:">
  <D:response><D:href>/dav/a.txt</D:href><D:propstat><D:prop><D:resourcetype/><D:getcontentlength>11</D:getcontentlength></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`)
			return
		}
		http.ServeContent(w, r, "a.txt", time.Time{}, strings.NewReader(fileA))
	})
	mux.HandleFunc("/dav/sub/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			http.NotFound(w, r)
			return
		}
		multistatus(w, `<?xml version="1.0"?><D:multistatus xmlns:D="DAV:">
  <D:response><D:href>/dav/sub/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
  <D:response><D:href>/dav/sub/b.txt</D:href><D:propstat><D:prop><D:resourcetype/><D:getcontentlength>10</D:getcontentlength></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`)
	})
	mux.HandleFunc("/dav/sub/b.txt", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "b.txt", time.Time{}, strings.NewReader(fileB))
	})
	return httptest.NewServer(mux)
}

// newTestDaemon builds a Daemon backed by a real store in a temp dir, but
// without a torrent manager — startWebDAV never touches d.tm, so tests
// that only exercise the WebDAV path don't need one (and avoiding it
// sidesteps torrentmgr.New's network listen, which isn't available in
// every sandbox).
func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "godl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Daemon{
		st:       st,
		dataDir:  dir,
		runtimes: map[string]*runtime{},
		logSubs:  map[chan logMsg]struct{}{},
	}
}

func waitForTerminal(t *testing.T, d *Daemon, id string) *store.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, err := d.st.GetJob(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		switch j.Status {
		case store.StatusCompleted, store.StatusFailed, store.StatusCanceled, store.StatusPaused:
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach a terminal state in time", id)
	return nil
}

func TestStartWebDAVDownloadsFolderRecursively(t *testing.T) {
	t.Setenv("GODL_DATA_DIR", t.TempDir())
	srv := newTestWebDAVServer(t)
	defer srv.Close()

	if err := connections.Add(connections.Connection{
		Name: "myconn", Type: connections.TypeWebDAV, URL: srv.URL + "/dav/",
	}); err != nil {
		t.Fatal(err)
	}

	d := newTestDaemon(t)
	output := t.TempDir()

	j, err := d.createJob(context.Background(), store.JobWebDAV, "myconn:/", output, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	d.startWebDAV(j)
	final := waitForTerminal(t, d, j.ID)

	if final.Status != store.StatusCompleted {
		t.Fatalf("job ended as %s: %s", final.Status, final.ErrorMsg)
	}
	if final.BytesDone != 21 {
		t.Errorf("BytesDone = %d, want 21 (11 + 10)", final.BytesDone)
	}
	if len(final.ResolvedPaths) != 2 {
		t.Fatalf("ResolvedPaths = %v, want 2 entries", final.ResolvedPaths)
	}

	a, err := os.ReadFile(filepath.Join(output, "a.txt"))
	if err != nil || string(a) != "hello root\n" {
		t.Errorf("a.txt = %q, %v", a, err)
	}
	b, err := os.ReadFile(filepath.Join(output, "sub", "b.txt"))
	if err != nil || string(b) != "hello sub\n" {
		t.Errorf("sub/b.txt = %q, %v", b, err)
	}
}

func TestStartWebDAVDownloadsSingleFile(t *testing.T) {
	t.Setenv("GODL_DATA_DIR", t.TempDir())
	srv := newTestWebDAVServer(t)
	defer srv.Close()

	if err := connections.Add(connections.Connection{
		Name: "myconn", Type: connections.TypeWebDAV, URL: srv.URL + "/dav/",
	}); err != nil {
		t.Fatal(err)
	}

	d := newTestDaemon(t)
	output := t.TempDir()

	j, err := d.createJob(context.Background(), store.JobWebDAV, "myconn:/a.txt", output, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	d.startWebDAV(j)
	final := waitForTerminal(t, d, j.ID)

	if final.Status != store.StatusCompleted {
		t.Fatalf("job ended as %s: %s", final.Status, final.ErrorMsg)
	}
	if len(final.ResolvedPaths) != 1 || final.ResolvedPaths[0] != filepath.Join(output, "a.txt") {
		t.Errorf("ResolvedPaths = %v, want [%s]", final.ResolvedPaths, filepath.Join(output, "a.txt"))
	}
	data, err := os.ReadFile(filepath.Join(output, "a.txt"))
	if err != nil || string(data) != "hello root\n" {
		t.Errorf("a.txt = %q, %v", data, err)
	}
}

// TestStartWebDAVResumeRedownloadsMissingResolvedFile is a regression
// test for a bug where a file already recorded in job.ResolvedPaths
// was permanently skipped on resume even if it no longer existed on
// disk — the "already downloaded" check short-circuited before ever
// looking at whether os.Stat succeeded, so a job could complete
// "successfully" with data silently missing.
func TestStartWebDAVResumeRedownloadsMissingResolvedFile(t *testing.T) {
	t.Setenv("GODL_DATA_DIR", t.TempDir())
	srv := newTestWebDAVServer(t)
	defer srv.Close()

	if err := connections.Add(connections.Connection{
		Name: "myconn", Type: connections.TypeWebDAV, URL: srv.URL + "/dav/",
	}); err != nil {
		t.Fatal(err)
	}

	d := newTestDaemon(t)
	output := t.TempDir()

	j, err := d.createJob(context.Background(), store.JobWebDAV, "myconn:/", output, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	d.startWebDAV(j)
	first := waitForTerminal(t, d, j.ID)
	if first.Status != store.StatusCompleted {
		t.Fatalf("initial download failed: %s", first.ErrorMsg)
	}

	// Simulate the user (or anything else) deleting an already-
	// downloaded file between runs, then re-running the same job
	// (what "godl resume" does under the hood) without clearing
	// ResolvedPaths.
	if err := os.Remove(filepath.Join(output, "a.txt")); err != nil {
		t.Fatal(err)
	}
	j2, err := d.st.GetJob(context.Background(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	d.startWebDAV(j2)
	second := waitForTerminal(t, d, j2.ID)
	if second.Status != store.StatusCompleted {
		t.Fatalf("resumed job failed: %s", second.ErrorMsg)
	}
	if data, err := os.ReadFile(filepath.Join(output, "a.txt")); err != nil || string(data) != "hello root\n" {
		t.Errorf("a.txt was not re-downloaded after being deleted: content=%q err=%v", data, err)
	}
}

func TestStartWebDAVUnknownConnectionFails(t *testing.T) {
	t.Setenv("GODL_DATA_DIR", t.TempDir())
	d := newTestDaemon(t)

	j, err := d.createJob(context.Background(), store.JobWebDAV, "doesnotexist:/a.txt", t.TempDir(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	d.startWebDAV(j)
	final := waitForTerminal(t, d, j.ID)

	if final.Status != store.StatusFailed {
		t.Fatalf("job ended as %s, want failed", final.Status)
	}
	if !strings.Contains(final.ErrorMsg, "not found") {
		t.Errorf("ErrorMsg = %q, want it to mention the connection wasn't found", final.ErrorMsg)
	}
}
