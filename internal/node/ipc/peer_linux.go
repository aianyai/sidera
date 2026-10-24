package ipc

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID is the uid of the process at the other end of a connected socket.
// unix(7) on Linux warns that portable programs should not rely on socket
// permissions for security, which is why the credentials are checked as well.
func peerUID(c *net.UnixConn) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var (
		uid  uint32
		serr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if e != nil {
			serr = e
			return
		}
		uid = cred.Uid
	}); err != nil {
		return 0, err
	}
	return uid, serr
}
