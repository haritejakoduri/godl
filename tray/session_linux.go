//go:build linux

package tray

import (
	"os"
	"path/filepath"
)

// checkSession looks for a D-Bus session bus, which is what carries the
// StatusNotifierItem interface the Linux tray is built on. A display
// alone isn't enough and isn't required: the icon is published over the
// bus, not drawn onto X or Wayland by godl.
func checkSession() error {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return nil
	}
	// Fall back to the well-known socket, which systemd user sessions
	// provide even when the variable isn't exported into this process.
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		if _, err := os.Stat(filepath.Join(rt, "bus")); err == nil {
			return nil
		}
	}
	return noSession("no D-Bus session bus; set DBUS_SESSION_BUS_ADDRESS or run this inside a desktop session")
}
