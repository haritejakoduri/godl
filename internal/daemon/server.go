package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"godl/internal/downloader"
	"godl/internal/ffmpeg"
	"godl/internal/notify"
	"godl/internal/paths"
	"godl/internal/ratelimit"
	"godl/internal/store"
	"godl/internal/torrentmgr"
	"godl/internal/ytdlp"
)

// runtime holds the in-memory state of an active job that doesn't belong
// in the database: how to cancel it, the samples needed for a live
// speed/ETA, and a done channel that closes only after the job's
// goroutine has finished persisting its terminal state — so pause/cancel
// handlers can wait for it and avoid racing the goroutine's own DB write.
type runtime struct {
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
	bytesDone  int64
	bytesTotal int64
	speedBps   float64
	lastBytes  int64
	lastTime   time.Time
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

type logMsg struct {
	jobID string
	line  string
	done  bool
}

type Daemon struct {
	st      *store.Store
	tm      *torrentmgr.Manager
	dataDir string

	mu       sync.Mutex
	runtimes map[string]*runtime

	logMu   sync.Mutex
	logSubs map[chan logMsg]struct{}

	// settingsMu guards settings, the in-memory cache of the store's
	// settings table — read on every job start/finish (max concurrency,
	// default rate limit, auto-retry, notifications), so those hot
	// paths don't hit sqlite each time. Refreshed on every set_settings.
	settingsMu sync.RWMutex
	settings   store.Settings

	// retryMu guards retryTimers, the pending auto-retry backoff timers
	// keyed by job ID (see scheduleAutoRetry/finishJob) — tracked so
	// Close can stop them, rather than letting one fire after the store
	// it would write to is already closed.
	retryMu     sync.Mutex
	retryTimers map[string]*time.Timer
}

func NewDaemon() (*Daemon, error) {
	dataDir, err := paths.DataDir()
	if err != nil {
		return nil, err
	}
	dbPath, err := paths.DBPath()
	if err != nil {
		return nil, err
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	torrentDir, err := paths.TorrentDataDir()
	if err != nil {
		return nil, err
	}
	tm, err := torrentmgr.New(torrentDir)
	if err != nil {
		st.Close()
		return nil, err
	}
	settings, err := st.GetSettings(context.Background())
	if err != nil {
		tm.Close()
		st.Close()
		return nil, err
	}
	return &Daemon{
		st:          st,
		tm:          tm,
		dataDir:     dataDir,
		runtimes:    map[string]*runtime{},
		logSubs:     map[chan logMsg]struct{}{},
		settings:    settings,
		retryTimers: map[string]*time.Timer{},
	}, nil
}

func (d *Daemon) cachedSettings() store.Settings {
	d.settingsMu.RLock()
	defer d.settingsMu.RUnlock()
	return d.settings
}

func (d *Daemon) setCachedSettings(s store.Settings) {
	d.settingsMu.Lock()
	d.settings = s
	d.settingsMu.Unlock()
}

// applySettings validates s, persists it, refreshes the in-memory
// cache, and — since MaxConcurrent may have just gone up — tries to
// start whatever's queued. Returns the settings actually saved (equal
// to s on success; validation errors are rejected outright rather than
// silently clamped, so what the caller sees saved is always exactly
// what it asked for).
func (d *Daemon) applySettings(ctx context.Context, s store.Settings) (store.Settings, error) {
	if s.MaxConcurrent < 0 {
		return store.Settings{}, fmt.Errorf("max concurrent downloads can't be negative")
	}
	if s.DefaultRateLimit != "" {
		if _, err := ratelimit.ParseRate(s.DefaultRateLimit); err != nil {
			return store.Settings{}, err
		}
	}
	if s.AutoRetryMaxAttempts < 1 {
		return store.Settings{}, fmt.Errorf("auto-retry max attempts must be at least 1")
	}
	if err := d.st.SaveSettings(ctx, s); err != nil {
		return store.Settings{}, err
	}
	d.setCachedSettings(s)
	d.tryStartQueued()
	return s, nil
}

// Serve accepts connections on the Unix socket until the listener closes.
// On startup it resumes any job that was left active/queued from a
// previous run (the process died or the machine restarted).
func (d *Daemon) Serve() error {
	sockPath, err := paths.SocketPath()
	if err != nil {
		return err
	}
	os.Remove(sockPath) // stale socket from a killed daemon

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return err
	}
	defer l.Close()
	defer os.Remove(sockPath)
	// Unix sockets get created with a mode based on umask (often
	// world-connectable), and SocketPath() can fall back to a shared
	// temp dir when $XDG_RUNTIME_DIR isn't set — restrict explicitly so
	// another local user can never issue commands to this daemon
	// (read job URLs/tokens, start/cancel downloads as this user)
	// regardless of which directory it ends up in.
	if err := os.Chmod(sockPath, 0o600); err != nil {
		return err
	}

	d.resumeInterruptedJobs()

	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			log.Printf("accept: %v", err)
			continue
		}
		go d.handleConn(conn)
	}
}

