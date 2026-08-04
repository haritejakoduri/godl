//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// broadcastEnvChange tells other top-level windows (Explorer, etc.) that
// the environment changed, per the documented WM_SETTINGCHANGE
// mechanism. This isn't wrapped in golang.org/x/sys/windows (it's a
// GUI messaging API, not core Win32), so it's declared directly via
// syscall.LazyDLL — the standard no-cgo way to call an arbitrary Win32
// function. Already-open terminals still won't see the new PATH until
// restarted; this just saves a full logoff for everything else.
var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	procSendMessageTimeoutW = user32.NewProc("SendMessageTimeoutW")
)

const (
	hwndBroadcast    = 0xffff
	wmSettingChange  = 0x001A
	smtoAbortIfHung  = 0x0002
	broadcastTimeout = 5000 // ms
)

func broadcastEnvChange() {
	param, err := syscall.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	procSendMessageTimeoutW.Call(
		hwndBroadcast,
		wmSettingChange,
		0,
		uintptr(unsafe.Pointer(param)),
		smtoAbortIfHung,
		broadcastTimeout,
		0,
	)
}
