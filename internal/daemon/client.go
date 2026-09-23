package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"

	"godl/internal/paths"
)

// InternalDaemonArg is the hidden cobra subcommand godl re-execs itself
// with to actually run the daemon in the background.
const InternalDaemonArg = "__daemon"

// The argv the daemon re-execs godl with to show its own tray icon.
// Declared here rather than in package cmd because the daemon is what
// spawns it: cmd builds the command from these same constants, so the
// two cannot drift into a daemon that spawns a subcommand nothing
// answers to.
const (
	TrayCommand      = "tray"
	TrayAttachedFlag = "attached"
)

// NoTrayEnv turns off the tray the daemon otherwise shows for itself.
// An escape hatch that works before any database exists and without one
// — for containers, CI, and anything embedding the daemon — alongside
// the Show tray icon setting, which is where a user would normally
// turn it off.
const NoTrayEnv = "GODL_NO_TRAY"

// spawnTray starts a tray icon for this daemon, so a running daemon is
// visible and can be stopped without the user having to know "godl
// tray" exists.
//
// Fire-and-forget on purpose. The child decides for itself whether an
// icon is possible — no desktop session, no tray host, no tray in this
// build, or one already showing all end in a silent exit (see
// tray.Attach) — and a daemon must never fail to start because an icon
// could not be drawn.
func spawnTray() {
	if os.Getenv(NoTrayEnv) != "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	// os.Executable is godl in every real run, but under "go test" it
	// is the test binary, which does not understand "tray --attached"
	// and would re-run the whole suite instead — including the tests
	// that start a daemon, each spawning another copy. Refusing to
	// re-exec a test binary keeps that from turning into a fork bomb.
	if strings.HasSuffix(exe, ".test") || strings.Contains(exe, "/go-build") {
		return
	}
	cmd := exec.Command(exe, TrayCommand, "--"+TrayAttachedFlag)
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		log.Printf("tray: %v", err)
		return
	}
	cmd.Process.Release()
}

// EnsureRunning makes sure a daemon is listening on the socket, starting
// one (detached, logging to paths.LogPath) if not.
func EnsureRunning() error {
	sockPath, err := paths.SocketPath()
	if err != nil {
		return err
	}
	if pingOK(sockPath) {
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath, err := paths.LogPath()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd := exec.Command(exe, InternalDaemonArg)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting godl daemon: %w", err)
	}
	cmd.Process.Release()

	for i := 0; i < 50; i++ {
		if pingOK(sockPath) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for godl daemon to start (see %s)", logPath)
}

// Running reports whether a daemon is currently listening on the
// socket, without starting one.
func Running() bool {
	sockPath, err := paths.SocketPath()
	if err != nil {
		return false
	}
	return pingOK(sockPath)
}

// Stop asks a running daemon to exit and waits for the socket to go
// quiet. It is not an error for no daemon to be running — stopped is
// false in that case, and nothing was there to stop.
func Stop() (stopped bool, err error) {
	sockPath, err := paths.SocketPath()
	if err != nil {
		return false, err
	}
	if !pingOK(sockPath) {
		return false, nil
	}
	if _, err := Call(Request{Cmd: CmdShutdown}); err != nil {
		return false, err
	}
	// The reply is sent before the listener closes, so the process is
	// still on its way out when Call returns; wait for it to actually
	// let go of the socket rather than reporting success early.
	for i := 0; i < 50; i++ {
		if !pingOK(sockPath) {
			return true, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false, fmt.Errorf("daemon did not exit within 5s")
}

func pingOK(sockPath string) bool {
	conn, err := net.DialTimeout("unix", sockPath, 300*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	data, _ := json.Marshal(Request{Cmd: CmdPing})
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return false
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return false
	}
	var r Response
	if json.Unmarshal(bytes.TrimSpace(line), &r) != nil {
		return false
	}
	return r.OK
}

// Call sends a single request and returns the single response, for every
// command except add_social (see StreamSocial) and subscribe (see
// Subscribe).
func Call(req Request) (*Response, error) {
	sockPath, err := paths.SocketPath()
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("connecting to godl daemon: %w", err)
	}
	defer conn.Close()

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return nil, err
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		return nil, err
	}
	if resp.Type == "error" || !resp.OK {
		return &resp, fmt.Errorf("%s", resp.Error)
	}
	return &resp, nil
}

// StreamSocial issues an add_social request and forwards each live output
// line from the daemon to onLine until the job finishes or ctx is
// canceled (e.g. the user hit Ctrl-C). onStart, if non-nil, is called
// once with the created job before streaming begins. The job itself
// keeps running on the daemon regardless of whether this call returns
// early.
func StreamSocial(ctx context.Context, req Request, onStart func(*JobView), onLine func(string)) (*Response, error) {
	sockPath, err := paths.SocketPath()
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("connecting to godl daemon: %w", err)
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return nil, err
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var first Response
	if err := json.Unmarshal(bytes.TrimSpace(line), &first); err != nil {
		return nil, err
	}
	if first.Type == "error" || !first.OK {
		return &first, fmt.Errorf("%s", first.Error)
	}
	if onStart != nil {
		onStart(first.Job)
	}

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return &first, nil // connection gone; job continues in the background
		}
		var r Response
		if json.Unmarshal(bytes.TrimSpace(line), &r) != nil {
			continue
		}
		if r.Type != "log" {
			continue
		}
		if r.LogDone {
			return &first, nil
		}
		if onLine != nil {
			onLine(r.Line)
		}
	}
}

