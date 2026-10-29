//go:build linux

package tray

import (
	"os"
	"path/filepath"

	"github.com/godbus/dbus/v5"
)

// watcherName is the bus name a desktop's tray host claims. The icon is
// published as a StatusNotifierItem and is only shown if something has
// registered as the watcher for them.
const watcherName = "org.kde.StatusNotifierWatcher"

// checkSession reports whether this Linux session can show a tray icon
// at all. Two things have to hold, and a session bus alone is not
// enough for the second.
func checkSession() error {
	if !hasSessionBus() {
		return noSession("no D-Bus session bus; set DBUS_SESSION_BUS_ADDRESS or run this inside a desktop session")
	}
	return checkWatcher()
}

// hasSessionBus looks for the bus that carries the StatusNotifierItem
// interface. A display is neither sufficient nor required: the icon is
// published over the bus, not drawn onto X or Wayland by godl.
func hasSessionBus() bool {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return true
	}
	// Fall back to the well-known socket, which systemd user sessions
	// provide even when the variable isn't exported into this process.
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		if _, err := os.Stat(filepath.Join(rt, "bus")); err == nil {
			return true
		}
	}
	return false
}

// checkWatcher confirms something is actually hosting tray icons.
//
// This is what stops the daemon leaving a useless process behind: a bus
// with no watcher (a bare dbus-run-session, or a desktop offering only
// the older XEmbed tray) lets systray start and "succeed", logging a
// registration failure and then sitting there forever showing nothing.
// Before the daemon spawned the tray itself that was a visible error in
// front of whoever typed the command; now it would happen silently at
// every daemon start, so it has to be detected rather than tolerated.
func checkWatcher() error {
	conn, err := dbus.SessionBus()
	if err != nil {
		return noSession("could not reach the session bus: " + err.Error())
	}
	// Not closed: godbus hands out a shared connection that systray
	// goes on to use.
	var owner string
	if err := conn.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, watcherName).Store(&owner); err != nil || owner == "" {
		return noSession("no tray host on the session bus — KDE has one built in, GNOME needs the AppIndicator extension")
	}
	return nil
}
