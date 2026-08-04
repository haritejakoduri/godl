//go:build windows

package main

import "syscall"

// detachedSysProcAttr starts the cleanup helper in its own process
// group with no visible console, so it isn't tied to (or killed along
// with) this installer process.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
