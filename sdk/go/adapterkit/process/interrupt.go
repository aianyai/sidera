package process

import "context"

// Interrupt asks the process id names to stop the way Ctrl-C in its terminal would.
// On Unix it sends SIGINT to the process alone. On Windows a short-lived helper (see
// HandleHelperInvocation) sends CTRL_C_EVENT to every process on its console; this
// works only for a process started with Spec.Interruptible. It returns once the
// request is delivered, not when the process stops: watch Done or Running for that.
// ErrNotRunning when id names no running process. Sending twice is harmless. ctx
// bounds the helper. Safe for concurrent use.
func Interrupt(ctx context.Context, id Identity) error {
	running, err := Running(id)
	if err != nil {
		return err
	}
	if !running {
		return ErrNotRunning
	}
	return interrupt(ctx, id)
}