func (d *Daemon) resumeInterruptedJobs() {
	ctx := context.Background()
	jobs, err := d.st.ListJobs(ctx)
	if err != nil {
		log.Printf("resume scan: %v", err)
		return
	}
	for _, j := range jobs {
		if j.Status != store.StatusActive && j.Status != store.StatusQueued {
			continue
		}
		log.Printf("resuming interrupted job %s (%s)", j.ID, j.Type)
		// Normalize to Queued before dispatch: start() may leave it
		// there rather than actually running it (MaxConcurrent already
		// full from an earlier job in this same loop), and a job stuck
		// showing "active" while nothing is running it would be wrong —
		// tryStartQueued only ever looks for StatusQueued.
		if j.Status != store.StatusQueued {
			j.Status = store.StatusQueued
			if err := d.st.UpdateJob(ctx, j); err != nil {
				log.Printf("resume scan: updating %s to queued: %v", j.ID, err)
				continue
			}
		}
		d.start(j)
	}
}

func (d *Daemon) handleConn(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}
	var req Request
	if err := json.Unmarshal(bytes.TrimSpace(line), &req); err != nil {
		writeResp(conn, Response{Type: "error", Error: "bad request: " + err.Error()})
		return
	}
	d.dispatch(conn, req)
}

func writeResp(w io.Writer, r Response) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

