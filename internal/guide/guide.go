// Package guide holds godl's end-user guide: one self-contained HTML
// page (no network, no external assets) compiled into the binary, so
// "godl guide" works on a machine that has nothing but godl itself.
//
// It's a second, task-first telling of what the README covers, for
// someone who installed a package and never saw the repository.
// cmd/guide_test.go checks every command line in it against the real
// CLI, so a renamed flag fails the build's tests instead of quietly
// teaching something that no longer works.
package guide

import (
	_ "embed"
	"fmt"
	"os/exec"
	"runtime"
)

//go:embed guide.html
var HTML []byte

// OpenInBrowser asks the desktop to open target (a URL or a file path)
// in the user's default browser.
func OpenInBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opening a browser: %w", err)
	}
	// Not waited on: the opener hands off to the browser and exits by
	// itself, and godl has nothing to learn from when.
	return cmd.Process.Release()
}
