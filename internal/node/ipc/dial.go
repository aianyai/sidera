package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
)

// Dial connects to the interface at addr, after checking that the way to it and
// the process serving it belong to the current user.
//
// It fails with ErrDaemonNotRunning when nothing listens at the address, with
// a *PeerCheckError (matching ErrPeerCheckFailed) when any check fails, and in
// that case nothing has been sent. The caller supplies the deadline in ctx: on
// Windows a busy pipe is retried until ctx ends.
func Dial(ctx context.Context, addr Address, opts Options) (net.Conn, error) {
	if addr.name == "" {
		return nil, errors.New("ipc: Dial needs an address made by Locate")
	}
	return dial(ctx, addr, opts)
}

// accessDenied is the failed check for an open or connect the system refused.
// The user's own socket and pipe always grant the user access, so a refusal
// means someone else holds the name, which is not the same as nobody serving it.
func accessDenied(addr Address, cause error) error {
	return &PeerCheckError{Failure: FailAccessDenied, Path: addr.name,
		Detail: "the system refused the user access to the endpoint, which the user's own endpoint always grants", Err: cause}
}

func notRunning(addr Address, cause error) error {
	return fmt.Errorf("%w: %s (%s): %w", ErrDaemonNotRunning, addr.name, runtime.GOOS, cause)
}
