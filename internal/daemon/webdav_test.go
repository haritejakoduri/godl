package daemon

import (
	"bytes"
	"context"
	"fmt"
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
		st:          st,
		dataDir:     dir,
		runtimes:    map[string]*runtime{},
		logSubs:     map[chan logMsg]struct{}{},
		settings:    store.DefaultSettings(),
		retryTimers: map[string]*time.Timer{},
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

// webdavFixture is the setup every startWebDAV test needs: an isolated
// data dir, a throwaway WebDAV server registered as a saved connection,
// a daemon, and an empty output directory.
type webdavFixture struct {
	d      *Daemon
	output string
}

// newWebDAVFixture registers newTestWebDAVServer's two-file tree as the
// connection "myconn".
func newWebDAVFixture(t *testing.T) webdavFixture {
	t.Helper()
	srv := newTestWebDAVServer(t)
	return newWebDAVFixtureFor(t, "myconn", srv, srv.URL+"/dav/")
}

// newWebDAVFixtureFor registers root on srv under the saved connection
// name.
func newWebDAVFixtureFor(t *testing.T, name string, srv *httptest.Server, root string) webdavFixture {
	t.Helper()
	t.Setenv("GODL_DATA_DIR", t.TempDir())
	t.Cleanup(srv.Close)
	if err := connections.Add(connections.Connection{
		Name: name, Type: connections.TypeWebDAV, URL: root,
	}); err != nil {
		t.Fatal(err)
	}
	return webdavFixture{d: newTestDaemon(t), output: t.TempDir()}
}

// newEmptyWebDAVFixture has no saved connections at all — for the
// "connection doesn't exist" path.
func newEmptyWebDAVFixture(t *testing.T) webdavFixture {
	t.Helper()
	t.Setenv("GODL_DATA_DIR", t.TempDir())
	return webdavFixture{d: newTestDaemon(t), output: t.TempDir()}
}

// run creates a WebDAV job for source and waits for it to settle.
func (f webdavFixture) run(t *testing.T, source string) *store.Job {
	t.Helper()
	j, err := f.d.createJob(context.Background(), store.JobWebDAV, source, f.output, "", 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	f.d.startWebDAV(j)
	return waitForTerminal(t, f.d, j.ID)
}

// rerun restarts an existing job from its stored state — what "godl
// resume" does under the hood.
func (f webdavFixture) rerun(t *testing.T, id string) *store.Job {
	t.Helper()
	j, err := f.d.st.GetJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	f.d.startWebDAV(j)
	return waitForTerminal(t, f.d, id)
}

// path joins p onto the fixture's output directory.
func (f webdavFixture) path(p ...string) string {
	return filepath.Join(append([]string{f.output}, p...)...)
}

func TestStartWebDAVDownloadsFolderRecursively(t *testing.T) {
	f := newWebDAVFixture(t)
	final := f.run(t, "myconn:/")

	if final.Status != store.StatusCompleted {
		t.Fatalf("job ended as %s: %s", final.Status, final.ErrorMsg)
	}
	if final.BytesDone != 21 {
		t.Errorf("BytesDone = %d, want 21 (11 + 10)", final.BytesDone)
	}
	if len(final.ResolvedPaths) != 2 {
		t.Fatalf("ResolvedPaths = %v, want 2 entries", final.ResolvedPaths)
	}

	a, err := os.ReadFile(f.path("a.txt"))
	if err != nil || string(a) != "hello root\n" {
		t.Errorf("a.txt = %q, %v", a, err)
	}
	b, err := os.ReadFile(f.path("sub", "b.txt"))
	if err != nil || string(b) != "hello sub\n" {
		t.Errorf("sub/b.txt = %q, %v", b, err)
	}
}

func TestStartWebDAVDownloadsSingleFile(t *testing.T) {
	f := newWebDAVFixture(t)
	final := f.run(t, "myconn:/a.txt")

	if final.Status != store.StatusCompleted {
		t.Fatalf("job ended as %s: %s", final.Status, final.ErrorMsg)
	}
	if len(final.ResolvedPaths) != 1 || final.ResolvedPaths[0] != f.path("a.txt") {
		t.Errorf("ResolvedPaths = %v, want [%s]", final.ResolvedPaths, f.path("a.txt"))
	}
	data, err := os.ReadFile(f.path("a.txt"))
	if err != nil || string(data) != "hello root\n" {
		t.Errorf("a.txt = %q, %v", data, err)
	}
}

// TestStartWebDAVNamedFolderKeepsItsOwnNameInOutput is a regression
// test: downloading a folder by name (not the connection's root) used
// to drop that folder's own name from the destination and dump its
// contents straight into -o, only preserving structure *underneath* it
// — so "myconn:/sub" landed at output/b.txt instead of
// output/sub/b.txt. Besides just being wrong (it doesn't read as "the
// sub folder, downloaded"), two differently-named remote folders that
// happen to share a child folder name could silently overwrite each
// other's files. The selected folder's own name must be preserved as a
// top-level directory under output.
func TestStartWebDAVNamedFolderKeepsItsOwnNameInOutput(t *testing.T) {
	f := newWebDAVFixture(t)
	// Trailing slash matches how the TUI always sends a folder's path
	// (directory hrefs from a real WebDAV server's PROPFIND response are
	// slash-terminated) — webdavLocalPath must handle it either way, but
	// this keeps the test aligned with the actual call shape in practice.
	final := f.run(t, "myconn:/sub/")

	if final.Status != store.StatusCompleted {
		t.Fatalf("job ended as %s: %s", final.Status, final.ErrorMsg)
	}
	want := f.path("sub", "b.txt")
	if len(final.ResolvedPaths) != 1 || final.ResolvedPaths[0] != want {
		t.Errorf("ResolvedPaths = %v, want [%s]", final.ResolvedPaths, want)
	}
	data, err := os.ReadFile(want)
	if err != nil || string(data) != "hello sub\n" {
		t.Errorf("%s = %q, %v", want, data, err)
	}
	if _, err := os.Stat(f.path("b.txt")); err == nil {
		t.Error("b.txt landed directly under output, without its \"sub\" folder name preserved")
	}
}

// TestStartWebDAVResumeRedownloadsMissingResolvedFile is a regression
// test for a bug where a file already recorded in job.ResolvedPaths
// was permanently skipped on resume even if it no longer existed on
// disk — the "already downloaded" check short-circuited before ever
// looking at whether os.Stat succeeded, so a job could complete
// "successfully" with data silently missing.
func TestStartWebDAVResumeRedownloadsMissingResolvedFile(t *testing.T) {
	f := newWebDAVFixture(t)
	first := f.run(t, "myconn:/")
	if first.Status != store.StatusCompleted {
		t.Fatalf("initial download failed: %s", first.ErrorMsg)
	}

	// Simulate the user (or anything else) deleting an already-
	// downloaded file between runs, then re-running the same job without
	// clearing ResolvedPaths.
	if err := os.Remove(f.path("a.txt")); err != nil {
		t.Fatal(err)
	}
	second := f.rerun(t, first.ID)
	if second.Status != store.StatusCompleted {
		t.Fatalf("resumed job failed: %s", second.ErrorMsg)
	}
	if data, err := os.ReadFile(f.path("a.txt")); err != nil || string(data) != "hello root\n" {
		t.Errorf("a.txt was not re-downloaded after being deleted: content=%q err=%v", data, err)
	}
}

// newWideTestWebDAVServer serves a single folder /wide/ containing n
// flat files (/wide/file0.bin .. file<n-1>.bin), each fileSize bytes of
// a byte-index-derived, verifiable pattern — used to stress the
// concurrent-download path (more files than
// webdavDownloadConcurrency) rather than internal/daemon's usual
// 2-file fixture, which never exceeds the concurrency limit.
func newWideTestWebDAVServer(t *testing.T, n int, fileSize int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/wide/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			http.NotFound(w, r)
			return
		}
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><D:multistatus xmlns:D="DAV:">`)
		b.WriteString(`<D:response><D:href>/wide/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`)
		if r.Header.Get("Depth") != "0" {
			for i := 0; i < n; i++ {
				fmt.Fprintf(&b, `<D:response><D:href>/wide/file%d.bin</D:href><D:propstat><D:prop><D:resourcetype/><D:getcontentlength>%d</D:getcontentlength></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`, i, fileSize)
			}
		}
		b.WriteString(`</D:multistatus>`)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(b.String()))
	})
	for i := 0; i < n; i++ {
		i := i
		mux.HandleFunc(fmt.Sprintf("/wide/file%d.bin", i), func(w http.ResponseWriter, r *http.Request) {
			data := bytes.Repeat([]byte{byte(i)}, fileSize)
			http.ServeContent(w, r, fmt.Sprintf("file%d.bin", i), time.Time{}, bytes.NewReader(data))
		})
	}
	return httptest.NewServer(mux)
}

// TestStartWebDAVConcurrentDownloadsDontLoseFiles is a regression test
// for the AppendResolvedPath race the concurrent-download path exposed:
// several goroutines calling it at once could each read the job's
// pre-append ResolvedPaths, then all write their own single-entry
// version back, silently discarding every entry but the last writer's.
// More files than webdavDownloadConcurrency forces the semaphore's
// backpressure path too (later downloads queued behind the first
// batch), not just "a few files that all start at once".
func TestStartWebDAVConcurrentDownloadsDontLoseFiles(t *testing.T) {
	const n = 12
	const fileSize = 4096
	srv := newWideTestWebDAVServer(t, n, fileSize)
	f := newWebDAVFixtureFor(t, "wideconn", srv, srv.URL+"/wide/")
	final := f.run(t, "wideconn:/")

	if final.Status != store.StatusCompleted {
		t.Fatalf("job ended as %s: %s", final.Status, final.ErrorMsg)
	}
	if len(final.ResolvedPaths) != n {
		t.Fatalf("ResolvedPaths has %d entries, want %d (this is the lost-update race if it regresses): %v",
			len(final.ResolvedPaths), n, final.ResolvedPaths)
	}
	if final.BytesDone != int64(n*fileSize) {
		t.Errorf("BytesDone = %d, want %d", final.BytesDone, n*fileSize)
	}
	for i := 0; i < n; i++ {
		p := f.path(fmt.Sprintf("file%d.bin", i))
		data, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("file%d.bin: %v", i, err)
			continue
		}
		want := bytes.Repeat([]byte{byte(i)}, fileSize)
		if !bytes.Equal(data, want) {
			t.Errorf("file%d.bin content mismatch (wrong file's bytes ended up here?)", i)
		}
	}
}

func TestStartWebDAVUnknownConnectionFails(t *testing.T) {
	f := newEmptyWebDAVFixture(t)
	final := f.run(t, "doesnotexist:/a.txt")

	if final.Status != store.StatusFailed {
		t.Fatalf("job ended as %s, want failed", final.Status)
	}
	if !strings.Contains(final.ErrorMsg, "not found") {
		t.Errorf("ErrorMsg = %q, want it to mention the connection wasn't found", final.ErrorMsg)
	}
}
