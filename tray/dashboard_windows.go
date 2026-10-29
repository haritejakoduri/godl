//go:build windows

package tray

// launchTerminal opens the dashboard in a new console window. The empty
// argument after "start" is its title parameter: without it, start
// treats a quoted program path as the window title and opens an empty
// console instead of running anything.
func launchTerminal(exe string) error {
	return tryEach([][]string{
		{"cmd", "/c", "start", "", exe, "status"},
	})
}
