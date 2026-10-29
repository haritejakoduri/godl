// Package autostart registers a command to run when the user logs in,
// using whatever mechanism the platform expects: the HKCU Run key on
// Windows, an XDG .desktop entry on Linux, a LaunchAgent on macOS.
//
// Everything here is per-user and needs no elevation, matching how godl
// installs itself in the first place.
package autostart

import "strings"

// Name identifies godl's entry wherever the platform keeps these, so
// Install and Uninstall agree on what to write and remove.
const Name = "godl-tray"

// Install registers "<godl> tray" to run at login. It is idempotent:
// installing over an existing entry rewrites it, which is also how an
// entry pointing at a moved or upgraded binary gets corrected.
func Install() error { return install() }

// Uninstall removes the entry. Removing one that was never there is not
// an error.
func Uninstall() error { return uninstall() }

// Status reports whether an entry is registered, and where it lives —
// the location is worth showing the user, since it is the thing they
// would edit or delete by hand.
func Status() (installed bool, location string, err error) { return status() }

// escapeXML escapes s for an XML text node, used for the executable
// path inside macOS's plist. Untagged rather than sitting in
// autostart_darwin.go so it compiles and is tested everywhere, not only
// on the platform that runs it.
func escapeXML(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	).Replace(s)
}
