package tray

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"time"

	"godl/internal/paths"
)

// ErrAlreadyRunning means another godl tray already holds the icon.
// Two icons for one daemon is worse than none, and there are now two
// ways to get one — the daemon starting it, and the user running "godl
// tray" — so whoever is second stands down.
var ErrAlreadyRunning = errors.New("a godl tray is already running")

// claimInstance takes the single-instance lock, returning a release
// func. The lock is a listening socket rather than a pid file: binding
// is atomic and the kernel drops it when the process dies, so a killed
// tray can't leave a lock behind that blocks the next one forever.
func claimInstance() (release func(), err error) {
	path, err := instancePath()
	if err != nil {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		// Either a live tray holds it, or a dead one left the file
		// behind. Connecting is what tells those apart.
		if conn, derr := net.DialTimeout("unix", path, 300*time.Millisecond); derr == nil {
			conn.Close()
			return nil, ErrAlreadyRunning
		}
		os.Remove(path)
		if l, err = net.Listen("unix", path); err != nil {
			return nil, err
		}
	}
	return func() { l.Close(); os.Remove(path) }, nil
}

// instancePath puts the lock beside the daemon's own socket, so the two
// share a lifetime and a cleanup story, and so a per-user runtime dir
// keeps one user's tray from locking out another's.
func instancePath() (string, error) {
	sock, err := paths.SocketPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(sock), "godl-tray.sock"), nil
}