// Subscribe opens a long-lived connection streaming a job-list snapshot
// roughly twice a second. The snapshot channel closes when the connection
// ends — the daemon went away, or ctx was canceled — with the reason,
// if it wasn't ctx, on the error channel first. Callers that want to
// outlive a dropped connection use SubscribeRetrying.
func Subscribe(ctx context.Context) (<-chan []*JobView, <-chan error) {
	snapCh := make(chan []*JobView)
	errCh := make(chan error, 1)
	go func() {
		defer close(snapCh)
		if _, err := subscribeOnce(ctx, snapCh); err != nil && ctx.Err() == nil {
			errCh <- err
		}
	}()
	return snapCh, errCh
}

// SubscribeRetrying is Subscribe that reconnects, with backoff, when the
// connection drops or the daemon isn't there yet, so a dashboard left
// open across a daemon restart picks up again instead of freezing on the
// last list it saw. The snapshot channel closes only when ctx is
// canceled. Each failed attempt is reported on the error channel
// (best-effort, never blocking) until the next snapshot arrives.
//
// It never starts the daemon: one that was stopped on purpose (see Stop)
// stays stopped, and comes back into view when something else starts it.
func SubscribeRetrying(ctx context.Context) (<-chan []*JobView, <-chan error) {
	snapCh := make(chan []*JobView)
	errCh := make(chan error, 1)

	go func() {
		defer close(snapCh)
		backoff := subscribeMinBackoff
		for ctx.Err() == nil {
			gotSnapshot, err := subscribeOnce(ctx, snapCh)
			if ctx.Err() != nil {
				return
			}
			if gotSnapshot {
				backoff = subscribeMinBackoff
			}
			select {
			case errCh <- err:
			default: // an earlier error is still unread; it'll do
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff = min(backoff*2, subscribeMaxBackoff)
		}
	}()

	return snapCh, errCh
}

const (
	subscribeMinBackoff = 500 * time.Millisecond
	subscribeMaxBackoff = 5 * time.Second
)

// subscribeOnce holds one subscription connection open, forwarding
// snapshots to snapCh, and returns why it ended. gotSnapshot reports
// whether it delivered at least one, i.e. whether the connection was
// healthy before it dropped.
func subscribeOnce(ctx context.Context, snapCh chan<- []*JobView) (gotSnapshot bool, err error) {
	sockPath, err := paths.SocketPath()
	if err != nil {
		return false, err
	}
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	data, _ := json.Marshal(Request{Cmd: CmdSubscribe})
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return false, err
	}

	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return gotSnapshot, err
		}
		var r Response
		if json.Unmarshal(bytes.TrimSpace(line), &r) != nil {
			continue
		}
		select {
		case snapCh <- r.Jobs:
			gotSnapshot = true
		case <-ctx.Done():
			return gotSnapshot, ctx.Err()
		}
	}
}

// RunForeground runs the daemon in the current process until the socket
// listener is closed. Used by the hidden __daemon subcommand.
func RunForeground() error {
	// The daemon is a long-running, throughput-oriented process moving
	// megabytes/sec through short-lived buffers (see internal/downloader,
	// internal/webdav) — the default GOGC=100 collects far more often
	// than a job like that needs, and its usual RSS (tens of MB) leaves
	// plenty of room to trade some memory for fewer GC cycles. Raise the
	// GC trigger, but cap it with a soft memory limit so behavior stays
	// bounded on a memory-constrained host instead of just growing
	// unchecked — the collector still runs (harder) if the limit is
	// approached. Scoped to the daemon process only; short-lived CLI
	// invocations (godl url, godl list, ...) keep Go's defaults.
	debug.SetGCPercent(400)
	debug.SetMemoryLimit(512 << 20) // 512MiB

	d, err := NewDaemon()
	if err != nil {
		return err
	}
	defer d.Close()

	if d.cachedSettings().ShowTray {
		spawnTray()
	}
	return d.Serve()
}
