// Package desktop registers godl with the system's application menu,
// so it can be launched like any other app rather than only from a
// terminal. Each platform wants something different:
//
//	Linux     a .desktop entry plus hicolor icons
//	Windows   a Start Menu shortcut (.lnk)
//	macOS     a godl.app bundle with an Info.plist and an .icns
//
// All three are per-user, so none needs root — matching how godl
// installs itself. A system-wide install done by a .deb or .rpm writes
// the Linux files under /usr/share instead, from the package scripts
// rather than from here.
package desktop

import (
	"os"
	"path/filepath"
)

// Name is what the entry is called on disk, and on Linux the icon name
// it references. Reverse-DNS would be the modern convention, but godl
// is also the binary name and the icon theme lookup key, and keeping
// all three the same is what makes `Icon=godl` resolve without extra
// indirection.
const Name = "godl"

// Install registers the running binary with the application menu. It
// is idempotent: installing over an existing entry rewrites it, which
// is also how an entry pointing at a moved or upgraded binary gets
// corrected.
func Install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Resolved so an entry written from a symlink (or a relative
	// invocation) still points at a real binary once the shell that
	// made it is gone.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return install(exe)
}

// InstallFor registers a binary other than the running one, for an
// installer that has just written godl somewhere and wants the menu
// entry to point at that rather than at itself.
func InstallFor(exe string) error { return install(exe) }

// Remove deletes what Install wrote. Removing what was never there is
// not an error.
func Remove() error { return remove() }

// Status reports whether an entry is registered and where it lives, so
// the user can be told the path rather than left to find it.
func Status() (installed bool, location string, err error) { return status() }
