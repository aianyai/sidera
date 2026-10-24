package ipc

import (
	"context"
	"errors"
	"net"
)

var errNotImplemented = errors.New("ipc: not implemented")

// Locate derives where the interface of the data directory lives.
func Locate(home string, kind Kind) (Address, error) { return Address{}, errNotImplemented }

// Listen starts serving the interface.
func Listen(addr Address, lock InstanceLock, opts Options) (net.Listener, error) {
	return nil, errNotImplemented
}

// Dial connects to the interface after checking who is at the other end.
func Dial(ctx context.Context, addr Address, opts Options) (net.Conn, error) {
	return nil, errNotImplemented
}
