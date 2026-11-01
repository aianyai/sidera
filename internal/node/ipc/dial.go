package ipc

import (
	"context"
	"errors"
	"net"
	"syscall"
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
// means that someone else holds the name or that a sandbox the process runs in
// does not let it connect, which is not the same as nobody serving it.
func accessDenied(addr Address, cause error) error {
	return &PeerCheckError{Failure: FailAccessDenied, Path: addr.name,
		Detail: "the system refused the user access to the endpoint, which the user's own endpoint always grants: another user holds the name, or a sandbox blocks the connection", Err: cause}
}

// notRunning is ErrDaemonNotRunning for a path that does not exist (cause may be
// nil) or a socket nobody accepts on.
func notRunning(addr Address, cause error) error {
	reason := "no such file"
	if errors.Is(cause, syscall.ECONNREFUSED) {
		reason = "connection refused"
	}
	return &NotRunningError{Path: addr.name, Reason: reason, Err: cause}
}
