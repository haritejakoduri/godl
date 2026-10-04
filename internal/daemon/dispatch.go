package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"

	"godl/internal/store"
)

type logMsg struct {
	jobID string
	line  string
	done  bool
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

// dispatch answers one request on a socket connection. Two commands
// hold the connection open and stream — add_social follows its result
// with the job's log lines, subscribe never sends a single result at
// all — and everything else is one request, one response (see do).
func (d *Daemon) dispatch(conn net.Conn, req Request) {
	switch req.Cmd {
	case CmdAddSocial:
		// Subscribe before starting, not after: startSocial can publish
		// log lines almost immediately, and subscribing afterwards would
		// lose the ones sent before this connection registers.
		logCh := d.subscribeLogs()
		resp := d.do(context.Background(), req)
		writeResp(conn, resp)
		if !resp.OK || resp.Job == nil {
			d.unsubscribeLogs(logCh)
			return
		}
		d.pumpLogs(conn, resp.Job.ID, logCh)

	case CmdSubscribe:
		d.streamSnapshots(conn)

	default:
		writeResp(conn, d.do(context.Background(), req))
	}
}

// do carries out one request and returns its single response. It's the
// whole command surface minus the two streaming ones, shared by the
// socket (dispatch) and the web interface (see webui.go), so the two
// front ends can't drift apart in what a command means.
func (d *Daemon) do(ctx context.Context, req Request) Response {
	// Takes createJob's two results directly, so each add_* case below
	// is a single line and the four that are identical look it.
	startAndReport := func(j *store.Job, err error) Response {
		if err != nil {
			return errResp(err)
		}
		d.start(j)
		return Response{Type: "result", OK: true, Job: d.view(j.ID)}
	}

	switch req.Cmd {
	case CmdPing:
		return Response{Type: "result", OK: true}

	case CmdAddURL:
		return startAndReport(d.createJob(ctx, store.JobURL, req.Source, req.Output, "", req.Concurrency, req.LimitRate, req.Sha256, req.Options))

	case CmdAddTorrent:
		return startAndReport(d.createJob(ctx, store.JobTorrent, req.Source, req.Output, "", 0, req.LimitRate, "", req.Options))

	case CmdAddWebDAV:
		return startAndReport(d.createJob(ctx, store.JobWebDAV, req.Source, req.Output, "", 0, req.LimitRate, "", store.JobOptions{}))

	case CmdAddSocial:
		return startAndReport(d.createJob(ctx, store.JobSocial, req.Source, req.Output, req.Format, 0, req.LimitRate, "", req.Options))

	case CmdPause:
		return jobResult(d.pause(ctx, req.JobID))

	case CmdResume:
		return jobResult(d.resume(ctx, req.JobID))

	case CmdRetry:
		return jobResult(d.retry(ctx, req.JobID))

	case CmdCancel:
		return jobResult(d.cancel(ctx, req.JobID))

	case CmdRemove:
		return jobResult(d.remove(ctx, req.JobID, req.Purge))

	case CmdTorrentFiles:
		name, files, err := d.tm.ListFiles(req.Source, torrentInfoTimeout)
		if err != nil {
			return errResp(err)
		}
		out := make([]TorrentFile, len(files))
		for i, f := range files {
			out[i] = TorrentFile{Index: f.Index, Path: f.Path, Length: f.Length}
		}
		return Response{Type: "result", OK: true, Name: name, Files: out}

	case CmdStreamTorrent:
		url, files, err := d.streamTorrent(req.JobID, req.FileIndex-1)
		if err != nil {
			return errResp(err)
		}
		return Response{Type: "result", OK: true, StreamURL: url, Files: files}

	case CmdJobFiles:
		files, note, err := d.jobFileList(req.JobID)
		if err != nil {
			return errResp(err)
		}
		// Live: the list is a running torrent's whole file list, so the
		// files can be switched on and off right there (select_files).
		infos, _ := d.tmFiles(req.JobID)
		live := infos != nil && len(infos) == len(files)
		return Response{Type: "result", OK: true, Files: files, Note: note, Live: live}

	case CmdTorrentChoice:
		name, files, err := d.torrentChoice(req.JobID)
		if err != nil {
			return errResp(err)
		}
		return Response{Type: "result", OK: true, Name: name, Files: files}

	case CmdSelectFiles:
		return jobResult(d.selectFiles(ctx, req.JobID, req.Options.TorrentFiles))

	case CmdList:
		return Response{Type: "result", OK: true, Jobs: d.snapshot()}

	case CmdGetSettings:
		s := d.cachedSettings()
		return Response{Type: "result", OK: true, Settings: &s}

	case CmdSetSettings:
		if req.Settings == nil {
			return errResp(fmt.Errorf("settings is required"))
		}
		applied, err := d.applySettings(ctx, *req.Settings)
		if err != nil {
			return errResp(err)
		}
		return Response{Type: "result", OK: true, Settings: &applied}

	default:
		return Response{Type: "error", Error: "unknown command: " + req.Cmd}
	}
}

func jobResult(j *store.Job, err error) Response {
	if err != nil {
		return errResp(err)
	}
	return Response{Type: "result", OK: true, Job: viewOf(j, nil)}
}

func errResp(err error) Response {
	return Response{Type: "error", Error: err.Error()}
}

func (d *Daemon) view(id string) *JobView {
	job, err := d.st.GetJob(context.Background(), id)
	if err != nil {
		return nil
	}
	return d.withSeedStats(viewOf(job, d.getRuntime(id)))
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
		views = append(views, d.withSeedStats(viewOf(j, d.getRuntime(j.ID))))
	}
	return views
}

// streamSnapshots pushes the job list to a subscribed client twice a
// second, but only when it has actually changed.
//
// Most of the time it hasn't: a few completed jobs sitting in the list
// produce byte-identical snapshots forever, and re-sending them made
// both sides busy for nothing — the daemon marshalling the same payload
// and the TUI rebuilding and re-rendering every row on receipt. Skipping
// unchanged payloads means an idle godl status costs nothing beyond one
// indexed query per tick. While a download is running the bytes change
// every tick and everything flows as before.
func (d *Daemon) streamSnapshots(conn net.Conn) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var lastSent []byte
	send := func() error {
		payload, err := json.Marshal(Response{Type: "snapshot", OK: true, Jobs: d.snapshot()})
		if err != nil {
			return err
		}
		payload = append(payload, '\n') // the framing every response uses
		if bytes.Equal(payload, lastSent) {
			return nil
		}
		lastSent = payload
		_, err = conn.Write(payload)
		return err
	}

	// The first snapshot always goes out, so a client that connects
	// while nothing is happening still gets the current state instead of
	// waiting for something to change.
	if err := send(); err != nil {
		return
	}
	for range ticker.C {
		if err := send(); err != nil {
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

func (d *Daemon) unsubscribeLogs(ch chan logMsg) {
	d.logMu.Lock()
	delete(d.logSubs, ch)
	d.logMu.Unlock()
}

func (d *Daemon) pumpLogs(conn net.Conn, jobID string, ch chan logMsg) {
	defer d.unsubscribeLogs(ch)

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
