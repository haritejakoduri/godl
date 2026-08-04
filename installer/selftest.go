//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"

	"godl/internal/version"
)

// selftest exercises the Windows-specific mechanics (registry
// read/modify/write with type preservation, process enumeration)
// against safe, throwaway state — never the real Environment key or a
// real process kill — so it can be run for real confidence without any
// risk to the machine it's run on. Not part of normal install/uninstall
// flow; invoked explicitly via `godl-setup.exe -selftest`.
func selftest() {
	failed := false
	check := func(name string, err error) {
		if err != nil {
			failed = true
			fmt.Printf("FAIL %s: %v\n", name, err)
		} else {
			fmt.Printf("PASS %s\n", name)
		}
	}

	check("splitPath", testSplitPath())
	check("updatePathInKey (REG_SZ)", testUpdatePathInKey(false))
	check("updatePathInKey (REG_EXPAND_SZ)", testUpdatePathInKey(true))
	check("findProcessesByName", testFindProcessesByName())
	check("installedVersion", testInstalledVersion())

	fmt.Println()
	if failed {
		fmt.Println("SELFTEST FAILED")
		os.Exit(1)
	}
	fmt.Println("SELFTEST OK")
}

func testSplitPath() error {
	got := splitPath(`C:\a;;C:\b\;C:\c`)
	want := []string{`C:\a`, `C:\b\`, `C:\c`}
	if len(got) != len(want) {
		return fmt.Errorf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			return fmt.Errorf("got %v, want %v", got, want)
		}
	}
	if !samePathEntry(`C:\Foo\`, `c:\foo`) {
		return fmt.Errorf("samePathEntry should be case/trailing-slash insensitive")
	}
	return nil
}

// testUpdatePathInKey exercises the real registry (add, verify,
// idempotent-add, remove, verify, idempotent-remove) against a scratch
// key under HKCU created and destroyed within this function — never
// touching Environment\Path.
func testUpdatePathInKey(expandType bool) error {
	const testKeyPath = `Software\godl-installer-selftest`
	registry.DeleteKey(registry.CURRENT_USER, testKeyPath) // ignore error: may not exist yet

	k, _, err := registry.CreateKey(registry.CURRENT_USER, testKeyPath, registry.ALL_ACCESS)
	if err != nil {
		return fmt.Errorf("creating scratch key: %w", err)
	}
	defer func() {
		k.Close()
		registry.DeleteKey(registry.CURRENT_USER, testKeyPath)
	}()

	seed := `C:\existing\path`
	if expandType {
		if err := k.SetExpandStringValue("Path", seed); err != nil {
			return err
		}
	} else {
		if err := k.SetStringValue("Path", seed); err != nil {
			return err
		}
	}

	dir := `C:\Users\Test\AppData\Local\Programs\godl`

	changed, err := updatePathInKey(k, dir, true)
	if err != nil {
		return fmt.Errorf("add: %w", err)
	}
	if !changed {
		return fmt.Errorf("add: expected changed=true")
	}
	val, valtype, err := k.GetStringValue("Path")
	if err != nil {
		return err
	}
	if !strings.Contains(val, dir) || !strings.Contains(val, seed) {
		return fmt.Errorf("add: Path=%q missing expected entries", val)
	}
	wantType := uint32(registry.SZ)
	if expandType {
		wantType = registry.EXPAND_SZ
	}
	if valtype != wantType {
		return fmt.Errorf("add: value type changed from %d to %d (should be preserved)", wantType, valtype)
	}

	changed, err = updatePathInKey(k, dir, true)
	if err != nil {
		return fmt.Errorf("re-add: %w", err)
	}
	if changed {
		return fmt.Errorf("re-add: expected changed=false (already present)")
	}

	changed, err = updatePathInKey(k, dir, false)
	if err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	if !changed {
		return fmt.Errorf("remove: expected changed=true")
	}
	val, _, err = k.GetStringValue("Path")
	if err != nil {
		return err
	}
	if strings.Contains(val, dir) {
		return fmt.Errorf("remove: Path=%q still contains %q", val, dir)
	}
	if !strings.Contains(val, seed) {
		return fmt.Errorf("remove: Path=%q lost unrelated pre-existing entry %q", val, seed)
	}

	changed, err = updatePathInKey(k, dir, false)
	if err != nil {
		return fmt.Errorf("re-remove: %w", err)
	}
	if changed {
		return fmt.Errorf("re-remove: expected changed=false (already absent)")
	}

	return nil
}

// testFindProcessesByName verifies the toolhelp32 snapshot/iteration
// mechanism returns real, correct PIDs by searching for this selftest
// process's own image name — no process is killed.
func testFindProcessesByName() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	name := filepath.Base(self)
	pids, err := findProcessesByName(name)
	if err != nil {
		return err
	}
	if len(pids) == 0 {
		return fmt.Errorf("didn't find our own process (%s) by name", name)
	}
	selfPID := os.Getpid()
	for _, p := range pids {
		if int(p) == selfPID {
			return nil
		}
	}
	return fmt.Errorf("found processes named %s (%v) but not our own pid %d", name, pids, selfPID)
}

// testInstalledVersion verifies doInstall's upgrade-detection helper
// against a real, runnable copy of the embedded godl.exe payload
// (extracted to a scratch temp dir, never the real install dir) — both
// the "found, parses correctly" and "nothing there yet" cases.
func testInstalledVersion() error {
	tmpDir, err := os.MkdirTemp("", "godl-installer-selftest-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	exePath := filepath.Join(tmpDir, "godl.exe")

	if got := installedVersion(exePath); got != "" {
		return fmt.Errorf("nonexistent exe: got %q, want \"\"", got)
	}

	if err := os.WriteFile(exePath, godlExe, 0o755); err != nil {
		return fmt.Errorf("staging test copy: %w", err)
	}
	got := installedVersion(exePath)
	if got != version.Version {
		return fmt.Errorf("got %q, want %q (the version this installer was built with)", got, version.Version)
	}
	return nil
}
