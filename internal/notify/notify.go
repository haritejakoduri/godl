// Package notify sends a best-effort desktop notification when a job
// completes, for users who've turned on the Settings tab's "notify on
// complete" option. Best effort by design: the daemon has no guaranteed
// UI session to notify into (headless server, SSH-only session, no
// D-Bus, ...), so a failure here is logged and nothing more — the
// download itself already succeeded regardless of whether anyone sees
// the notification about it.
package notify

import (
	"context"
	"log"
	"os/exec"
	"runtime"
	"time"
)

// sendTimeout bounds how long a notifier subprocess gets before it's
// killed — this runs on the same goroutine path as job completion
// bookkeeping, and a hung "notify-send" (e.g. talking to a dead D-Bus
// session) must never be able to wedge that.
const sendTimeout = 5 * time.Second

// Send fires a desktop notification with title/body on platforms with a
// known mechanism: notify-send on Linux (itself a no-op with no running
// D-Bus session — this doesn't special-case that any further), osascript
// on macOS. Windows has no equivalent single-binary CLI notifier in the
// standard toolchain, so it's a no-op there rather than shipping a
// bundled dependency for one settings checkbox.
func Send(title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.CommandContext(ctx, "notify-send", title, body)
	case "darwin":
		// title/body are passed as argv items (via "on run argv"), not
		// interpolated into the script text, so no manual AppleScript
		// string-quoting is needed or risked.
		cmd = exec.CommandContext(ctx, "osascript",
			"-e", "on run argv",
			"-e", "display notification (item 1 of argv) with title (item 2 of argv)",
			"-e", "end run",
			"--", body, title)
	default:
		return
	}
	if err := cmd.Run(); err != nil {
		log.Printf("notify: %v", err)
	}
}
