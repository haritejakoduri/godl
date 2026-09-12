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

// Run shows the tray icon and blocks until the user dismisses it.
// It does not start the daemon: the tray reports the daemon as stopped
// and offers to start it, so putting this in a desktop's autostart
// doesn't silently launch a download daemon at every login.
func Run() error { return run() }