func (d *Daemon) dispatch(conn net.Conn, req Request) {
	ctx := context.Background()
	switch req.Cmd {
	case CmdPing:
		writeResp(conn, Response{Type: "result", OK: true})

	case CmdAddURL:
		j, err := d.createJob(ctx, store.JobURL, req.Source, req.Output, "", req.Concurrency, req.LimitRate, req.Sha256)
		if err != nil {
			writeResp(conn, errResp(err))
			return
		}
		d.start(j)
		writeResp(conn, Response{Type: "result", OK: true, Job: d.view(j.ID)})

	case CmdAddTorrent:
		j, err := d.createJob(ctx, store.JobTorrent, req.Source, req.Output, "", 0, req.LimitRate, "")
		if err != nil {
			writeResp(conn, errResp(err))
			return
		}
		d.start(j)
		writeResp(conn, Response{Type: "result", OK: true, Job: d.view(j.ID)})

	case CmdAddWebDAV:
		j, err := d.createJob(ctx, store.JobWebDAV, req.Source, req.Output, "", 0, req.LimitRate, "")
		if err != nil {
			writeResp(conn, errResp(err))
			return
		}
		d.start(j)
		writeResp(conn, Response{Type: "result", OK: true, Job: d.view(j.ID)})

	case CmdAddSocial:
		j, err := d.createJob(ctx, store.JobSocial, req.Source, req.Output, req.Format, 0, req.LimitRate, "")
		if err != nil {
			writeResp(conn, errResp(err))
			return
		}
		// Subscribe before starting the job, not after: startSocial can
		// publish log lines (e.g. "downloading yt-dlp...") almost
		// immediately, and a subscribe-after-start ordering could lose
		// them in the gap before this connection registers itself.
		logCh := d.subscribeLogs()
		d.start(j)
		writeResp(conn, Response{Type: "result", OK: true, Job: d.view(j.ID)})
		d.pumpLogs(conn, j.ID, logCh)

	case CmdPause:
		j, err := d.pause(ctx, req.JobID)
		writeResult(conn, j, err)

	case CmdResume:
		j, err := d.resume(ctx, req.JobID)
		writeResult(conn, j, err)

	case CmdRetry:
		j, err := d.retry(ctx, req.JobID)
		writeResult(conn, j, err)

	case CmdCancel:
		j, err := d.cancel(ctx, req.JobID)
		writeResult(conn, j, err)

	case CmdRemove:
		j, err := d.remove(ctx, req.JobID, req.Purge)
		writeResult(conn, j, err)

	case CmdList:
		writeResp(conn, Response{Type: "result", OK: true, Jobs: d.snapshot()})

	case CmdSubscribe:
		d.streamSnapshots(conn)

	case CmdGetSettings:
		s := d.cachedSettings()
		writeResp(conn, Response{Type: "result", OK: true, Settings: &s})

	case CmdSetSettings:
		if req.Settings == nil {
			writeResp(conn, errResp(fmt.Errorf("settings is required")))
			return
		}
		applied, err := d.applySettings(ctx, *req.Settings)
		if err != nil {
			writeResp(conn, errResp(err))
			return
		}
		writeResp(conn, Response{Type: "result", OK: true, Settings: &applied})

	default:
		writeResp(conn, Response{Type: "error", Error: "unknown command: " + req.Cmd})
	}
}

func writeResult(conn net.Conn, j *store.Job, err error) {
	if err != nil {
		writeResp(conn, errResp(err))
		return
	}
	writeResp(conn, Response{Type: "result", OK: true, Job: viewOf(j, nil)})
}

func errResp(err error) Response {
	return Response{Type: "error", Error: err.Error()}
}

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

// start dispatches a job to its type-specific starter, recovering from any
// panic that escapes it. Job starters do real work (parsing sources,
// talking to third-party libraries like anacrolix/torrent) synchronously
// on this goroutine before handing off to a background one, so a bug or
// a malformed input surfacing as a panic here would otherwise crash the
// entire daemon process — every job, not just this one. That's especially
// bad for resumeInterruptedJobs, which calls start for jobs restored from
// disk: without this recover, a single bad persisted job would crash the
// daemon on every subsequent restart, forever. Recovered panics are
// reported the same way an ordinary error would be: the job fails, and
// every other job keeps running.
//
// Every caller that wants a job running — the four add_* handlers,
// resume, retry, resumeInterruptedJobs, and the auto-retry timer — goes
// through here rather than calling a startX function directly, so the
// max-concurrent-downloads cap (see acquireSlot) only has to be
// enforced in one place. When the cap is full, start is a no-op: the
// job simply stays in the store as StatusQueued (every caller sets that
// before calling start, same as a brand new job already does) and
// tryStartQueued picks it up once a slot frees.
func (d *Daemon) start(j *store.Job) {
	if !d.acquireSlot(j.ID) {
		return
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
	ctx := context.Background()
	for {
		max := d.cachedSettings().MaxConcurrent
		d.mu.Lock()
		full := max > 0 && len(d.runtimes) >= max
		d.mu.Unlock()
		if full {
			return
		}
		j, err := d.nextQueuedJob(ctx)
		if err != nil || j == nil {
			return
		}
		d.start(j)
	}
}

// nextQueuedJob returns the oldest StatusQueued job, or nil if there is
// none. ListJobs already orders oldest-created-first, so the first
// match is it.
func (d *Daemon) nextQueuedJob(ctx context.Context) (*store.Job, error) {
	jobs, err := d.st.ListJobs(ctx)
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if j.Status == store.StatusQueued {
			return j, nil
		}
	}
	return nil, nil
}

