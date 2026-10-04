//go:build !linux && !darwin

package daemon

import "fmt"

// peerPID isn't available here: a daemon too old to be asked to shut
// down has to be stopped by hand.
func peerPID(sockPath string) (int, error) {
	return 0, fmt.Errorf("can't find the old daemon's process on this system")
}
