//go:build !linux

package tray

// checkSession is a no-op away from Linux: Windows and macOS put every
// interactive login in a session that has a notification area, so
// there is nothing to probe for.
func checkSession() error { return nil }
