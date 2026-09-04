//go:build windows

package player

import "syscall"

// detachedSysProcAttr starts the player in its own process group,
// decoupled from godl's console — unlike internal/daemon's own
// version of this, HideWindow is deliberately left false: mpv/VLC are
// GUI/console media players whose window the user wants to see.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
