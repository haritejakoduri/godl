//go:build windows

package main

import "golang.org/x/sys/windows/registry"

// registerUninstaller adds a per-user (HKCU, no admin needed) entry
// under the standard Uninstall registry path, so godl shows up in
// Windows Settings > Apps with a working Uninstall button.
func registerUninstaller(dir, setupPath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	set := func(name, val string) {
		if err == nil {
			err = k.SetStringValue(name, val)
		}
	}
	set("DisplayName", "godl")
	set("UninstallString", `"`+setupPath+`" -uninstall`)
	set("InstallLocation", dir)
	set("Publisher", "godl")
	if err != nil {
		return err
	}
	if err := k.SetDWordValue("NoModify", 1); err != nil {
		return err
	}
	return k.SetDWordValue("NoRepair", 1)
}

func unregisterUninstaller() error {
	err := registry.DeleteKey(registry.CURRENT_USER, uninstallKeyPath)
	if err == registry.ErrNotExist {
		return nil
	}
	return err
}
