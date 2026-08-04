//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// stopRunningDaemons terminates any running godl.exe process (the
// background daemon, or a foreground CLI invocation someone happens to
// have open at the exact moment of uninstall). Getting a process's full
// command line on Windows needs reading its PEB, which is more
// involved than this warrants; matching on the image name alone is
// enough for an uninstaller — the worst case is interrupting an
// in-progress foreground `godl` command, which is harmless since the
// daemon-owned job it's talking to keeps running regardless.
func stopRunningDaemons() int {
	pids, err := findProcessesByName("godl.exe")
	if err != nil {
		return 0
	}
	stopped := 0
	for _, pid := range pids {
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
		if err != nil {
			continue
		}
		if windows.TerminateProcess(h, 0) == nil {
			stopped++
		}
		windows.CloseHandle(h)
	}
	return stopped
}

func findProcessesByName(name string) ([]uint32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)

	var pids []uint32
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	err = windows.Process32First(snap, &entry)
	for err == nil {
		exe := windows.UTF16ToString(entry.ExeFile[:])
		if strings.EqualFold(exe, name) {
			pids = append(pids, entry.ProcessID)
		}
		err = windows.Process32Next(snap, &entry)
	}
	return pids, nil
}

// scheduleDirCleanup removes dir (including the setup exe currently
// executing out of it) a moment after this process exits. Windows
// holds a lock on a running exe's file, so it can't delete itself or
// its own containing directory synchronously; spawning a detached
// helper that waits for us to exit first is the standard workaround.
// If this fails for any reason, the only consequence is a handful of
// leftover files in dir, which is harmless (godl no longer runs or is
// on PATH at that point) — hence best-effort, no error return.
func scheduleDirCleanup(dir string) {
	self, err := os.Executable()
	if err != nil {
		return
	}
	// Don't delete out from under an install dir that isn't actually
	// ours, or a directory some caller pointed at $HOME by mistake.
	if filepath.Dir(self) != dir || dir == "" || dir == string(filepath.Separator) {
		return
	}
	// dir gets concatenated into a cmd.exe /C command line below (there's
	// no way to hand cmd.exe a clean argv the way exec.Command normally
	// avoids shell parsing entirely — /C's argument is a command line it
	// re-interprets itself). installDir() only returns %LOCALAPPDATA%\
	// Programs\godl or the GODL_INSTALL_DIR test override, neither of
	// which should ever contain these characters, but refuse rather than
	// build an injectable command line if one somehow did.
	if strings.ContainsAny(dir, "\"&|<>^%") {
		return
	}
	cmd := exec.Command("cmd", "/C", "timeout /t 1 /nobreak >nul & rmdir /s /q \""+dir+"\"")
	cmd.SysProcAttr = detachedSysProcAttr()
	cmd.Start()
}
