package daemon

import (
	"context"
	"testing"
	"time"

	"godl/internal/store"
)

// TestTryStartQueuedTerminatesOnAStuckJob is the regression test for a
// daemon hang: tryStartQueued loops "find a queued job, start it" until
// nothing is startable, but start silently declines a job that already
// holds a runtime. A job can be in exactly that state — queued in the
// store, runtime registered — whenever the "now active" write hasn't
// landed, and the error from that write used to be discarded entirely.
// The loop then got the same row back on every pass and spun at full
// speed against sqlite forever.
//
// Here that state is set up directly, since it's the shape that matters,
// not how the job got into it.
func TestTryStartQueuedTerminatesOnAStuckJob(t *testing.T) {
	d := newTestDaemon(t)
	ctx := context.Background()

	j := &store.Job{
		ID:     "stuck-job",
		Type:   store.JobURL,
		Source: "https://example.com/f.bin",
		Output: "/tmp/f.bin",
		Status: store.StatusQueued,
	}
	if err := d.st.CreateJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	// Queued in the store, but already holding a runtime.
	d.setRuntime(j.ID, &runtime{done: make(chan struct{})})

	done := make(chan struct{})
	go func() {
		d.tryStartQueued()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("tryStartQueued never returned — it is spinning on a queued job it can't start")
	}
}

