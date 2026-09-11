package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"godl/internal/notify"
	"godl/internal/ratelimit"
	"godl/internal/store"
)

// How stale the stored byte counts may get while a job runs; whichever
// trips first forces a write. Progress callbacks fire ~5x a second per
// stream and a folder job runs several at once, so persisting each one
// meant tens of sqlite transactions a second through a single
// connection. What's stored is a checkpoint — resume re-derives from the
// file on disk — so an ungraceful death costs at most this much, and
// finishJob/pause write the final counts regardless.
//
// Vars, not consts, so tests can shrink them.
var (
	progressPersistInterval = 2 * time.Second
	progressPersistBytes    = int64(8 << 20)
)

func (d *Daemon) createJob(ctx context.Context, typ store.JobType, source, output, format string, concurrency int, limitRate int64, sha256 string) (*store.Job, error) {
	if source == "" {
		return nil, fmt.Errorf("source is required")
	}
	if concurrency < 1 {
		concurrency = 1
	}
	// A job that didn't ask for its own --limit-rate falls back to the
	// settings-tab default, if one's set — parse errors here would mean
	// a bad value slipped past applySettings' own validation, so
	// treating that as "no default" rather than failing job creation
	// over it is the safer failure mode.
	if limitRate == 0 {
		if def := d.cachedSettings().DefaultRateLimit; def != "" {
			if parsed, err := ratelimit.ParseRate(def); err == nil {
				limitRate = parsed
			}
		}
	}
	id, err := d.st.NewID(ctx)
	if err != nil {
		return nil, err
	}
	j := &store.Job{
		ID:          id,
		Type:        typ,
		Source:      source,
		Output:      output,
		Format:      format,
		Concurrency: concurrency,
		LimitRate:   limitRate,
		Sha256:      sha256,
		Status:      store.StatusQueued,
	}
	if err := d.st.CreateJob(ctx, j); err != nil {
		return nil, err
	}
	return j, nil
}

// finishJob persists the terminal (completed/failed) state of a job once
// its goroutine returns on its own — natural completion or a real error.
// It is NOT used for the pause/cancel path; those callers wait for the
// goroutine to exit (see waitForStop) and then write the terminal state
// themselves, so there's exactly one writer for that transition.
func (d *Daemon) finishJob(id string, bytesDone int64, completed bool, err error) {
	ctx := context.Background()
	job, gerr := d.st.GetJob(ctx, id)
	if gerr != nil {
		return
	}
	job.BytesDone = bytesDone
	if job.Type == store.JobURL {
		job.ResumeOffset = bytesDone
	}

	// > 0 once set means "schedule an auto-retry after this persists".
	var retryDelay time.Duration
	switch {
	case completed:
		job.Status = store.StatusCompleted
		job.ErrorMsg = ""
		job.RetryCount = 0
	case errors.Is(err, context.Canceled):
		job.Status = store.StatusPaused
	case err != nil:
		job.Status = store.StatusFailed
		job.ErrorMsg = err.Error()
		if s := d.cachedSettings(); s.AutoRetry && job.RetryCount < s.AutoRetryMaxAttempts {
			job.RetryCount++
			retryDelay = autoRetryBackoff(job.RetryCount)
			job.ErrorMsg = fmt.Sprintf("%s (auto-retry %d/%d in %s)", err.Error(), job.RetryCount, s.AutoRetryMaxAttempts, retryDelay.Round(time.Second))
		}
	default:
		job.Status = store.StatusFailed
		job.ErrorMsg = "job ended without completing"
	}
	d.st.UpdateJob(ctx, job)

	if completed && d.cachedSettings().NotifyOnComplete {
		go notify.Send("godl: download complete", notifyBody(job))
	}
	if retryDelay > 0 {
		d.scheduleAutoRetry(id, retryDelay)
	}
}

// notifyBody is the one-line summary a completion notification shows:
// the downloaded file's name when known, falling back to the source
// (e.g. a torrent job before its content path resolves, though by the
// time a job reaches "completed" that's already set for every type).
func notifyBody(job *store.Job) string {
	if job.Output != "" {
		return filepath.Base(job.Output)
	}
	return job.Source
}

// autoRetryBackoff returns how long to wait before an auto-retry's
// attempt'th try (1-indexed): 5s, 15s, 45s, ... tripling each time, up
// to a 5-minute ceiling so a persistently-broken source doesn't retry
// so slowly it might as well have stopped, nor so fast it hammers a
// server that's genuinely down. A package-level var (not a const/plain
// func call) purely so tests can swap in a near-zero delay instead of
// actually sleeping.
var autoRetryBackoff = func(attempt int) time.Duration {
	delay := 5 * time.Second
	for i := 1; i < attempt; i++ {
		delay *= 3
		if delay >= 5*time.Minute {
			return 5 * time.Minute
		}
	}
	return delay
}

