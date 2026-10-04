package daemon

import (
	"fmt"
	"net"
	"time"

	"golang.org/x/sys/unix"
)

// peerPID is the process listening on the Unix socket at sockPath, as
// the kernel reports it for a connection to it.
func peerPID(sockPath string) (int, error) {
	conn, err := net.DialTimeout("unix", sockPath, 300*time.Millisecond)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	raw, err := conn.(*net.UnixConn).SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid int
	var serr error
	if err := raw.Control(func(fd uintptr) {
		pid, serr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return 0, err
	}
	if serr != nil {
		return 0, serr
	}
	if pid <= 0 {
		return 0, fmt.Errorf("couldn't tell which process the old daemon is")
	}
	return pid, nil
}
