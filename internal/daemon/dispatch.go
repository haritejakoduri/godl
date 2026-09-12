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

func (d *Daemon) dispatch(conn net.Conn, req Request) {
	ctx := context.Background()

	// Takes createJob's two results directly, so each add_* case below
	// is a single line and the three that are identical look it.
	startAndReport := func(j *store.Job, err error) {
		if err != nil {
			writeResp(conn, errResp(err))
			return
		}
		d.start(j)
		writeResp(conn, Response{Type: "result", OK: true, Job: d.view(j.ID)})
	}

	switch req.Cmd {
	case CmdPing:
		writeResp(conn, Response{Type: "result", OK: true})

	case CmdAddURL:
		startAndReport(d.createJob(ctx, store.JobURL, req.Source, req.Output, "", req.Concurrency, req.LimitRate, req.Sha256))

	case CmdAddTorrent:
		startAndReport(d.createJob(ctx, store.JobTorrent, req.Source, req.Output, "", 0, req.LimitRate, ""))

	case CmdAddWebDAV:
		startAndReport(d.createJob(ctx, store.JobWebDAV, req.Source, req.Output, "", 0, req.LimitRate, ""))

	case CmdAddSocial:
		j, err := d.createJob(ctx, store.JobSocial, req.Source, req.Output, req.Format, 0, req.LimitRate, "")
		if err != nil {
			writeResp(conn, errResp(err))
			return
		}
		// Subscribe before starting, not after: startSocial can publish
		// log lines almost immediately, and subscribing afterwards would
		// lose the ones sent before this connection registers.
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

	case CmdShutdown:
		writeResp(conn, Response{Type: "result", OK: true})
		// Hang up before tearing the listener down. Defensive: in
		// practice handleConn's deferred Close already wins, since
		// Shutdown has to unwind Accept, Serve and RunForeground before
		// the process goes — but the reply's delivery shouldn't rest on
		// which of two unsynchronized paths gets there first.
		conn.Close()
		d.Shutdown()

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
