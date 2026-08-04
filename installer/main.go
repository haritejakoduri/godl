//go:build windows

// Command godl-setup is a one-click, no-admin-required installer for
// godl on Windows: it embeds godl.exe, copies it to a per-user install
// directory, adds that directory to the user's PATH, and registers an
// uninstall entry so it shows up in Settings > Apps. Run it again with
// -uninstall (or use the Windows Settings uninstall button, which does
// this automatically) to remove everything it did.
package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"godl/internal/version"
)

//go:embed payload/godl.exe
var godlExe []byte

const uninstallKeyPath = `Software\Microsoft\Windows\CurrentVersion\Uninstall\godl`

func main() {
	switch {
	case hasFlag("-selftest"):
		selftest()
	case hasFlag("-uninstall"):
		run(doUninstall)
	default:
		run(doInstall)
	}
}

func hasFlag(name string) bool {
	for _, a := range os.Args[1:] {
		if a == name {
			return true
		}
	}
	return false
}

// run wraps an install/uninstall action with error handling and a
// "press enter" pause — double-clicking an exe from Explorer opens a
// console window that would otherwise vanish the instant it's done,
// before the user can read the result. Each action prints its own
// opening status line(s) rather than a fixed header here, since
// doInstall's depends on whether this is a fresh install, a repeat of
// the current version, or an upgrade.
func run(action func() error) {
	if err := action(); err != nil {
		fmt.Println()
		fmt.Println("Error:", err)
		pause()
		os.Exit(1)
	}
	pause()
}

func pause() {
	fmt.Println()
	fmt.Print("Press Enter to close this window... ")
	var discard string
	fmt.Scanln(&discard)
}

func installDir() (string, error) {
	if v := os.Getenv("GODL_INSTALL_DIR"); v != "" {
		return v, nil
	}
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return "", fmt.Errorf("%%LOCALAPPDATA%% is not set")
	}
	return filepath.Join(localAppData, "Programs", "godl"), nil
}

func dataDir() string {
	if v := os.Getenv("GODL_DATA_DIR"); v != "" {
		return v
	}
	return filepath.Join(os.Getenv("USERPROFILE"), ".local", "share", "godl")
}

func doInstall() error {
	dir, err := installDir()
	if err != nil {
		return err
	}

	exePath := filepath.Join(dir, "godl.exe")
	oldVersion := installedVersion(exePath)
	newVersion := version.Version
	switch {
	case oldVersion == "":
		fmt.Printf("Installing godl %s\n\n", newVersion)
	case oldVersion == newVersion:
		fmt.Printf("godl %s is already installed; reinstalling\n\n", newVersion)
	default:
		fmt.Printf("Upgrading godl %s -> %s\n\n", oldVersion, newVersion)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := os.WriteFile(exePath, godlExe, 0o755); err != nil {
		return fmt.Errorf("writing %s: %w", exePath, err)
	}
	fmt.Println("  wrote", exePath)

	// A permanent copy of this installer, so "Uninstall" in Windows
	// Settings still works even if the user deletes whatever they
	// originally downloaded godl-setup.exe to.
	setupPath := filepath.Join(dir, "godl-setup.exe")
	if self, err := os.Executable(); err == nil {
		if selfBytes, err := os.ReadFile(self); err == nil {
			os.WriteFile(setupPath, selfBytes, 0o755)
		}
	}

	changed, err := addToUserPath(dir)
	if err != nil {
		return fmt.Errorf("updating PATH: %w", err)
	}
	if changed {
		fmt.Println("  added", dir, "to your user PATH")
	} else {
		fmt.Println("  already on your PATH")
	}

	if err := registerUninstaller(dir, setupPath); err != nil {
		// Non-fatal: godl itself works fine without a Settings entry.
		fmt.Println("  warning: couldn't register with Windows Settings > Apps:", err)
	} else {
		fmt.Println("  registered with Windows Settings > Apps (uninstall from there works too)")
	}

	fmt.Println()
	fmt.Println("Done. Open a NEW terminal window and run: godl --help")
	return nil
}

func doUninstall() error {
	dir, err := installDir()
	if err != nil {
		return err
	}
	fmt.Println("Uninstalling godl")
	fmt.Println()

	if n := stopRunningDaemons(); n > 0 {
		fmt.Printf("  stopped %d running godl.exe process(es)\n", n)
	}

	if changed, err := removeFromUserPath(dir); err != nil {
		fmt.Println("  warning: couldn't update PATH:", err)
	} else if changed {
		fmt.Println("  removed", dir, "from your user PATH")
	}

	if err := unregisterUninstaller(); err != nil {
		fmt.Println("  warning: couldn't remove the Settings > Apps entry:", err)
	}

	dd := dataDir()
	if _, err := os.Stat(dd); err == nil {
		fmt.Println()
		fmt.Print("Also remove job history and cached yt-dlp/ffmpeg (", dd, ")? [y/N] ")
		var reply string
		fmt.Scanln(&reply)
		if strings.EqualFold(strings.TrimSpace(reply), "y") {
			if err := os.RemoveAll(dd); err != nil {
				fmt.Println("  warning: couldn't remove", dd, ":", err)
			} else {
				fmt.Println("  removed", dd)
			}
		}
	}

	fmt.Println()
	fmt.Println("Removing installed files...")
	scheduleDirCleanup(dir)
	return nil
}

// installedVersion runs an already-installed godl.exe with --version to
// find out what's there before overwriting it, so doInstall can tell
// the user whether this is a fresh install, an upgrade, or a repeat of
// what they've already got. Returns "" if nothing's installed yet or
// the check fails for any reason (best-effort — worst case, doInstall
// just falls back to "Installing godl vX" phrasing).
func installedVersion(exePath string) string {
	if _, err := os.Stat(exePath); err != nil {
		return ""
	}
	out, err := exec.Command(exePath, "--version").Output()
	if err != nil {
		return ""
	}
	// cobra's --version prints "godl version X.Y.Z"; take the last field.
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}
