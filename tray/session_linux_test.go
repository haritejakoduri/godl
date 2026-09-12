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
// SSH say something useful instead.
func TestCheckSessionWithoutABusFails(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // exists, but holds no "bus"

	err := checkSession()
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("checkSession() = %v, want ErrNoSession", err)
	}
}

func TestCheckSessionAcceptsAnExplicitBusAddress(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
	if err := checkSession(); err != nil {
		t.Errorf("checkSession() = %v, want nil when the bus address is set", err)
	}
}

// TestCheckSessionFindsTheWellKnownSocket covers the systemd user
// session case: the bus exists at $XDG_RUNTIME_DIR/bus but the variable
// was never exported into this process, which is normal for a program
// launched from an autostart entry.
func TestCheckSessionFindsTheWellKnownSocket(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("XDG_RUNTIME_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "bus"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkSession(); err != nil {
		t.Errorf("checkSession() = %v, want nil when $XDG_RUNTIME_DIR/bus exists", err)
	}
}
