//go:build windows

package daemon

import "syscall"

// detachedSysProcAttr starts the daemon in its own process group with no
// visible console, so it isn't tied to the launching terminal.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
