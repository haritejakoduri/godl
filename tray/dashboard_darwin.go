//go:build darwin

package tray

// launchTerminal asks Terminal.app to run the dashboard. AppleScript
// rather than "open", because open can launch an application or open a
// file but cannot hand a command line to a shell in a new window.
func launchTerminal(exe string) error {
	return tryEach([][]string{{"osascript", "-e", terminalAppleScript(exe)}})
}