func (d *Daemon) startURL(j *store.Job) {
	ctx, cancel := context.WithCancel(context.Background())
	rt := &runtime{cancel: cancel, done: make(chan struct{}), lastTime: time.Now(), bytesDone: j.BytesDone, bytesTotal: j.BytesTotal}
	d.setRuntime(j.ID, rt)
	d.st.UpdateStatus(context.Background(), j.ID, store.StatusActive, "")

	// Single-stream downloads (concurrency<=1) checkpoint their resume
	// offset on every progress tick, not just at pause/completion, so an
	// ungraceful daemon death loses at most one tick of progress.
	// Concurrent chunked downloads checkpoint via their own sidecar file
	// instead (see internal/downloader) and don't use ResumeOffset at all.
	single := j.Concurrency <= 1

	go func() {
		defer close(rt.done)
		defer d.clearRuntime(j.ID)

		res, err := downloader.Run(ctx, downloader.Options{
			URL:         j.Source,
			OutputPath:  j.Output,
			Concurrency: j.Concurrency,
			StartOffset: j.ResumeOffset,
			Limiter:     ratelimit.NewLimiter(j.LimitRate),
			Sha256:      j.Sha256,
			Progress: func(done, total int64) {
				d.reportProgress(j.ID, done, total)
				if single {
					d.st.UpdateResumeOffset(context.Background(), j.ID, done)
				}
			},
		})
		d.finishJob(j.ID, res.BytesDone, res.Completed, err)
	}()
}

func (d *Daemon) startTorrent(j *store.Job) {
	ctx, cancel := context.WithCancel(context.Background())
	rt := &runtime{cancel: cancel, done: make(chan struct{}), lastTime: time.Now(), bytesDone: j.BytesDone, bytesTotal: j.BytesTotal}
	d.setRuntime(j.ID, rt)
	d.st.UpdateStatus(context.Background(), j.ID, store.StatusActive, "")

	// anacrolix/torrent's rate limiter is shared by its whole Client, not
	// per-torrent (see torrentmgr's doc comment) — setting it here means
	// the most recently started torrent job's limit wins for all
	// concurrently active ones, not just this one.
	if j.LimitRate > 0 {
		d.tm.SetDownloadLimit(j.LimitRate)
	}

	t, err := d.tm.Add(j.ID, j.Source, j.Output)
	if err != nil {
		close(rt.done)
		d.clearRuntime(j.ID)
		d.finishJob(j.ID, j.BytesDone, false, err)
		return
	}

	go func() {
		defer close(rt.done)
		defer d.clearRuntime(j.ID)

		select {
		case <-t.GotInfo():
			if job, gerr := d.st.GetJob(context.Background(), j.ID); gerr == nil {
				if hex, ok := d.tm.InfoHash(j.ID); ok {
					job.InfoHash = hex
				}
				// The torrent's actual content lands at Output/<name>
				// (single file or a directory, per anacrolix's
				// storage.NewFile convention) — Output itself is just
				// the base directory the user passed with -o. Record
				// the resolved name so "godl remove --purge" knows
				// exactly what to delete without touching anything
				// else in that directory.
				if info := t.Info(); info != nil {
					job.ResolvedPaths = []string{info.Name}
				}
				d.st.UpdateJob(context.Background(), job)
			}
		case <-ctx.Done():
			// pause()/cancel() owns persisting the terminal state.
			return
		}

		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.Complete().On():
				done, _, _ := d.tm.Progress(j.ID)
				d.finishJob(j.ID, done, true, nil)
				return
			case <-ticker.C:
				done, total, ok := d.tm.Progress(j.ID)
				if ok {
					d.reportProgress(j.ID, done, total)
				}
			}
		}
	}()
}

