//go:build !linux && !darwin

package tray

// checkSession is a no-op on Windows, which puts every interactive
// login in a session that has a notification area, so there is
// nothing to probe for. Linux and macOS each have a session that can
// lack one and check for it; see session_linux.go and
// session_darwin.go.
func checkSession() error { return nil }
