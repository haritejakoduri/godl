//go:build windows

package autostart

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

// runKey is the per-user Run key: every value under it is executed once
// at login. HKCU, not HKLM, so this needs no elevation — the same
// choice the Windows installer makes for PATH and its uninstall entry.
const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

const location = `HKCU\` + runKey + `\` + Name

func install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	// Quoted: Run values are parsed as command lines, and the default
	// install path sits under a profile directory that can contain
	// spaces.
	return k.SetStringValue(Name, `"`+exe+`" tray`)
}

func uninstall() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(Name); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}

func status() (bool, string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return false, location, nil
		}
		return false, location, err
	}
	defer k.Close()
	_, _, err = k.GetStringValue(Name)
	if err == registry.ErrNotExist {
		return false, location, nil
	}
	if err != nil {
		return false, location, err
	}
	return true, location, nil
}
