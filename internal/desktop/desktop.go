// Package desktop registers godl with the desktop environment: an
// application menu entry and the icons that go with it.
//
// Everything is per-user under $XDG_DATA_HOME (~/.local/share), so it
// needs no root — matching how godl installs itself. A system-wide
// install done by a .deb or .rpm writes the same files under /usr/share
// instead, from the package scripts rather than from here.
package desktop

// Name is the desktop file's basename and the icon name it references.
// Reverse-DNS would be the modern convention, but godl is also the
// binary name and the icon theme lookup key, and keeping all three the
// same is what makes `Icon=godl` resolve without extra indirection.
const Name = "godl"

// Install writes the desktop entry and icons, then refreshes the
// desktop's caches. It is idempotent: installing over an existing entry
// rewrites it, which is also how an entry pointing at a moved or
// upgraded binary gets corrected.
func Install() error { return install() }

// Remove deletes what Install wrote. Removing what was never there is
// not an error.
func Remove() error { return remove() }

// Status reports whether an entry is registered and where it lives, so
// the user can be told the path rather than left to find it.
func Status() (installed bool, location string, err error) { return status() }
