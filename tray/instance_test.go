package tray

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// trayLockIn points the instance lock at a short-lived directory.
// os.MkdirTemp rather than t.TempDir() for the same reason as the
// daemon's socket tests: t.TempDir() embeds the test name, and a Unix
// socket address is capped near 104 bytes on macOS.
func trayLockIn(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "godl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("GODL_SOCKET_PATH", filepath.Join(dir, "d.sock"))
	return dir
}

// TestClaimInstanceRejectsASecond is the guard against two icons for
// one daemon, which is now reachable two ways: the daemon spawns a tray
// on start, and the user can still run "godl tray" by hand.
func TestClaimInstanceRejectsASecond(t *testing.T) {
	trayLockIn(t)

	release, err := claimInstance()
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	defer release()

	if _, err := claimInstance(); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second claim = %v, want ErrAlreadyRunning", err)
	}
}

// TestClaimInstanceReleasesForTheNextOne: the lock must not outlive the
// tray holding it, or dismissing the icon would leave the daemon unable
// to put one up ever again.
func TestClaimInstanceReleasesForTheNextOne(t *testing.T) {
	trayLockIn(t)

	release, err := claimInstance()
	if err != nil {
		t.Fatal(err)
	}
	release()

	release2, err := claimInstance()
	if err != nil {
		t.Fatalf("claim after release: %v", err)
	}
	release2()
}

// TestClaimInstanceTakesOverAStaleLock covers a killed tray: the socket
// file survives the process, so a plain "does this file exist" check
// would lock the user out of their own tray until they deleted it by
// hand. Nothing is listening on a stale one, so the next tray takes it.
func TestClaimInstanceTakesOverAStaleLock(t *testing.T) {
	dir := trayLockIn(t)
	stale, err := instancePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(stale) != dir {
		t.Fatalf("lock landed in %s, want it beside the daemon socket in %s", filepath.Dir(stale), dir)
	}

	release, err := claimInstance()
	if err != nil {
		t.Fatalf("claim over a stale lock file: %v", err)
	}
	release()
}

// TestAttachIsSilentWithNowhereToShow is the contract the daemon relies
// on: it spawns a tray unconditionally on every start, including on a
// server with no desktop, so "there is nowhere to put an icon" must be
// a quiet no-op rather than an error the daemon logs at every boot.
func TestAttachIsSilentWithNowhereToShow(t *testing.T) {
	trayLockIn(t)
	// No session bus: on Linux this is exactly the headless case, and
	// on the platforms whose checkSession always passes, the tray still
	// stops at the unsupported build or the instance lock.
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	if err := Attach(); err != nil {
		t.Fatalf("Attach() = %v, want nil — the daemon spawns this unconditionally", err)
	}
}

// TestAttachStandsDownWhenOneIsAlreadyUp: with a tray already showing,
// the daemon's spawn must exit quietly rather than raise a second icon
// or report a failure.
func TestAttachStandsDownWhenOneIsAlreadyUp(t *testing.T) {
	trayLockIn(t)

	release, err := claimInstance()
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if err := Attach(); err != nil {
		t.Fatalf("Attach() with a tray already up = %v, want nil", err)
	}
}