// scheduleAutoRetry re-queues job id after delay, unless something else
// (a manual retry/remove/pause-then-resume) has already moved it out of
// StatusFailed by the time the timer fires — re-checked rather than
// assumed, since delay can be minutes and a lot can happen to a job in
// that time. Tracked in retryTimers so Close can stop any still pending
// at shutdown, rather than one firing after the store it would write to
// is already closed.
func (d *Daemon) scheduleAutoRetry(id string, delay time.Duration) {
	t := time.AfterFunc(delay, func() {
		d.retryMu.Lock()
		delete(d.retryTimers, id)
		d.retryMu.Unlock()

		ctx := context.Background()
		job, err := d.st.GetJob(ctx, id)
		if err != nil || job.Status != store.StatusFailed {
			return
		}
		resetForRetry(job)
		// RetryCount was already incremented in finishJob when this
		// retry was scheduled — resetForRetry doesn't touch it, unlike
		// a manual retry's explicit reset to 0.
		if err := d.st.UpdateJob(ctx, job); err != nil {
			return
		}
		d.start(job)
	})
	d.retryMu.Lock()
	d.retryTimers[id] = t
	d.retryMu.Unlock()
}

// reportProgress records a job's latest byte counts. The in-memory
// runtime is updated on every call — that's what the TUI reads for
// speed and ETA, so throttling it would make the display stutter — but
// the write to the store is rate-limited (see progressPersistInterval).
//
// resumeOffset, when non-nil, is checkpointed in the same write: only
// single-stream url jobs use that column (chunked ones keep their resume
// state in a sidecar file), and they used to issue it as a second,
// separate UPDATE against the row this one had just touched.
func (d *Daemon) reportProgress(jobID string, done, total int64, resumeOffset *int64) {
	rt := d.getRuntime(jobID)
	now := time.Now()
	persist := true
	if rt != nil {
		rt.mu.Lock()
		if elapsed := now.Sub(rt.lastTime).Seconds(); elapsed > 0 {
			rt.speedBps = float64(done-rt.lastBytes) / elapsed
		}
		rt.lastBytes = done
		rt.lastTime = now
		rt.bytesDone = done
		rt.bytesTotal = total

		// First call for this job always writes, so a job that starts
		// and stalls still shows something durable.
		persist = rt.lastPersist.IsZero() ||
			now.Sub(rt.lastPersist) >= progressPersistInterval ||
			done-rt.lastPersistBytes >= progressPersistBytes
		if persist {
			rt.lastPersist = now
			rt.lastPersistBytes = done
		}
		rt.mu.Unlock()
	}
	// No runtime means no throttling state to consult — write through
	// rather than silently dropping the update.
	if persist {
		d.st.UpdateProgress(context.Background(), jobID, done, total, resumeOffset)
	}
}

// pause stops an active job's goroutine and waits for it to fully exit
// before writing the paused state, so this is the single, race-free
// writer of that transition (see finishJob's doc comment).
func (d *Daemon) pause(ctx context.Context, id string) (*store.Job, error) {
	job, err := d.st.GetJob(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("job %s not found", id)
	}
	if job.Status != store.StatusActive && job.Status != store.StatusQueued {
		return nil, fmt.Errorf("job %s is %s, not active", id, job.Status)
	}
	rt := d.getRuntime(id)

	if job.Type == store.JobTorrent {
		preBytes, preTotal, havePre := d.tm.Progress(id)
		d.tm.Pause(id)
		if rt != nil {
			rt.cancel()
		}
		waitForStop(rt)
		job, err = d.st.GetJob(ctx, id)
		if err != nil {
			return nil, err
		}
		if havePre {
			job.BytesDone = preBytes
			if preTotal > 0 {
				job.BytesTotal = preTotal
			}
		}
		job.Status = store.StatusPaused
		job.ErrorMsg = ""
		if err := d.st.UpdateJob(ctx, job); err != nil {
			return nil, err
		}
		return job, nil
	}

	if rt == nil {
		job.Status = store.StatusPaused
		if err := d.st.UpdateJob(ctx, job); err != nil {
			return nil, err
		}
		return job, nil
	}
	rt.cancel()
	waitForStop(rt)
	return d.st.GetJob(ctx, id)
}

func (d *Daemon) resume(ctx context.Context, id string) (*store.Job, error) {
	job, err := d.st.GetJob(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("job %s not found", id)
	}
	if job.Status != store.StatusPaused && job.Status != store.StatusFailed {
		return nil, fmt.Errorf("job %s is %s, cannot resume", id, job.Status)
	}
	// Queued before dispatch, same reason as resumeInterruptedJobs:
	// start() may leave it there rather than actually running it if
	// MaxConcurrent is already full, and tryStartQueued only looks for
	// StatusQueued — leaving it Paused/Failed here would hide it from
	// ever being picked up once a slot frees.
	job.Status = store.StatusQueued
	job.ErrorMsg = ""
	if err := d.st.UpdateJob(ctx, job); err != nil {
		return nil, err
	}
	d.start(job)
	return d.st.GetJob(ctx, id)
}

