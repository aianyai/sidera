package ipc

import (
	"errors"
	"net"
)

// Listen starts serving the interface at addr and returns the listener.
//
// lock must show that the caller holds the data directory's instance lock:
// on Unix a leftover socket is deleted before listening, and that is only safe
// for the one process that knows no other daemon serves the directory. A nil
// or released lock fails with ErrLockNotHeld on every platform.
//
// On Unix the socket directory must already exist, be owned by the user and
// have mode 0700, or Listen fails with a *PeerCheckError; the socket is made
// 0600 and every accepted connection is checked to come from the user. On
// Windows the pipe is created as the first instance of its name, so a second
// listener on the name fails, and only the user may open it.
func Listen(addr Address, lock InstanceLock, opts Options) (net.Listener, error) {
	if addr.name == "" {
		return nil, errors.New("ipc: Listen needs an address made by Locate")
	}
	if lock == nil || !lock.Held() {
		return nil, ErrLockNotHeld
	}
	return listen(addr, opts)
}
