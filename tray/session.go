package tray

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNoSession is returned when there is no desktop session to put an
// icon in — over SSH, on a server, in a container. Without this check
// the tray simply blocks forever waiting for a tray host that will
// never answer, which looks like a hang rather than an explanation.
var ErrNoSession = errors.New("no desktop session found to show a tray icon in")

func noSession(detail string) error {
	return fmt.Errorf("%w (%s)", ErrNoSession, detail)
}

// aquaManager is what "launchctl managername" prints for a macOS login
// that has a desktop: Aqua is the window server's domain. A shell over
// SSH reports "Background" and a LaunchDaemon "System", and neither has
// a menu bar to put an icon in.
const aquaManager = "Aqua"

// isAquaSession reads that output. It lives here rather than in the
// darwin file so it is compiled and tested everywhere: the string
// matching is the part most likely to be wrong, and it cannot be
// exercised on a machine that has launchctl to run.
func isAquaSession(managerName string) bool {
	return strings.TrimSpace(managerName) == aquaManager
}
