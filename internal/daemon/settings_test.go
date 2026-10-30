package daemon

import (
	"golang.org/x/time/rate"

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
		{"unparseable global rate limit", store.Settings{GlobalRateLimit: "not-a-rate", AutoRetryMaxAttempts: 1}},
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
	valid := store.Settings{MaxConcurrent: 2, DefaultRateLimit: "1M", GlobalRateLimit: "10M", AutoRetry: true, AutoRetryMaxAttempts: 3, NotifyOnComplete: true}
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
	j, err := d.createJob(ctx, store.JobURL, "http://example.com/f", t.TempDir(), "", 1, 0, "", store.JobOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const oneMiB = 1024 * 1024
	if j.LimitRate != oneMiB {
		t.Fatalf("createJob with no -R: LimitRate = %d, want the default %d (1M)", j.LimitRate, oneMiB)
	}

	// An explicit -R still wins over the default.
	const explicit = int64(500 * 1024)
	j2, err := d.createJob(ctx, store.JobURL, "http://example.com/f2", t.TempDir(), "", 1, explicit, "", store.JobOptions{})
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
		j, err := d.createJob(ctx, store.JobURL, srv.URL+"/f?job="+string(rune('1'+i)), filepath.Join(t.TempDir(), "f"), "", 1, 0, "", store.JobOptions{})
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

	j, err := d.createJob(ctx, store.JobURL, badURL, filepath.Join(t.TempDir(), "f"), "", 1, 0, "", store.JobOptions{})
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

func TestMinPositiveRate(t *testing.T) {
	cases := []struct {
		name string
		a, b int64
		want int64
	}{
		{"both unset", 0, 0, 0},
		{"a unset, b set", 0, 500, 500},
		{"a set, b unset", 500, 0, 500},
		{"a smaller", 200, 500, 200},
		{"b smaller", 500, 200, 200},
		{"equal", 300, 300, 300},
		{"negative treated as unset", -1, 500, 500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := minPositiveRate(c.a, c.b); got != c.want {
				t.Errorf("minPositiveRate(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
			}
		})
	}
}

// TestGlobalLimiterIsOneSharedBucket is the core regression test for
// the global bandwidth cap actually being a shared bucket rather than
// a per-job clone of the same rate: every job started under a given
// GlobalRateLimit must be handed the exact same *rate.Limiter pointer
// (rate.Limiter is safe for concurrent use, so sharing the instance is
// what makes their combined throughput share one budget — two separate
// limiters built from the same rate would let combined throughput
// reach 2x it instead).
//
// That last sentence is why this test used to be wrong. It required a
// change of rate to swap in a fresh limiter, which produced precisely
// the two-limiters case it warns about: a job started before the
// change and one started after held different instances, each
// enforcing the cap on its own. Measured, two jobs then ran at 512
// KiB/s and 3072 KiB/s against a stated 3M global cap — 3584 KiB/s in
// total. One instance, re-rated in place, is what makes the cap hold.
func TestGlobalLimiterIsOneSharedBucket(t *testing.T) {
	d := newTestDaemon(t)
	ctx := context.Background()

	if _, err := d.applySettings(ctx, store.Settings{GlobalRateLimit: "5M", AutoRetryMaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	first := d.cachedGlobalLimiter()
	if first == nil {
		t.Fatal("cachedGlobalLimiter() = nil after setting GlobalRateLimit, want a limiter")
	}
	if first != d.cachedGlobalLimiter() {
		t.Fatal("two calls returned different instances — jobs started at different times would not share one bucket")
	}

	// A change of rate keeps the instance, so a job already holding it
	// is capped by the new rate rather than the one it started under.
	if _, err := d.applySettings(ctx, store.Settings{GlobalRateLimit: "1M", AutoRetryMaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	if d.cachedGlobalLimiter() != first {
		t.Fatal("the limiter was replaced when the rate changed; running jobs keep the old cap and the two instances each enforce the limit separately")
	}
	if got, want := float64(first.Limit()), float64(1024*1024); got != want {
		t.Fatalf("limit = %v after lowering to 1M, want %v", got, want)
	}

	// Clearing it lifts the cap without discarding the instance —
	// going nil would strand every running job as unlimited forever.
	if _, err := d.applySettings(ctx, store.Settings{GlobalRateLimit: "", AutoRetryMaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	if got := d.cachedGlobalLimiter(); got != first {
		t.Fatalf("cachedGlobalLimiter() after clearing = %v, want the same instance", got)
	}
	if first.Limit() != rate.Inf {
		t.Fatalf("limit after clearing = %v, want rate.Inf", first.Limit())
	}
	if got := d.cachedGlobalRateLimitBps(); got != 0 {
		t.Fatalf("cachedGlobalRateLimitBps() after clearing = %d, want 0", got)
	}
}

// TestGlobalLimiterSurvivesASettingsChange is the bug this guards: the
// limiter used to be replaced, so a job started before a change kept
// the old one. Two consequences, both silent — the change never
// reached a running download, and because each limiter enforced the
// cap separately, two jobs on two limiters could together use twice
// the "global" limit.
func TestGlobalLimiterSurvivesASettingsChange(t *testing.T) {
	d := newTestDaemon(t)

	d.applyGlobalRateLimit(store.Settings{GlobalRateLimit: "500K"})
	first := d.cachedGlobalLimiter()
	if first == nil {
		t.Fatal("no global limiter after setting a cap")
	}
	if got, want := float64(first.Limit()), float64(500*1024); got != want {
		t.Fatalf("limit = %v, want %v", got, want)
	}

	d.applyGlobalRateLimit(store.Settings{GlobalRateLimit: "3M"})
	second := d.cachedGlobalLimiter()

	// Pointer identity, not just the value: a job holds the limiter it
	// was handed at start, so only the same instance can carry a change
	// to it.
	if first != second {
		t.Error("the limiter was replaced; running jobs hold the old one and never see the new cap")
	}
	if got, want := float64(first.Limit()), float64(3*1024*1024); got != want {
		t.Errorf("after raising the cap the limiter a running job holds reads %v, want %v", got, want)
	}
	if got := d.cachedGlobalRateLimitBps(); got != 3*1024*1024 {
		t.Errorf("cachedGlobalRateLimitBps() = %d, want %d", got, 3*1024*1024)
	}
}

// Unlimited has to be reachable and reversible on the same instance,
// or turning the cap off and on again would strand running jobs.
func TestGlobalLimiterHandlesUnlimitedBothWays(t *testing.T) {
	d := newTestDaemon(t)

	d.applyGlobalRateLimit(store.Settings{GlobalRateLimit: ""})
	l := d.cachedGlobalLimiter()
	if l == nil {
		t.Fatal("global limiter is nil when unlimited; it could never later be given a cap")
	}
	if l.Limit() != rate.Inf {
		t.Errorf("unlimited reads %v, want rate.Inf", l.Limit())
	}

	d.applyGlobalRateLimit(store.Settings{GlobalRateLimit: "1M"})
	if d.cachedGlobalLimiter() != l {
		t.Error("going from unlimited to a cap replaced the limiter")
	}
	if got, want := float64(l.Limit()), float64(1024*1024); got != want {
		t.Errorf("limit = %v, want %v", got, want)
	}

	d.applyGlobalRateLimit(store.Settings{GlobalRateLimit: ""})
	if d.cachedGlobalLimiter() != l || l.Limit() != rate.Inf {
		t.Error("going back to unlimited replaced the limiter or did not lift the cap")
	}
}
