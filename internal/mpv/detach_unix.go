//go:build !windows

package mpv

import "syscall"

// detachedSysProcAttr starts mpv in its own session so it isn't killed
// when the launching terminal closes or godl's own process exits.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
