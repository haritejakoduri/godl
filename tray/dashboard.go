package tray

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// openDashboard launches "godl status" in a terminal window. The tray
// has no terminal of its own — it is typically started from a desktop
// autostart entry — so the dashboard needs one opened for it.
func openDashboard() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return launchTerminal(exe)
}

// tryEach runs the first candidate whose program is on PATH, and
// reports the one that started. Used by the platforms where there is no
// single answer for "the terminal".
func tryEach(candidates [][]string) error {
	var tried []string
	for _, argv := range candidates {
		if _, err := exec.LookPath(argv[0]); err != nil {
			continue
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		if err := cmd.Start(); err != nil {
			tried = append(tried, argv[0])
			continue
		}
		// Reaped in the background so the tray doesn't accumulate
		// zombies for every dashboard the user opens.
		go cmd.Wait()
		return nil
	}
	if len(tried) > 0 {
		return fmt.Errorf("no terminal emulator would start (tried %v)", tried)
	}
	return fmt.Errorf("no terminal emulator found to open the dashboard in")
}

// terminalAppleScript builds the script macOS runs to open the
// dashboard. Two quoting layers stack here and the order matters: the
// path is shell-quoted first because "do script" hands its argument to
// a shell, and the result is then escaped for the AppleScript string
// literal it sits inside. Doing it the other way round would let the
// shell see the AppleScript's own escapes.
//
// Untagged, not in dashboard_darwin.go, so this is compiled and tested
// everywhere rather than only on the one platform that runs it.
func terminalAppleScript(exe string) string {
	cmdline := shellQuote(exe) + " status"
	return `tell application "Terminal"
	activate
	do script "` + appleScriptEscape(cmdline) + `"
end tell`
}

// shellQuote wraps s for /bin/sh. Single quotes take everything
// literally, so the only case needing work is a single quote itself.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// appleScriptEscape escapes s for an AppleScript double-quoted literal.
func appleScriptEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}
