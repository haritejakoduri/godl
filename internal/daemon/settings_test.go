package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"godl/internal/store"
)

func TestApplySettingsValidates(t *testing.T) {
	d := newTestDaemon(t)
	ctx := context.Background()

	cases := []struct {
		name string
		s    store.Settings
	}{
		{"negative max concurrent", store.Settings{MaxConcurrent: -1, AutoRetryMaxAttempts: 1}},
		{"unparseable default rate limit", store.Settings{DefaultRateLimit: "not-a-rate", AutoRetryMaxAttempts: 1}},
		{"zero auto-retry max attempts", store.Settings{AutoRetryMaxAttempts: 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := d.applySettings(ctx, c.s); err == nil {
				t.Fatalf("applySettings(%+v) succeeded, want a validation error", c.s)
			}
		})
	}

	// A valid save must still work, round-tripping through the store,
	// not just the in-memory cache.
	valid := store.Settings{MaxConcurrent: 2, DefaultRateLimit: "1M", AutoRetry: true, AutoRetryMaxAttempts: 3, NotifyOnComplete: true}
	applied, err := d.applySettings(ctx, valid)
	if err != nil {
		t.Fatalf("applySettings(%+v): %v", valid, err)
	}
	if applied != valid {
		t.Fatalf("applySettings returned %+v, want %+v", applied, valid)
	}
	if d.cachedSettings() != valid {
		t.Fatalf("cachedSettings() = %+v, want %+v", d.cachedSettings(), valid)
	}
	reread, err := d.st.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reread != valid {
		t.Fatalf("GetSettings after applySettings = %+v, want %+v (not persisted)", reread, valid)
	}
}

func TestDefaultRateLimitAppliedWhenJobOmitsOwn(t *testing.T) {
	d := newTestDaemon(t)
	ctx := context.Background()

	if _, err := d.applySettings(ctx, store.Settings{DefaultRateLimit: "1M", AutoRetryMaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}

	// limitRate=0 (no -R passed) picks up the 1M default.
	j, err := d.createJob(ctx, store.JobURL, "http://example.com/f", t.TempDir(), "", 1, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	const oneMiB = 1024 * 1024
	if j.LimitRate != oneMiB {
		t.Fatalf("createJob with no -R: LimitRate = %d, want the default %d (1M)", j.LimitRate, oneMiB)
	}

	// An explicit -R still wins over the default.
	const explicit = int64(500 * 1024)
	j2, err := d.createJob(ctx, store.JobURL, "http://example.com/f2", t.TempDir(), "", 1, explicit, "")
	if err != nil {
		t.Fatal(err)
	}
	if j2.LimitRate != explicit {
		t.Fatalf("createJob with explicit -R: LimitRate = %d, want %d (default must not override it)", j2.LimitRate, explicit)
	}
}

// gatedURLServer serves GET requests for /f?job=<id> that block until
// release(id) is called, so a test can deterministically observe "job
// N is still actively downloading" instead of racing a real transfer
// against assertions. HEAD requests (downloader.Run's initial probe)
// are answered immediately and never gated.
type gatedURLServer struct {
	*httptest.Server
	mu    sync.Mutex
	gates map[string]chan struct{}
}

func newGatedURLServer(t *testing.T) *gatedURLServer {
	t.Helper()
	g := &gatedURLServer{gates: map[string]chan struct{}{}}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "5")
			return
		}
		<-g.gateFor(r.URL.Query().Get("job"))
		w.Write([]byte("hello"))
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *gatedURLServer) gateFor(id string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	ch, ok := g.gates[id]
	if !ok {
		ch = make(chan struct{})
		g.gates[id] = ch
	}
	return ch
}

// release lets job id's blocked GET through. Safe to call before the
// request has even arrived — gateFor creates the channel on first
// touch either way, so whichever of release/the handler gets there
// first just creates it for the other to find.
func (g *gatedURLServer) release(id string) { close(g.gateFor(id)) }

