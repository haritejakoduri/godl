package daemon

import (
	"context"
	"testing"

	"godl/internal/connections"
	"godl/internal/store"
)

// TestStartRecoversFromPanicWithoutCrashingDaemon is a regression test
// for a bug where a panic during job start (originally: an
// anacrolix/torrent panic on a magnet link with a zero info hash — see
// torrentmgr.specFromSource) crashed the entire daemon process. Because
// bad jobs persist in the store and resumeInterruptedJobs re-starts
// them synchronously on every daemon boot, an unrecovered panic here
// meant a single malformed job put the daemon into a permanent
// crash-loop, taking down url/social/webdav jobs too, not just the
// torrent job that triggered it.
//
// newTestDaemon builds a Daemon with a nil torrent manager (tm), so
// starting any torrent job panics with a nil pointer dereference inside
// Manager.Add — a stand-in for "some panic escapes a job starter",
// independent of the specific zero-infohash case that motivated this
// fix (which is now rejected with a clean error before it ever reaches
// that code, see torrentmgr_test.go).
func TestStartRecoversFromPanicWithoutCrashingDaemon(t *testing.T) {
	d := newTestDaemon(t)
	ctx := context.Background()

	j, err := d.createJob(ctx, store.JobTorrent, "magnet:?xt=urn:btih:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2&dn=faketest", t.TempDir(), "", 0)
	if err != nil {
		t.Fatal(err)
	}

	d.start(j)
	final := waitForTerminal(t, d, j.ID)

	if final.Status != store.StatusFailed {
		t.Fatalf("job ended as %s, want %s", final.Status, store.StatusFailed)
	}
	if final.ErrorMsg == "" {
		t.Error("ErrorMsg is empty, want a message describing the recovered panic")
	}
	if d.getRuntime(j.ID) != nil {
		t.Error("runtime for the panicked job was not cleared")
	}

	// Prove the daemon (and this test process) is still alive and fully
	// functional after the panic — the whole point of the recover.
	srv := newTestWebDAVServer(t)
	defer srv.Close()
	t.Setenv("GODL_DATA_DIR", t.TempDir())
	if err := connections.Add(connections.Connection{
		Name: "survives", Type: connections.TypeWebDAV, URL: srv.URL + "/dav/",
	}); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	j2, err := d.createJob(ctx, store.JobWebDAV, "survives:/a.txt", output, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	d.start(j2)
	final2 := waitForTerminal(t, d, j2.ID)
	if final2.Status != store.StatusCompleted {
		t.Fatalf("post-panic job ended as %s: %s", final2.Status, final2.ErrorMsg)
	}
}

// TestResumeInterruptedJobsSurvivesAPanickingJob is a regression test
// for the crash-loop specifically: resumeInterruptedJobs (called
// synchronously from Serve() on every daemon startup) must not let one
// bad persisted job's panic stop it from resuming the rest.
func TestResumeInterruptedJobsSurvivesAPanickingJob(t *testing.T) {
	d := newTestDaemon(t)
	ctx := context.Background()

	bad, err := d.createJob(ctx, store.JobTorrent, "magnet:?xt=urn:btih:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2&dn=faketest", t.TempDir(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	bad.Status = store.StatusQueued
	if err := d.st.UpdateJob(ctx, bad); err != nil {
		t.Fatal(err)
	}

	srv := newTestWebDAVServer(t)
	defer srv.Close()
	t.Setenv("GODL_DATA_DIR", t.TempDir())
	if err := connections.Add(connections.Connection{
		Name: "good", Type: connections.TypeWebDAV, URL: srv.URL + "/dav/",
	}); err != nil {
		t.Fatal(err)
	}
	good, err := d.createJob(ctx, store.JobWebDAV, "good:/a.txt", t.TempDir(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	good.Status = store.StatusQueued
	if err := d.st.UpdateJob(ctx, good); err != nil {
		t.Fatal(err)
	}

	// Exercises the exact code path a daemon restart takes on boot.
	d.resumeInterruptedJobs()

	badFinal := waitForTerminal(t, d, bad.ID)
	if badFinal.Status != store.StatusFailed {
		t.Errorf("bad job ended as %s, want %s", badFinal.Status, store.StatusFailed)
	}
	goodFinal := waitForTerminal(t, d, good.ID)
	if goodFinal.Status != store.StatusCompleted {
		t.Errorf("good job ended as %s: %s", goodFinal.Status, goodFinal.ErrorMsg)
	}
}
