package ipc

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// xucredVersion is XUCRED_VERSION of <sys/ucred.h>, which x/sys does not export.
const xucredVersion = 0

// peerUID is the uid of the process at the other end of a connected socket.
// unix(4) on macOS: the credentials a server sees are those of the client when
// it called connect(2), and the client can read the server's in the same way;
// "this mechanism is reliable".
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
		cred, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if e != nil {
			serr = e
			return
		}
		if cred.Version != xucredVersion {
			serr = fmt.Errorf("unexpected xucred version %d, want %d", cred.Version, xucredVersion)
			return
		}
		uid = cred.Uid
	}); err != nil {
		return 0, err
	}
	return uid, serr
}
