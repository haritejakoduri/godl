//go:build darwin

package desktop

import (
	"os"
	"os/exec"
	"path/filepath"

	"godl/internal/version"
)

// bundleName is the .app directory. macOS shows the name without the
// extension in Finder, Launchpad and Spotlight.
const bundleName = "godl.app"

func appsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	// ~/Applications, not /Applications: per-user, so no root, which
	// matches how godl installs itself everywhere else. macOS shows
	// both in Launchpad.
	return filepath.Join(home, "Applications"), nil
}

func entryPath() (string, error) {
	dir, err := appsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, bundleName), nil
}

func install(exe string) error {
	bundle, err := entryPath()
	if err != nil {
		return err
	}

	// Rebuilt from scratch so a reinstall cannot leave stale files
	// from an older layout behind inside the bundle.
	if err := os.RemoveAll(bundle); err != nil && !os.IsNotExist(err) {
		return err
	}
	macOS := filepath.Join(bundle, "Contents", "MacOS")
	resources := filepath.Join(bundle, "Contents", "Resources")
	for _, d := range []string{macOS, resources} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	plist := infoPlistFor(version.Version)
	if err := os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		return err
	}
	// PkgInfo is legacy but cheap, and some tools still read it.
	if err := os.WriteFile(filepath.Join(bundle, "Contents", "PkgInfo"), []byte("APPL????"), 0o644); err != nil {
		return err
	}
	script := launcherScript(exe)
	if err := os.WriteFile(filepath.Join(macOS, "godl"), []byte(script), 0o755); err != nil {
		return err
	}
	icns, err := icons.ReadFile("icons/godl.icns")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(resources, "godl.icns"), icns, 0o644); err != nil {
		return err
	}

	refresh(bundle)
	return nil
}

func remove() error {
	bundle, err := entryPath()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(bundle); err != nil && !os.IsNotExist(err) {
		return err
	}
	refresh(bundle)
	return nil
}

// refresh tells Launch Services about the bundle so it shows up in
// Spotlight and Launchpad now rather than whenever macOS next rescans.
// Best-effort: lsregister lives inside a framework and has moved before,
// and the bundle is found eventually either way.
func refresh(bundle string) {
	const lsregister = "/System/Library/Frameworks/CoreServices.framework/" +
		"Frameworks/LaunchServices.framework/Support/lsregister"
	exec.Command(lsregister, "-f", bundle).Run()
}

func status() (bool, string, error) {
	bundle, err := entryPath()
	if err != nil {
		return false, "", err
	}
	// The directory alone is not enough — an empty one would not launch.
	// Info.plist is what makes macOS treat it as an application.
	_, statErr := os.Stat(filepath.Join(bundle, "Contents", "Info.plist"))
	if statErr != nil && !os.IsNotExist(statErr) {
		return false, bundle, statErr
	}
	return statErr == nil, bundle, nil
}
