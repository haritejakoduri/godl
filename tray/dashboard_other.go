//go:build !linux && !darwin && !windows

package tray

import "errors"

func launchTerminal(string) error {
	return errors.New("opening a terminal is not supported on this platform")
}
