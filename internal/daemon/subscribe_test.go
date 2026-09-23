package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"godl/internal/store"
)

// TestSubscribeReconnectsAfterTheConnectionDrops is the guard against the
// dashboard freezing when the daemon goes away: a dropped subscription
// must be reported and then re-established, not left dead.
func TestSubscribeRetryingReconnectsAfterTheConnectionDrops(t *testing.T) {
	// Not t.TempDir(): unix socket paths are capped near 100 bytes.
	dir, err := os.MkdirTemp("", "gs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	t.Setenv("GODL_SOCKET_PATH", sock)

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Each accepted connection gets one snapshot naming its own job, then
	// the first is dropped while the second is held open.
	go func() {
		for i := 1; ; i++ {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Read the subscribe request before answering. Closing the
			// connection while the client is still writing it would fail
			// that write instead of the read below, so the client would
			// retry without ever seeing this connection's snapshot —
			// which is a different path from the one under test.
			if _, err := bufio.NewReader(conn).ReadBytes('\n'); err != nil {
				conn.Close()
				continue
			}
			id := string(rune('0' + i))
			line, _ := json.Marshal(Response{Jobs: []*JobView{{Job: &store.Job{ID: id}}}})
			conn.Write(append(line, '\n'))
			if i == 1 {
				conn.Close()
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	snapCh, errCh := SubscribeRetrying(ctx)

	next := func() []*JobView {
		t.Helper()
		select {
		case jobs, ok := <-snapCh:
			if !ok {
				t.Fatal("snapshot channel closed while ctx was live")
			}
			return jobs
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a snapshot")
			return nil
		}
	}

	if jobs := next(); len(jobs) != 1 || jobs[0].ID != "1" {
		t.Fatalf("first snapshot = %v, want job 1", jobs)
	}
	if jobs := next(); len(jobs) != 1 || jobs[0].ID != "2" {
		t.Fatalf("snapshot after the drop = %v, want job 2 (from the reconnect)", jobs)
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Error("the drop was reported as a nil error")
		}
	default:
		t.Error("the dropped connection was never reported on the error channel")
	}
	cancel()
	select {
	case _, ok := <-snapCh:
		if ok {
			t.Error("got a snapshot after cancel")
		}
	case <-time.After(5 * time.Second):
		t.Error("snapshot channel not closed after cancel")
	}
}

// TestSubscribeClosesWhenTheConnectionDrops pins the contract the system
// tray relies on: plain Subscribe ends when the daemon goes away (its
// snapshot channel closes) rather than reconnecting, so a caller ranging
// over it can notice the daemon is gone.
func TestSubscribeClosesWhenTheConnectionDrops(t *testing.T) {
	dir, err := os.MkdirTemp("", "gs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	t.Setenv("GODL_SOCKET_PATH", sock)

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		line, _ := json.Marshal(Response{Jobs: []*JobView{{Job: &store.Job{ID: "1"}}}})
		conn.Write(append(line, '\n'))
		conn.Close()
	}()

	snapCh, errCh := Subscribe(context.Background())
	deadline := time.After(5 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-snapCh:
		case <-deadline:
			t.Fatal("snapshot channel stayed open after the daemon hung up")
		}
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Error("nil error reported")
		}
	default:
		t.Error("the drop was not reported on the error channel")
	}
}
