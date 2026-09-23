//go:build darwin

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// label is the LaunchAgent's reverse-DNS identifier, which launchd uses
// as the job's name and which must match the plist's file name.
const label = "com.godl.tray"

func entryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
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
	// KeepAlive is deliberately absent: this should start the tray at
	// login, not resurrect it the moment the user dismisses the icon.
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>tray</string>
	</array>
	<key>RunAtLoad</key><true/>
</dict>
</plist>
`, label, escapeXML(exe))
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return err
	}
	// Load it now so the icon appears without waiting for a logout.
	// Best-effort: a failure here (already loaded, no GUI session)
	// doesn't undo a plist that will be honored at the next login.
	exec.Command("launchctl", "load", path).Run()
	return nil
}

func uninstall() error {
	path, err := entryPath()
	if err != nil {
		return err
	}
	exec.Command("launchctl", "unload", path).Run()
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
