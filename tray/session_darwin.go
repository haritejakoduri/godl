//go:build darwin

package tray

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// launchctlPath is absolute because an attached tray is spawned by the
// daemon and inherits the daemon's environment, not a login shell's —
// PATH may not have /bin in it.
const launchctlPath = "/bin/launchctl"

// checkSessionTimeout bounds the probe. It runs on every daemon start,
// and a launchctl that never answers must not turn into a daemon that
// never finishes starting.
const checkSessionTimeout = 2 * time.Second

// checkSession reports whether this macOS login has a menu bar to put
// an icon in. Being logged in is not enough: a shell over SSH and a
// LaunchDaemon are both ordinary macOS sessions with no window server
// behind them, and that is exactly where the daemon spawns a tray for
// itself at every start.
//
// The question did not arise while macOS was cross-compiled: without
// cgo there was no tray in the binary at all, so Attach returned
// ErrUnsupported and came to the same silent no-op. A native build has
// a real Cocoa tray in it, so something has to ask first.
func checkSession() error {
	out, err := launchctlManagerName()
	if err != nil {
		// Permissive on purpose. launchctl missing, moved, or refusing
		// to answer is not evidence that there is no desktop, and
		// hiding the tray from someone who has one is the worse of the
		// two mistakes — they asked for this icon.
		return nil
	}
	if !isAquaSession(out) {
		return noSession("launchctl reports a " + strings.TrimSpace(out) +
			" session, which has no menu bar; the tray needs a desktop login")
	}
	return nil
}

// launchctlManagerName asks which domain this process is in. Split out
// so the test can tell "launchctl would not answer" — where the probe
// deliberately assumes a desktop — apart from an answer it rejected.
func launchctlManagerName() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), checkSessionTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, launchctlPath, "managername").Output()
	return string(out), err
}