// TestMaxConcurrentQueuesExtraJobs is the core regression test for the
// concurrency cap: with MaxConcurrent=1, a second and third job created
// while the first is still running must stay StatusQueued (not start
// alongside it), and each must start in turn only once the one ahead of
// it actually finishes — proving both the gate (acquireSlot) and the
// hand-off (clearRuntime -> tryStartQueued) work together, not just one
// half of the mechanism.
func TestMaxConcurrentQueuesExtraJobs(t *testing.T) {
	d := newTestDaemon(t)
	ctx := context.Background()
	srv := newGatedURLServer(t)

	if _, err := d.applySettings(ctx, store.Settings{MaxConcurrent: 1, AutoRetryMaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}

	ids := make([]string, 3)
	for i := range ids {
		j, err := d.createJob(ctx, store.JobURL, srv.URL+"/f?job="+string(rune('1'+i)), filepath.Join(t.TempDir(), "f"), "", 1, 0, "")
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = j.ID
		d.start(j)
	}

	waitForStatus := func(id string, want store.JobStatus) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			j, err := d.st.GetJob(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if j.Status == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		j, _ := d.st.GetJob(ctx, id)
		t.Fatalf("job %s never reached %s (last seen: %s)", id, want, j.Status)
	}

	// Only the first job should be running; the other two stay queued
	// behind it even though they were started (via d.start) too.
	waitForStatus(ids[0], store.StatusActive)
	for _, id := range ids[1:] {
		j, err := d.st.GetJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status != store.StatusQueued {
			t.Fatalf("job %s = %s while the cap is full, want queued", id, j.Status)
		}
	}

	srv.release("1")
	waitForStatus(ids[0], store.StatusCompleted)
	waitForStatus(ids[1], store.StatusActive) // freed slot picked up the next-oldest queued job

	srv.release("2")
	waitForStatus(ids[1], store.StatusCompleted)
	waitForStatus(ids[2], store.StatusActive)

	srv.release("3")
	waitForStatus(ids[2], store.StatusCompleted)
}

// TestAutoRetryReschedulesFailedJobUpToMaxAttempts confirms the
// scheduleAutoRetry/finishJob loop actually re-queues a failed job and
// stops once RetryCount reaches AutoRetryMaxAttempts, rather than
// retrying forever or not at all.
func TestAutoRetryReschedulesFailedJobUpToMaxAttempts(t *testing.T) {
	origBackoff := autoRetryBackoff
	autoRetryBackoff = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { autoRetryBackoff = origBackoff })

	d := newTestDaemon(t)
	ctx := context.Background()

	// A server that's immediately closed leaves nothing listening at
	// its address, so every connection attempt fails fast and
	// deterministically — no network timeout to wait out.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	badURL := srv.URL + "/f"
	srv.Close()

	if _, err := d.applySettings(ctx, store.Settings{AutoRetry: true, AutoRetryMaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}

	j, err := d.createJob(ctx, store.JobURL, badURL, filepath.Join(t.TempDir(), "f"), "", 1, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	d.start(j)

	deadline := time.Now().Add(5 * time.Second)
	var final *store.Job
	for time.Now().Before(deadline) {
		cur, err := d.st.GetJob(ctx, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if cur.Status == store.StatusFailed && cur.RetryCount >= 2 {
			final = cur
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if final == nil {
		last, _ := d.st.GetJob(ctx, j.ID)
		t.Fatalf("job never reached RetryCount>=2 and StatusFailed within the deadline (last seen: status=%s retryCount=%d)", last.Status, last.RetryCount)
	}
	if final.RetryCount != 2 {
		t.Fatalf("RetryCount = %d, want 2 (AutoRetryMaxAttempts)", final.RetryCount)
	}

	// Confirm it actually stops: no further auto-retry should fire once
	// the cap is hit.
	time.Sleep(50 * time.Millisecond)
	recheck, err := d.st.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recheck.RetryCount != 2 || recheck.Status != store.StatusFailed {
		t.Fatalf("job kept changing after hitting the retry cap: status=%s retryCount=%d", recheck.Status, recheck.RetryCount)
	}
}
