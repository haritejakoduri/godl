//go:build windows

package main

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// splitPath splits a Windows PATH-style string on ';', dropping empty
// segments. Pure and Windows-independent, so it's covered by a plain
// go test.
func splitPath(p string) []string {
	var out []string
	for _, part := range strings.Split(p, ";") {
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
	}
	return out
}

func samePathEntry(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(a, `\`), strings.TrimRight(b, `\`))
}

// updatePathInKey adds or removes dir from the "Path" value under k,
// preserving its registry type (REG_SZ vs REG_EXPAND_SZ — the latter is
// what Windows itself normally uses for User\Environment\Path, so
// blindly writing REG_SZ would downgrade it and break any %VAR%
// expansions already in there). Returns whether it actually changed
// anything. Takes the registry.Key directly (rather than opening
// Environment itself) so the logic can be exercised against a scratch
// test key without touching the real one.
func updatePathInKey(k registry.Key, dir string, add bool) (changed bool, err error) {
	existing, valtype, err := k.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		return false, err
	}

	parts := splitPath(existing)
	idx := -1
	for i, p := range parts {
		if samePathEntry(p, dir) {
			idx = i
			break
		}
	}

	switch {
	case add && idx >= 0:
		return false, nil
	case add:
		parts = append(parts, dir)
	case !add && idx < 0:
		return false, nil
	default:
		parts = append(parts[:idx], parts[idx+1:]...)
	}

	newVal := strings.Join(parts, ";")
	if valtype == registry.EXPAND_SZ {
		err = k.SetExpandStringValue("Path", newVal)
	} else {
		err = k.SetStringValue("Path", newVal)
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func addToUserPath(dir string) (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	changed, err := updatePathInKey(k, dir, true)
	if err != nil {
		return false, err
	}
	if changed {
		broadcastEnvChange()
	}
	return changed, nil
}

func removeFromUserPath(dir string) (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	changed, err := updatePathInKey(k, dir, false)
	if err != nil {
		return false, err
	}
	if changed {
		broadcastEnvChange()
	}
	return changed, nil
}
