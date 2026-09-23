package tray

import (
	"errors"
	"fmt"
)

// ErrNoSession is returned when there is no desktop session to put an
// icon in — over SSH, on a server, in a container. Without this check
// the tray simply blocks forever waiting for a tray host that will
// never answer, which looks like a hang rather than an explanation.
var ErrNoSession = errors.New("no desktop session found to show a tray icon in")

func noSession(detail string) error {
	return fmt.Errorf("%w (%s)", ErrNoSession, detail)
}