func (d *Daemon) startSocial(j *store.Job) {
	ctx, cancel := context.WithCancel(context.Background())
	rt := &runtime{cancel: cancel, done: make(chan struct{}), lastTime: time.Now()}
	d.setRuntime(j.ID, rt)
	d.st.UpdateStatus(context.Background(), j.ID, store.StatusActive, "")

	go func() {
		defer close(rt.done)
		defer d.clearRuntime(j.ID)

		ytDlpPath, err := ytdlp.Ensure(ctx, func(msg string) { d.publishLog(j.ID, msg, false) })
		if err != nil {
			d.publishLog(j.ID, "error: "+err.Error(), true)
			d.finishJob(j.ID, 0, false, err)
			return
		}

		args := []string{
			"--newline", "-P", j.Output,
			// Machine-readable progress on its own line, so we can feed
			// it into the same byte-count/speed tracking url and
			// torrent jobs use instead of scraping the human-readable
			// "[download] 54.1% of 180MiB at 12MiB/s" text. Filtered
			// out of the human-visible log by godlProgressPrefix below.
			"--progress-template", "download:" + godlProgressPrefix + "%(progress.status)s|%(progress.downloaded_bytes)s|%(progress.total_bytes)s|%(progress.total_bytes_estimate)s",
			// Fires during/after postprocessing (merging separate
			// video+audio, embedding thumbnails, renaming, ...) with the
			// true final path — unlike the progress hook above, which
			// for a merged download reports the intermediate video/
			// audio files that get deleted right after merging. Lets
			// "godl remove --purge" know exactly what to delete later.
			// Also filtered out of the human-visible log.
			//
			// This used to be `--print "after_move:..."`, which is the
			// more semantically-precise hook for "give me the final
			// path once" — but combining --print with --progress-template
			// silently kills the download-progress hook entirely (empirically
			// confirmed against yt-dlp 2026.07.04: 0 progress lines with
			// --print present vs. the expected 11 without it, regardless
			// of flag order). A second --progress-template of type
			// postprocess sidesteps the conflict by staying within the
			// one feature, and empirically never reports the
			// soon-to-be-deleted intermediate files either — it can fire
			// more than once for the same final path (once per
			// postprocessor stage), which AppendResolvedPath already
			// no-ops on if the path repeats.
			"--progress-template", "postprocess:" + godlFilePrefix + "%(info.filepath)s",
		}
		if j.Format != "" {
			args = append(args, "-f", j.Format)
		}
		if j.LimitRate > 0 {
			// yt-dlp has its own native rate limiter — no need to
			// reimplement one for a subprocess we don't read the bytes
			// of ourselves.
			args = append(args, "--limit-rate", strconv.FormatInt(j.LimitRate, 10))
		}
		// ffmpeg is needed to merge separately-downloaded video+audio
		// streams (common with -f "bv*+ba" style selectors). Its
		// absence isn't fatal to the job — yt-dlp just leaves the
		// streams unmerged and warns — so a failure here is logged, not
		// treated as a job error.
		if ffmpegDir, err := ffmpeg.Ensure(ctx, func(msg string) { d.publishLog(j.ID, msg, false) }); err == nil {
			args = append(args, "--ffmpeg-location", ffmpegDir)
		} else {
			d.publishLog(j.ID, "warning: "+err.Error()+" — separately downloaded video/audio streams won't be merged", false)
		}
		args = append(args, j.Source)

		cmd := exec.CommandContext(ctx, ytDlpPath, args...)
		lw := &lineWriter{onLine: func(line string) {
			if done, total, ok := parseGodlProgress(line); ok {
				d.reportProgress(j.ID, done, total)
				return
			}
			if path, ok := parseGodlFile(line); ok {
				d.st.AppendResolvedPath(context.Background(), j.ID, path)
				return
			}
			d.publishLog(j.ID, line, false)
		}}
		cmd.Stdout = lw
		cmd.Stderr = lw

		runErr := cmd.Run()
		lw.flush()

		rt.mu.Lock()
		finalBytes := rt.bytesDone
		rt.mu.Unlock()

		if runErr != nil && ctx.Err() != nil {
			d.publishLog(j.ID, "", true)
			d.finishJob(j.ID, finalBytes, false, context.Canceled)
			return
		}
		if runErr != nil {
			d.publishLog(j.ID, "error: "+runErr.Error(), true)
			d.finishJob(j.ID, finalBytes, false, runErr)
			return
		}
		d.publishLog(j.ID, "", true)
		d.finishJob(j.ID, finalBytes, true, nil)
	}()
}

