// Package tray is godl's system tray front end: a notification-area
// icon showing what the daemon is doing, with a menu to open the
// dashboard, pause or resume everything, and — the reason it exists —
// stop the daemon without hunting for its process.
//
// Like package tui, it is one of godl's front ends and depends on the
// core, never the other way round. Run is its entire public surface;
// see boundary_test.go for the rule that keeps it that way.
package tray

import "errors"

// ErrUnsupported is what Run returns where godl has no tray to show. In
// practice that is a macOS build made without cgo: the tray there needs
// Cocoa, so the published macOS binary is built natively with cgo while
// a plain CGO_ENABLED=0 cross-compile still builds and runs, minus this
// one feature.
var ErrUnsupported = errors.New("the system tray is not available in this build")

// Run shows the tray icon and blocks until the user dismisses it. This
// is the explicit "godl tray" path: the daemon is not started for you,
// and the icon stays up if the daemon stops, offering to start it
// again.
func Run() error { return start(false) }

// Attach is the entry point the daemon spawns for itself, so a running
// daemon is visible without anyone knowing this command exists.
//
// Unlike Run, every reason an icon can't appear is a silent no-op: no
// desktop session (a server, a container, over SSH), no tray in this
// build, or one already up. Nobody asked for this icon, so failing to
// show it is not an error worth printing into the daemon's log on every
// start.
func Attach() error {
	err := start(true)
	switch {
	case errors.Is(err, ErrNoSession), errors.Is(err, ErrUnsupported), errors.Is(err, ErrAlreadyRunning):
		return nil
	}
	return err
}

// start claims the icon and shows it. attached ties the tray's lifetime
// to the daemon's.
func start(attached bool) error {
	// Checked before the instance lock so a headless run leaves nothing
	// behind, and before systray.Run, which would otherwise block
	// forever waiting on a tray host that isn't there.
	if err := checkSession(); err != nil {
		return err
	}
	release, err := claimInstance()
	if err != nil {
		return err
	}
	defer release()
	return run(attached)
}
