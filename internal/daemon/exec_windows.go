//go:build windows

package daemon

import (
	"os"
	"syscall"
)

// detachedSysProcAttr starts the daemon in its own process group with no
// visible console, so it isn't tied to the launching terminal.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// terminate ends a process; Windows has no gentler signal to send.
func terminate(p *os.Process) error { return p.Kill() }
