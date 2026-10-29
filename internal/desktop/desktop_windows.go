//go:build windows

package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// shortcutName is the Start Menu entry. Windows shows the file name
// without .lnk, so this is what the user sees and types to search.
const shortcutName = "godl.lnk"

func startMenuDir() (string, error) {
	// %APPDATA%\Microsoft\Windows\Start Menu\Programs is the per-user
	// Start Menu. The all-users one under %ProgramData% would need
	// admin, which godl's installer deliberately never asks for.
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return "", fmt.Errorf("APPDATA is not set, so the Start Menu folder can't be located")
	}
	return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs"), nil
}

func entryPath() (string, error) {
	dir, err := startMenuDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, shortcutName), nil
}

// install writes the Start Menu shortcut. See shortcutScript for why
// it goes through PowerShell rather than writing the .lnk directly.
func install(exe string) error {
	link, err := entryPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}

	script := shortcutScript(link, exe, filepath.Dir(exe))

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("creating the Start Menu shortcut: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func remove() error {
	link, err := entryPath()
	if err != nil {
		return err
	}
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func status() (bool, string, error) {
	link, err := entryPath()
	if err != nil {
		return false, "", err
	}
	_, statErr := os.Stat(link)
	if statErr != nil && !os.IsNotExist(statErr) {
		return false, link, statErr
	}
	return statErr == nil, link, nil
}
