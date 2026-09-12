package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"godl/internal/store"
)

// socketPath points GODL_SOCKET_PATH at a short-lived socket.
//
// Deliberately not t.TempDir(): that embeds the test's name in the
// path, and a Unix socket address is capped at 104 bytes on macOS
// (108 on Linux) by sockaddr_un.sun_path. The long names in this file
// pushed it to exactly 104, and every bind failed with a bare
// "invalid argument" that says nothing about length. os.MkdirTemp
// keeps it short regardless of what the test is called.
func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "godl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

// serveTestDaemon runs d.Serve in the background against a socket in a
// temp dir, and returns once it is actually accepting connections.
func serveTestDaemon(t *testing.T, d *Daemon) (served chan error) {
	t.Helper()
	t.Setenv("GODL_SOCKET_PATH", socketPath(t))
	served = make(chan error, 1)
	go func() { served <- d.Serve() }()
	for i := 0; i < 100; i++ {
		if Running() {
			return served
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon never started listening")
	return nil
}

func waitServed(t *testing.T, served chan error) {
	t.Helper()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil on a clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after Shutdown")
	}
}

func TestShutdownStopsServe(t *testing.T) {
	d := newTestDaemon(t)
	served := serveTestDaemon(t, d)

	d.Shutdown()
	waitServed(t, served)

	if Running() {
		t.Error("daemon still answering pings after Shutdown")
	}
}

// TestShutdownCommandEndsAServingDaemon covers the in-process half of
// the shutdown command: the request is dispatched, answered, and the
// accept loop stops. It cannot cover the reply *surviving process
// exit*, because Serve returning here just ends a goroutine — that half
// is TestShutdownReplyReachesClientAcrossProcessExit.
func TestShutdownCommandEndsAServingDaemon(t *testing.T) {
	d := newTestDaemon(t)
	served := serveTestDaemon(t, d)

	stopped, err := Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !stopped {
		t.Fatal("Stop reported nothing was running, but the daemon was serving")
	}
	waitServed(t, served)
}

// TestStopOnNoDaemonIsNotAnError: "stop what isn't running" is a no-op,
// not a failure — the tray's Quit and "godl daemon stop" both rely on
// that rather than having to check first.
func TestStopOnNoDaemonIsNotAnError(t *testing.T) {
	t.Setenv("GODL_SOCKET_PATH", socketPath(t))
	stopped, err := Stop()
	if err != nil {
		t.Fatalf("Stop with no daemon running: %v", err)
	}
	if stopped {
		t.Error("Stop claimed it stopped a daemon that was never running")
	}
}

// TestShutdownBeforeServeStillStops covers the gap between Shutdown
// setting its flag and Serve assigning the listener: without the
// stopped check in Serve, a shutdown landing in that window would be
// dropped and the daemon would serve forever.
func TestShutdownBeforeServeStillStops(t *testing.T) {
	d := newTestDaemon(t)
	t.Setenv("GODL_SOCKET_PATH", socketPath(t))

	d.Shutdown()

	served := make(chan error, 1)
	go func() { served <- d.Serve() }()
	waitServed(t, served)
}

// TestUnfinishedJobsSurviveShutdown: stopping the daemon must not lose
// work. A job still queued when it goes down is normalized and picked
// up by the next start (resumeInterruptedJobs), which is what makes
// "Quit" in the tray safe to press mid-download.
func TestUnfinishedJobsSurviveShutdown(t *testing.T) {
	d := newTestDaemon(t)
	ctx := context.Background()
	j := seedJob(t, d, "in-flight", store.StatusActive)

	served := serveTestDaemon(t, d)
	d.Shutdown()
	waitServed(t, served)

	after, err := d.st.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status == store.StatusCompleted || after.Status == store.StatusCanceled {
		t.Fatalf("shutdown marked an unfinished job %s; it should be left to resume", after.Status)
	}
}

// daemonSubprocessEnv marks the re-exec'd copy of this test binary that
// runs a real daemon, so it can be told apart from an ordinary test run.
const daemonSubprocessEnv = "GODL_TEST_RUN_DAEMON"

// TestMain lets this test binary stand in for a real daemon process
// when re-exec'd with daemonSubprocessEnv set. That is the only way to
// exercise what happens to an in-flight reply when handling it is what
// terminates the process.
func TestMain(m *testing.M) {
	if os.Getenv(daemonSubprocessEnv) != "" {
		if err := RunForeground(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestShutdownReplyReachesClientAcrossProcessExit runs a real daemon in
// a subprocess and shuts it down over the socket, so the reply has to
// survive the process actually exiting — the one thing the in-process
// tests above cannot show, since there Serve returning only ends a
// goroutine.
//
// It does not isolate dispatch's explicit conn.Close(): removing that
// still passes, because handleConn's deferred close beats the unwind
// through Accept/Serve/RunForeground every time. What this does cover
// is the whole path end to end — dispatch, exit, and a client that gets
// its answer rather than an EOF.
func TestShutdownReplyReachesClientAcrossProcessExit(t *testing.T) {
	dir := t.TempDir()
	sock := socketPath(t)
	t.Setenv("GODL_SOCKET_PATH", sock)

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		daemonSubprocessEnv+"=1",
		"GODL_SOCKET_PATH="+sock,
		"GODL_DATA_DIR="+dir,
	)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	var up bool
	for i := 0; i < 100; i++ {
		if Running() {
			up = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !up {
		t.Fatal("daemon subprocess never started listening")
	}

	// The assertion: Call must come back with a reply rather than an
	// EOF from a socket the exiting process dropped.
	resp, err := Call(Request{Cmd: CmdShutdown})
	if err != nil {
		t.Fatalf("shutdown reply lost as the daemon exited: %v", err)
	}
	if !resp.OK {
		t.Fatalf("shutdown reported not-OK: %+v", resp)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("daemon subprocess exited badly: %v", err)
	}
}
