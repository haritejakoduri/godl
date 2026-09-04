//go:build windows

package mpv

import "syscall"

// detachedSysProcAttr starts mpv in its own process group, decoupled
// from godl's console — unlike internal/daemon's own version of this,
// HideWindow is deliberately left false: mpv is a GUI/console media
// player whose window the user wants to see.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
