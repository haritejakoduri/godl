//go:build linux

package tray

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestCheckSessionWithoutABusFails is the guard against the tray
// blocking forever: with no session bus there is no tray host to
// answer, and systray.Run would sit there indefinitely looking like a
// hang. Failing fast with ErrNoSession is what lets "godl tray" over
// SSH say something useful, and what keeps the daemon from spawning a
// tray on a server at every start.
func TestCheckSessionWithoutABusFails(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // exists, but holds no "bus"

	err := checkSession()
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("checkSession() = %v, want ErrNoSession", err)
	}
}

// hasSessionBus is tested apart from checkSession because it is the
// half that can be exercised without a real bus: it answers "is there
// an address to try", where checkSession goes on to connect and ask
// whether anything actually hosts tray icons.
func TestHasSessionBus(t *testing.T) {
	t.Run("explicit address", func(t *testing.T) {
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
		if !hasSessionBus() {
			t.Error("hasSessionBus() = false with DBUS_SESSION_BUS_ADDRESS set")
		}
	})

	// The systemd user session case: the bus exists at
	// $XDG_RUNTIME_DIR/bus but the variable was never exported into
	// this process, which is normal for a program launched from an
	// autostart entry or spawned by the daemon.
	t.Run("well-known socket", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
		t.Setenv("XDG_RUNTIME_DIR", dir)
		if err := os.WriteFile(filepath.Join(dir, "bus"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if !hasSessionBus() {
			t.Error("hasSessionBus() = false with $XDG_RUNTIME_DIR/bus present")
		}
	})

	t.Run("nothing at all", func(t *testing.T) {
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
		t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
		if hasSessionBus() {
			t.Error("hasSessionBus() = true with no address and no socket")
		}
	})
}

// TestCheckSessionRejectsABusWithNoTrayHost is the regression test for
// the zombie this feature would otherwise create. A session bus with
// nothing watching for StatusNotifierItems — a bare dbus-run-session,
// or a desktop offering only the older XEmbed tray — lets systray start
// and keep running while showing nothing at all. When the user typed
// "godl tray" that was at least a visible error; now the daemon spawns
// it silently at every start, so an undetected one is a useless process
// left behind for the life of the session.
//
// The address points at this test's own empty directory: reachable
// enough to try, with certainly no watcher behind it.
func TestCheckSessionRejectsABusWithNoTrayHost(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "bus"))

	err := checkSession()
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("checkSession() = %v, want ErrNoSession when nothing hosts tray icons", err)
	}
}
