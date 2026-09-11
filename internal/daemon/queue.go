package daemon

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"godl/internal/store"
)

// runtime is an active job's in-memory state. done closes only after the
// job's goroutine has persisted its terminal state, so pause/cancel can
// wait on it instead of racing that write.
type runtime struct {
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
	bytesDone  int64
	bytesTotal int64
	speedBps   float64
	lastBytes  int64
	lastTime   time.Time
	// See progressPersistInterval.
	lastPersist      time.Time
	lastPersistBytes int64
}

func waitForStop(rt *runtime) {
	if rt == nil {
		return
	}
	select {
	case <-rt.done:
	case <-time.After(5 * time.Second):
	}
}

// start launches j if a concurrency slot is free, and reports whether
// the slot was taken — tryStartQueued relies on that to stop, since a
// job that can't start stays queued and would be handed back forever.
//
// Every caller goes through here rather than calling a startX directly,
// so the concurrency cap is enforced in one place. The recover matters
// most for resumeInterruptedJobs: without it one bad persisted job would
// crash the daemon on every restart, forever.
func (d *Daemon) start(j *store.Job) bool {
	if !d.acquireSlot(j.ID) {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("recovered from panic starting job %s (%s): %v", j.ID, j.Type, r)
			d.clearRuntime(j.ID)
			d.finishJob(j.ID, j.BytesDone, false, fmt.Errorf("internal error starting job: %v", r))
		}
	}()
	switch j.Type {
	case store.JobURL:
		d.startURL(j)
	case store.JobTorrent:
		d.startTorrent(j)
	case store.JobSocial:
		d.startSocial(j)
	case store.JobWebDAV:
		d.startWebDAV(j)
	default:
		// Unreachable for any job actually created by createJob, but
		// without this the slot acquired above would never be
		// released, permanently shrinking capacity by one.
		d.clearRuntime(j.ID)
	}
	return true
}

// acquireSlot reserves a concurrency slot for job id, if the daemon's
// MaxConcurrent setting allows it (0 = unlimited) and id isn't already
// running. The reservation is a placeholder runtime entry — whichever
// startX function id's job reaches next immediately overwrites it via
// its own setRuntime call with the real cancel func — so the capacity
// check and the reservation happen atomically under one lock, with no
// gap for a second concurrent start() call to slip through. Released by
// clearRuntime, which also triggers tryStartQueued so a freed slot
// doesn't sit idle while other jobs wait.
func (d *Daemon) acquireSlot(id string) bool {
	max := d.cachedSettings().MaxConcurrent
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, running := d.runtimes[id]; running {
		return false
	}
	if max > 0 && len(d.runtimes) >= max {
		return false
	}
	d.runtimes[id] = &runtime{done: make(chan struct{})}
	return true
}

// markActive flips a job to StatusActive once its runtime is registered,
// and reports whether that stuck. On failure the caller must not launch
// the worker: the row would stay "queued" while a runtime existed for
// it, which is both a lie to every "godl status" reader and the state
// that used to make tryStartQueued spin — it kept being offered as
// startable, and start kept refusing it because the slot was taken.
//
// The error was previously discarded at all four call sites, so a store
// that had started failing (disk full, say) produced exactly that.
func (d *Daemon) markActive(j *store.Job) bool {
	if err := d.st.UpdateStatus(context.Background(), j.ID, store.StatusActive, ""); err != nil {
		log.Printf("job %s: recording it as active failed: %v", j.ID, err)
		d.clearRuntime(j.ID)
		d.finishJob(j.ID, j.BytesDone, false, fmt.Errorf("recording job as active: %w", err))
		return false
	}
	return true
}

func (d *Daemon) setRuntime(id string, rt *runtime) {
	d.mu.Lock()
	d.runtimes[id] = rt
	d.mu.Unlock()
}

func (d *Daemon) getRuntime(id string) *runtime {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.runtimes[id]
}

func (d *Daemon) clearRuntime(id string) {
	d.mu.Lock()
	delete(d.runtimes, id)
	d.mu.Unlock()
	d.tryStartQueued()
}