// TestTryStartQueuedAttemptsEachJobOnce guards the other half: a job
// that stays queued because its status write failed must not be retried
// in a tight loop either. Nothing here can start (the concurrency cap is
// already full), so the pass must simply end rather than re-reading the
// same rows forever.
func TestTryStartQueuedAttemptsEachJobOnce(t *testing.T) {
	d := newTestDaemon(t)
	ctx := context.Background()

	if _, err := d.applySettings(ctx, store.Settings{MaxConcurrent: 1, AutoRetryMaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	// Occupy the only slot.
	d.setRuntime("already-running", &runtime{done: make(chan struct{})})

	for _, id := range []string{"q1", "q2", "q3"} {
		if err := d.st.CreateJob(ctx, &store.Job{
			ID: id, Type: store.JobURL, Source: "https://example.com/" + id,
			Output: "/tmp/" + id, Status: store.StatusQueued,
		}); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan struct{})
	go func() {
		d.tryStartQueued()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("tryStartQueued never returned with the concurrency cap full")
	}

	// Nothing should have been started past the cap.
	d.mu.Lock()
	n := len(d.runtimes)
	d.mu.Unlock()
	if n != 1 {
		t.Errorf("runtimes = %d, want 1 (the cap) — queued jobs were started past MaxConcurrent", n)
	}
}

// TestListQueuedJobsReturnsOnlyQueued covers the narrowed query that
// replaced "list every job, then filter": the daemon runs it on every
// job completion, so it must not depend on scanning the whole table.
func TestListQueuedJobsReturnsOnlyQueued(t *testing.T) {
	d := newTestDaemon(t)
	ctx := context.Background()

	seed := []struct {
		id     string
		status store.JobStatus
	}{
		{"a", store.StatusQueued},
		{"b", store.StatusActive},
		{"c", store.StatusCompleted},
		{"d", store.StatusQueued},
		{"e", store.StatusFailed},
	}
	for _, s := range seed {
		if err := d.st.CreateJob(ctx, &store.Job{
			ID: s.id, Type: store.JobURL, Source: "https://example.com/" + s.id,
			Output: "/tmp/" + s.id, Status: s.status,
		}); err != nil {
			t.Fatal(err)
		}
	}

	jobs, err := d.st.ListQueuedJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("ListQueuedJobs returned %d jobs, want 2", len(jobs))
	}
	for _, j := range jobs {
		if j.Status != store.StatusQueued {
			t.Errorf("job %s has status %q, want only queued jobs", j.ID, j.Status)
		}
	}
}

// TestReportProgressThrottlesStoreWrites covers the write-amplification
// fix. Progress callbacks arrive several times a second per stream (and
// a WebDAV folder job runs several streams at once), and each one used
// to be its own sqlite transaction. The in-memory runtime must still
// track every tick — the TUI reads it for speed and ETA — while the
// store sees only periodic checkpoints.
func TestReportProgressThrottlesStoreWrites(t *testing.T) {
	origInterval, origBytes := progressPersistInterval, progressPersistBytes
	progressPersistInterval = time.Hour // only the byte trigger should fire
	progressPersistBytes = 1 << 20
	t.Cleanup(func() {
		progressPersistInterval, progressPersistBytes = origInterval, origBytes
	})

	d := newTestDaemon(t)
	ctx := context.Background()
	j := &store.Job{
		ID: "throttled", Type: store.JobURL, Source: "https://example.com/f.bin",
		Output: "/tmp/f.bin", Status: store.StatusActive,
	}
	if err := d.st.CreateJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	rt := &runtime{done: make(chan struct{}), lastTime: time.Now()}
	d.setRuntime(j.ID, rt)

	// 100 ticks of 64KiB. Only the ones crossing a 1MiB boundary should
	// reach the store; without the throttle all 100 would.
	const ticks = 100
	const per = int64(64 << 10)
	for i := 1; i <= ticks; i++ {
		d.reportProgress(j.ID, per*int64(i), per*ticks, nil)
	}

	// The runtime saw every tick, whatever the store saw.
	rt.mu.Lock()
	gotRuntime := rt.bytesDone
	rt.mu.Unlock()
	if want := per * ticks; gotRuntime != want {
		t.Errorf("runtime bytesDone = %d, want %d — the in-memory view must track every tick", gotRuntime, want)
	}

	stored, err := d.st.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 100 ticks x 64KiB = 6.25MiB, so ~6 crossings of the 1MiB trigger
	// plus the first-call write. The exact count doesn't matter; that it
	// is far below one-per-tick does.
	if stored.BytesDone == 0 {
		t.Error("nothing was ever persisted — progress must still be checkpointed, just less often")
	}
	if stored.BytesDone > per*ticks {
		t.Errorf("stored bytes_done = %d exceeds what was reported (%d)", stored.BytesDone, per*ticks)
	}
	// The durability bound: never more than one trigger's worth behind.
	if behind := per*ticks - stored.BytesDone; behind > progressPersistBytes+per {
		t.Errorf("stored progress is %d bytes behind, want at most ~%d — the throttle is looser than documented", behind, progressPersistBytes)
	}
}

// TestReportProgressWritesResumeOffsetInTheSameRow confirms the folded
// write: a single-stream url job checkpoints its resume offset alongside
// its byte counts rather than issuing a second UPDATE for the row that
// was just written.
func TestReportProgressWritesResumeOffsetInTheSameRow(t *testing.T) {
	d := newTestDaemon(t)
	ctx := context.Background()
	j := &store.Job{
		ID: "single", Type: store.JobURL, Source: "https://example.com/f.bin",
		Output: "/tmp/f.bin", Status: store.StatusActive,
	}
	if err := d.st.CreateJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	d.setRuntime(j.ID, &runtime{done: make(chan struct{}), lastTime: time.Now()})

	done := int64(4096)
	d.reportProgress(j.ID, done, 8192, &done)

	stored, err := d.st.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BytesDone != done {
		t.Errorf("bytes_done = %d, want %d", stored.BytesDone, done)
	}
	if stored.ResumeOffset != done {
		t.Errorf("resume_offset = %d, want %d — it should ride along in the same write", stored.ResumeOffset, done)
	}

	// A chunked job passes nil and must leave resume_offset alone.
	d.reportProgress(j.ID, done*2, 8192, nil)
	stored, err = d.st.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ResumeOffset != done {
		t.Errorf("resume_offset = %d after a nil-offset report, want it unchanged at %d", stored.ResumeOffset, done)
	}
}