// resetForRetry clears a job's progress state so the next start begins
// completely fresh: byte counters zeroed, any partial output removed
// (a partial file left in place could otherwise be silently reused by
// a differently-configured retry — e.g. a --sha256 that would now fail
// against bytes downloaded before the checksum was added), and status
// set to Queued. Caller persists via UpdateJob and calls start(); it
// does NOT touch RetryCount, since manual retry and the auto-retry
// timer (see finishJob/scheduleAutoRetry) want different behavior
// there — see each caller.
func resetForRetry(job *store.Job) {
	job.BytesDone = 0
	job.ResumeOffset = 0
	job.ErrorMsg = ""
	job.Status = store.StatusQueued
	if job.Type == store.JobURL {
		os.Remove(job.Output + ".godl-progress.json")
		os.Remove(job.Output)
	}
	if job.Type == store.JobWebDAV {
		for _, p := range job.ResolvedPaths {
			os.Remove(p)
		}
		job.ResolvedPaths = nil
	}
}

func (d *Daemon) retry(ctx context.Context, id string) (*store.Job, error) {
	job, err := d.st.GetJob(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("job %s not found", id)
	}
	if job.Status == store.StatusActive || job.Status == store.StatusQueued {
		return nil, fmt.Errorf("job %s is already %s", id, job.Status)
	}
	resetForRetry(job)
	// A manual retry is an explicit fresh start, not another automated
	// attempt — reset the auto-retry streak so it gets the full backoff
	// budget again rather than picking up where it left off.
	job.RetryCount = 0
	if err := d.st.UpdateJob(ctx, job); err != nil {
		return nil, err
	}
	d.start(job)
	return d.st.GetJob(ctx, id)
}

// cancel stops a job's goroutine (if any), waits for it to exit, and then
// writes the canceled state itself — the single, race-free writer of
// that transition, same reasoning as pause.
func (d *Daemon) cancel(ctx context.Context, id string) (*store.Job, error) {
	job, err := d.st.GetJob(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("job %s not found", id)
	}
	rt := d.getRuntime(id)
	if job.Type == store.JobTorrent {
		d.tm.Cancel(id)
	}
	if rt != nil {
		rt.cancel()
		waitForStop(rt)
	}
	job, err = d.st.GetJob(ctx, id)
	if err != nil {
		return nil, err
	}
	job.Status = store.StatusCanceled
	job.ErrorMsg = ""
	if err := d.st.UpdateJob(ctx, job); err != nil {
		return nil, err
	}
	return job, nil
}

// remove stops a job if it's still running (same as cancel, just without
// bothering to persist the canceled status — the row is about to be
// deleted anyway), optionally deletes what it downloaded, and removes
// it from the list entirely. Returns the job as it was just before
// deletion, for the caller to report what got removed.
func (d *Daemon) remove(ctx context.Context, id string, purge bool) (*store.Job, error) {
	job, err := d.st.GetJob(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("job %s not found", id)
	}
	rt := d.getRuntime(id)
	if job.Type == store.JobTorrent {
		d.tm.Cancel(id)
	}
	if rt != nil {
		rt.cancel()
		waitForStop(rt)
	}
	// Re-fetch: the stopped job's own goroutine may have just persisted
	// its final ResolvedPaths/status via finishJob.
	job, err = d.st.GetJob(ctx, id)
	if err != nil {
		return nil, err
	}

	if purge {
		removeDownloadedFiles(job)
	}

	if err := d.st.DeleteJob(ctx, id); err != nil {
		return nil, err
	}
	return job, nil
}

// removeDownloadedFiles deletes whatever a job actually wrote to disk.
// Best-effort: a file already missing (never started, already deleted,
// paused before anything was written) isn't an error worth surfacing —
// the goal ("this job's output is gone") is still met.
func removeDownloadedFiles(job *store.Job) {
	switch job.Type {
	case store.JobURL:
		os.Remove(job.Output)
		os.Remove(job.Output + ".godl-progress.json")
	case store.JobTorrent:
		// job.Output is the directory the user passed with -o; the
		// torrent's actual content lives at Output/<torrent name>,
		// captured into ResolvedPaths once the torrent's metadata
		// arrived. Nothing recorded means either the job never got
		// that far, or it's an older job predating this tracking —
		// either way, deleting the whole -o directory would be wrong
		// (it's not a per-job directory), so there's nothing safe to
		// remove.
		if len(job.ResolvedPaths) > 0 {
			os.RemoveAll(filepath.Join(job.Output, job.ResolvedPaths[0]))
		}
	case store.JobSocial, store.JobWebDAV:
		// Each entry is a full, exact local path: for JobSocial, what
		// yt-dlp reported via its after_move hook (the true final file,
		// post-merge/post-processing) — see startSocial; for JobWebDAV,
		// one downloaded file's exact local path, covering both the
		// single-file and whole-folder case — see startWebDAV.
		for _, p := range job.ResolvedPaths {
			os.Remove(p)
		}
	}
}
