package daemon

import (
	"fmt"
	"net"
	"syscall"
	"time"
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
	var cred *syscall.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		cred, serr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if serr != nil {
		return 0, serr
	}
	if cred.Pid <= 0 {
		return 0, fmt.Errorf("couldn't tell which process the old daemon is")
	}
	return int(cred.Pid), nil
}
