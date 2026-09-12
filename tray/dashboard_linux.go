//go:build linux

package tray

// launchTerminal opens the dashboard in whichever terminal this desktop
// has. There is no portable answer on Linux, so this walks the usual
// suspects: the Debian/Ubuntu alternatives symlink first (it points at
// whatever the user actually chose), then the desktop-environment
// defaults, then the common standalone emulators, then xterm as the
// one that is almost always installed even when nothing else is.
func launchTerminal(exe string) error {
	return tryEach([][]string{
		{"x-terminal-emulator", "-e", exe, "status"},
		{"gnome-terminal", "--", exe, "status"},
		{"konsole", "-e", exe, "status"},
		{"xfce4-terminal", "-e", exe + " status"},
		{"kitty", exe, "status"},
		{"alacritty", "-e", exe, "status"},
		{"wezterm", "start", "--", exe, "status"},
		{"foot", exe, "status"},
		{"xterm", "-e", exe, "status"},
	})
}
