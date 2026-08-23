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
	"godl/internal/paths"
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
	return &Daemon{
		st:       st,
		tm:       tm,
		dataDir:  dataDir,
		runtimes: map[string]*runtime{},
		logSubs:  map[chan logMsg]struct{}{},
	}, nil
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
		if j.Status == store.StatusActive || j.Status == store.StatusQueued {
			log.Printf("resuming interrupted job %s (%s)", j.ID, j.Type)
			d.start(j)
		}
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
		j, err := d.createJob(ctx, store.JobURL, req.Source, req.Output, "", req.Concurrency)
		if err != nil {
			writeResp(conn, errResp(err))
			return
		}
		d.start(j)
		writeResp(conn, Response{Type: "result", OK: true, Job: d.view(j.ID)})

	case CmdAddTorrent:
		j, err := d.createJob(ctx, store.JobTorrent, req.Source, req.Output, "", 0)
		if err != nil {
			writeResp(conn, errResp(err))
			return
		}
		d.start(j)
		writeResp(conn, Response{Type: "result", OK: true, Job: d.view(j.ID)})

	case CmdAddWebDAV:
		j, err := d.createJob(ctx, store.JobWebDAV, req.Source, req.Output, "", 0)
		if err != nil {
			writeResp(conn, errResp(err))
			return
		}
		d.start(j)
		writeResp(conn, Response{Type: "result", OK: true, Job: d.view(j.ID)})

	case CmdAddSocial:
		j, err := d.createJob(ctx, store.JobSocial, req.Source, req.Output, req.Format, 0)
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

func (d *Daemon) createJob(ctx context.Context, typ store.JobType, source, output, format string, concurrency int) (*store.Job, error) {
	if source == "" {
		return nil, fmt.Errorf("source is required")
	}
	if concurrency < 1 {
		concurrency = 1
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
		Status:      store.StatusQueued,
	}
	if err := d.st.CreateJob(ctx, j); err != nil {
		return nil, err
	}
	return j, nil
}

func (d *Daemon) start(j *store.Job) {
	switch j.Type {
	case store.JobURL:
		d.startURL(j)
	case store.JobTorrent:
		d.startTorrent(j)
	case store.JobSocial:
		d.startSocial(j)
	case store.JobWebDAV:
		d.startWebDAV(j)
	}
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
	switch {
	case completed:
		job.Status = store.StatusCompleted
		job.ErrorMsg = ""
	case errors.Is(err, context.Canceled):
		job.Status = store.StatusPaused
	case err != nil:
		job.Status = store.StatusFailed
		job.ErrorMsg = err.Error()
	default:
		job.Status = store.StatusFailed
		job.ErrorMsg = "job ended without completing"
	}
	d.st.UpdateJob(ctx, job)
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
	d.start(job)
	return d.st.GetJob(ctx, id)
}

func (d *Daemon) retry(ctx context.Context, id string) (*store.Job, error) {
	job, err := d.st.GetJob(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("job %s not found", id)
	}
	if job.Status == store.StatusActive || job.Status == store.StatusQueued {
		return nil, fmt.Errorf("job %s is already %s", id, job.Status)
	}
	job.BytesDone = 0
	job.ResumeOffset = 0
	job.ErrorMsg = ""
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
	job.Status = store.StatusQueued
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
	d.tm.Close()
	d.st.Close()
}
