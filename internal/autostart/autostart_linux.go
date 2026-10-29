//go:build linux

package autostart

import (
	"fmt"
	"os"
	"path/filepath"
)

// entryPath returns the XDG autostart file. Desktops read every
// .desktop file in this directory at login; $XDG_CONFIG_HOME overrides
// the default location, as elsewhere in the spec.
func entryPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "autostart", Name+".desktop"), nil
}

func install() error {
	path, err := entryPath()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// X-GNOME-Autostart-enabled and the KDE-friendly Terminal=false are
	// both defaults, but stated explicitly: a desktop that reads this
	// file with different assumptions shouldn't get to decide whether a
	// tray icon opens a terminal window at login.
	entry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=godl tray
Comment=Show godl's download daemon in the system tray
Exec=%s tray
Terminal=false
X-GNOME-Autostart-enabled=true
`, exe)
	return os.WriteFile(path, []byte(entry), 0o644)
}

func uninstall() error {
	path, err := entryPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func status() (bool, string, error) {
	path, err := entryPath()
	if err != nil {
		return false, "", err
	}
	_, statErr := os.Stat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return false, path, statErr
	}
	return statErr == nil, path, nil
}
