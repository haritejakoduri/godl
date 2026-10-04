//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

// detachedSysProcAttr starts the daemon in its own session so it isn't
// killed when the launching terminal closes or the parent process exits.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// terminate asks a process to exit.
func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
