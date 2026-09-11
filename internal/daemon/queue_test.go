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
