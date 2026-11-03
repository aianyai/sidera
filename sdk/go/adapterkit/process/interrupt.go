package process

import (
	"context"
	"errors"
)

var errMissingHelperCall = errors.New("process: Interrupt needs the adapter's main to call process.HandleHelperInvocation as its first statement")

// Interrupt asks the process id names to stop the way Ctrl-C in its terminal would.
// On Unix it sends SIGINT to the process alone. On Windows a short-lived helper (see
// HandleHelperInvocation) sends CTRL_C_EVENT to the console it attaches to, which
// every process attached to that console receives. That works for any process that
// has a console, and a process started with Spec.Interruptible has one of its own;
// for a process without a console the helper fails. It returns once the request is
// delivered, not when the process stops: watch Done or Running for that.
// ErrNotRunning when id names no running process. It fails on every platform, with
// no effect, when the adapter's main has not called HandleHelperInvocation. Sending
// twice is harmless. ctx bounds the helper. Safe for concurrent use.
func Interrupt(ctx context.Context, id Identity) error {
	if !helperHandled.Load() {
		return errMissingHelperCall
	}
	running, err := Running(id)
	if err != nil {
		return err
	}
	if !running {
		return ErrNotRunning
	}
	return interrupt(ctx, id)
}