// tryStartQueued starts as many queued jobs (oldest first) as
// MaxConcurrent currently allows — called whenever a running job's slot
// frees up (clearRuntime) and whenever the setting itself changes
// (applySettings), so a raised cap or a job finishing doesn't leave
// something queued that could be running. A no-op when unlimited or
// nothing's queued.
func (d *Daemon) tryStartQueued() {
	// Re-entrancy guard. start -> startX -> (store write fails) ->
	// finishJob -> clearRuntime lands back here, so without this a
	// persistently failing store would recurse instead of looping. A
	// nested call just marks "go round again" and returns; the outermost
	// call picks that up.
	d.tryMu.Lock()
	if d.tryRunning {
		d.tryAgain = true
		d.tryMu.Unlock()
		return
	}
	d.tryRunning = true
	d.tryMu.Unlock()

	defer func() {
		d.tryMu.Lock()
		d.tryRunning = false
		again := d.tryAgain
		d.tryAgain = false
		d.tryMu.Unlock()
		if again {
			d.tryStartQueued()
		}
	}()

	ctx := context.Background()
	// Every job this pass has already picked up. A job whose "now
	// active" write fails stays queued in the store with no runtime, so
	// it would otherwise be handed straight back here and retried at
	// full speed for as long as the store keeps failing. One attempt per
	// job per pass bounds that to something finite no matter what the
	// store does.
	attempted := map[string]bool{}
	for {
		max := d.cachedSettings().MaxConcurrent
		d.mu.Lock()
		full := max > 0 && len(d.runtimes) >= max
		d.mu.Unlock()
		if full {
			return
		}
		j, err := d.nextQueuedJob(ctx, attempted)
		if err != nil || j == nil {
			return
		}
		attempted[j.ID] = true
		// A queued job that won't start is a job this pass can't make
		// progress on — carrying on would just fetch the same row again.
		// (nextQueuedJob already skips jobs holding a runtime, so this
		// is the capacity race, not the stale-status case.)
		if !d.start(j) {
			return
		}
	}
}

// nextQueuedJob returns the oldest job that is both StatusQueued and not
// already running, or nil if there is none.
//
// The runtime check is what keeps tryStartQueued terminating. A job can
// legitimately hold a runtime while its row still reads "queued" — the
// status write happens just after the runtime is registered (see
// startURL and friends), and it can also simply fail. Without this
// filter such a row is returned on every pass forever, and since start
// refuses it each time, the loop spins on the store at full speed.
func (d *Daemon) nextQueuedJob(ctx context.Context, skip map[string]bool) (*store.Job, error) {
	jobs, err := d.st.ListQueuedJobs(ctx)
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if !skip[j.ID] && d.getRuntime(j.ID) == nil {
			return j, nil
		}
	}
	return nil, nil
}

// launch registers a runtime for j, marks it active, and runs work on a
// background goroutine. Every job type starts this way; work does only
// the part that differs, and reports the outcome through finishJob.
//
// Doing the teardown here (rather than in each starter) is what keeps
// rt.done and clearRuntime paired: they must fire exactly once, on every
// exit path, or pause/cancel — which block on rt.done — hang.
func (d *Daemon) launch(j *store.Job, work func(ctx context.Context, rt *runtime)) {
	ctx, cancel := context.WithCancel(context.Background())
	rt := &runtime{cancel: cancel, done: make(chan struct{}), lastTime: time.Now(), bytesDone: j.BytesDone, bytesTotal: j.BytesTotal}
	d.setRuntime(j.ID, rt)
	if !d.markActive(j) {
		cancel()
		return
	}
	go func() {
		// LIFO: recover runs first, so the job is marked failed before
		// its runtime is cleared and rt.done closed — the same order the
		// normal path takes. start() recovers panics raised before this
		// goroutine exists; this covers the rest, which previously had
		// no protection at all and would have taken the daemon down.
		defer close(rt.done)
		defer d.clearRuntime(j.ID)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("recovered from panic running job %s (%s): %v", j.ID, j.Type, r)
				d.finishJob(j.ID, j.BytesDone, false, fmt.Errorf("internal error: %v", r))
			}
		}()
		work(ctx, rt)
	}()
}
