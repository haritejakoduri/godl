//go:build !windows

package daemon

import "syscall"

// detachedSysProcAttr starts the daemon in its own session so it isn't
// killed when the launching terminal closes or the parent process exits.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