// godlProgressPrefix tags the machine-readable progress lines produced
// by yt-dlp's --progress-template so they can be told apart from its
// normal human-readable output.
const godlProgressPrefix = "GODLPROGRESS "

// parseGodlProgress parses a
// "GODLPROGRESS <status>|<downloaded>|<total>|<total_estimate>" line
// (fields are "NA" when yt-dlp doesn't know them) into byte counts.
// total falls back to the estimate when the exact size isn't known.
//
// When yt-dlp finds the destination file already fully present (e.g.
// re-running on something already downloaded), it fires this hook
// exactly once with status=finished but downloaded_bytes=NA — it
// didn't transfer anything, so it has no byte count for it, even
// though total_bytes is usually still known. Without special-casing
// that, this job's progress would never be recorded at all and
// list/status would show "-" forever despite completing successfully.
// Treated as fully done (using total for both) instead.
//
// Multi-stream downloads (e.g. -f "bv*+ba") report progress per
// stream, not as one smooth job-wide percentage — a video stream
// finishing and an audio stream starting will show as a reset to a
// lower percentage, which is an accurate reflection of what's actually
// happening rather than a bug.
func parseGodlProgress(line string) (done, total int64, ok bool) {
	if !strings.HasPrefix(line, godlProgressPrefix) {
		return 0, 0, false
	}
	fields := strings.Split(strings.TrimPrefix(line, godlProgressPrefix), "|")
	if len(fields) != 4 {
		return 0, 0, false
	}
	status, doneField, totalField, estimateField := fields[0], fields[1], fields[2], fields[3]

	total, err := strconv.ParseInt(totalField, 10, 64)
	if err != nil {
		total, err = strconv.ParseInt(estimateField, 10, 64)
		if err != nil {
			total = 0
		}
	}

	done, err = strconv.ParseInt(doneField, 10, 64)
	if err != nil {
		if status == "finished" && total > 0 {
			return total, total, true
		}
		return 0, 0, false
	}
	return done, total, true
}

// godlFilePrefix tags the after_move print-hook lines from yt-dlp
// carrying a final resolved output file path (see startSocial).
const godlFilePrefix = "GODLFILE "

func parseGodlFile(line string) (path string, ok bool) {
	if !strings.HasPrefix(line, godlFilePrefix) {
		return "", false
	}
	return strings.TrimPrefix(line, godlFilePrefix), true
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

func (d *Daemon) reportProgress(jobID string, done, total int64) {
	rt := d.getRuntime(jobID)
	now := time.Now()
	if rt != nil {
		rt.mu.Lock()
		if elapsed := now.Sub(rt.lastTime).Seconds(); elapsed > 0 {
			rt.speedBps = float64(done-rt.lastBytes) / elapsed
		}
		rt.lastBytes = done
		rt.lastTime = now
		rt.bytesDone = done
		rt.bytesTotal = total
		rt.mu.Unlock()
	}
	d.st.UpdateProgress(context.Background(), jobID, done, total)
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

func (d *Daemon) view(id string) *JobView {
	job, err := d.st.GetJob(context.Background(), id)
	if err != nil {
		return nil
	}
	return viewOf(job, d.getRuntime(id))
}

func viewOf(job *store.Job, rt *runtime) *JobView {
	v := &JobView{Job: job, ETASeconds: -1}
	if rt == nil {
		return v
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	v.SpeedBps = rt.speedBps
	if rt.bytesDone > job.BytesDone {
		v.BytesDone = rt.bytesDone
	}
	if rt.bytesTotal > 0 {
		v.BytesTotal = rt.bytesTotal
	}
	if rt.speedBps > 0 && v.BytesTotal > v.BytesDone {
		v.ETASeconds = int64(float64(v.BytesTotal-v.BytesDone) / rt.speedBps)
	}
	return v
}

func (d *Daemon) snapshot() []*JobView {
	jobs, err := d.st.ListJobs(context.Background())
	if err != nil {
		return nil
	}
	views := make([]*JobView, 0, len(jobs))
	for _, j := range jobs {
		views = append(views, viewOf(j, d.getRuntime(j.ID)))
	}
	return views
}

func (d *Daemon) streamSnapshots(conn net.Conn) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	if err := writeResp(conn, Response{Type: "snapshot", OK: true, Jobs: d.snapshot()}); err != nil {
		return
	}
	for range ticker.C {
		if err := writeResp(conn, Response{Type: "snapshot", OK: true, Jobs: d.snapshot()}); err != nil {
			return
		}
	}
}

func (d *Daemon) publishLog(jobID, line string, done bool) {
	d.logMu.Lock()
	defer d.logMu.Unlock()
	for ch := range d.logSubs {
		select {
		case ch <- logMsg{jobID: jobID, line: line, done: done}:
		default:
		}
	}
}

// subscribeLogs registers a buffered channel for every published log
// line (across all jobs — pumpLogs filters to the one it cares about).
// Buffered and registered up front so callers can subscribe before
// starting a job, closing the gap where early lines would otherwise be
// published before anyone's listening.
func (d *Daemon) subscribeLogs() chan logMsg {
	ch := make(chan logMsg, 64)
	d.logMu.Lock()
	d.logSubs[ch] = struct{}{}
	d.logMu.Unlock()
	return ch
}

func (d *Daemon) pumpLogs(conn net.Conn, jobID string, ch chan logMsg) {
	defer func() {
		d.logMu.Lock()
		delete(d.logSubs, ch)
		d.logMu.Unlock()
	}()

	for msg := range ch {
		if msg.jobID != jobID {
			continue
		}
		if msg.done {
			writeResp(conn, Response{Type: "log", OK: true, JobIDForLog: jobID, LogDone: true})
			return
		}
		if err := writeResp(conn, Response{Type: "log", OK: true, JobIDForLog: jobID, Line: msg.line}); err != nil {
			return
		}
	}
}

// lineWriter buffers partial writes and invokes onLine for each complete
// line, so a subprocess's raw byte stream can be forwarded line-by-line.
type lineWriter struct {
	buf    []byte
	onLine func(string)
}

func (lw *lineWriter) Write(p []byte) (int, error) {
	lw.buf = append(lw.buf, p...)
	for {
		idx := bytes.IndexByte(lw.buf, '\n')
		if idx < 0 {
			break
		}
		line := string(bytes.TrimRight(lw.buf[:idx], "\r"))
		lw.buf = lw.buf[idx+1:]
		if lw.onLine != nil && line != "" {
			lw.onLine(line)
		}
	}
	return len(p), nil
}

func (lw *lineWriter) flush() {
	if len(lw.buf) > 0 && lw.onLine != nil {
		lw.onLine(string(lw.buf))
		lw.buf = nil
	}
}

func (d *Daemon) Close() {
	d.retryMu.Lock()
	for _, t := range d.retryTimers {
		t.Stop()
	}
	d.retryTimers = nil
	d.retryMu.Unlock()
	d.tm.Close()
	d.st.Close()
}
