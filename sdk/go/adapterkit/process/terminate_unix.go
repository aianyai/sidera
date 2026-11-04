//go:build darwin || linux

package process

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"
)

// pollEvery is how often the end of a signalled process is looked for.
const pollEvery = 25 * time.Millisecond

func terminate(ctx context.Context, id Identity, grace time.Duration) error {
	return terminateWith(ctx, id, grace, Running)
}

// terminateWith is terminate with the process table as a parameter, so that a test
// can make it fail.
//
// The identity is checked before each signal, and the window between the check
// and the signal, in which the PID could be reused, is the one Interrupt accepts.
func terminateWith(ctx context.Context, id Identity, grace time.Duration, running func(Identity) (bool, error)) error {
	if err := mustRun(id, running); err != nil {
		return err
	}
	target := id.PID
	switch pgid, err := syscall.Getpgid(id.PID); {
	case errors.Is(err, syscall.ESRCH):
		return ErrNotRunning
	case err != nil:
		return fmt.Errorf("process: find the group of process %d: %w", id.PID, err)
	case pgid == id.PID:
		target = -id.PID // the process leads a group: end the group
	}

	if grace > 0 {
		if err := signal(id, target, syscall.SIGTERM); err != nil {
			return err
		}
		ended, err := waitEnded(ctx, id, target, grace, running)
		if err != nil || ended {
			return err
		}
		// The last poll saw the process or its group still there, so the force goes to it.
	}
	if err := signal(id, target, syscall.SIGKILL); errors.Is(err, ErrNotRunning) {
		return nil
	} else if err != nil {
		return err
	}
	ended, err := waitEnded(ctx, id, target, killWait, running)
	if err == nil && !ended {
		err = fmt.Errorf("process: process %d still runs %v after SIGKILL", id.PID, killWait)
	}
	return err
}

// mustRun is nil when the table shows id running, ErrNotRunning when it shows
// that it is not, and the table's error when it did not answer.
func mustRun(id Identity, running func(Identity) (bool, error)) error {
	switch ok, err := running(id); {
	case err != nil:
		return err
	case !ok:
		return ErrNotRunning
	}
	return nil
}

func signal(id Identity, target int, sig syscall.Signal) error {
	switch err := syscall.Kill(target, sig); {
	case err == nil:
		return nil
	case errors.Is(err, syscall.ESRCH):
		return ErrNotRunning
	default:
		return fmt.Errorf("process: signal process %d: %w", id.PID, err)
	}
}

// waitEnded polls until id no longer names a running process and, when target is a
// group, the group is empty, or until limit has passed. A group outlives its
// leader while any member runs, and a member may ignore what ended the leader.
// A table error or a done ctx ends the wait with that error.
func waitEnded(ctx context.Context, id Identity, target int, limit time.Duration, running func(Identity) (bool, error)) (bool, error) {
	timeout := time.NewTimer(limit)
	defer timeout.Stop()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		ended, err := isEnded(id, target, running)
		if err != nil || ended {
			return ended, err
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timeout.C:
			// Look once more: the caller forces what this says still runs.
			return isEnded(id, target, running)
		case <-tick.C:
		}
	}
}

// isEnded is true when id is not running and, for a group target, no process is
// left in the group. While a member runs, the group ID cannot name anything else,
// so signalling the group after the leader has ended reaches only the group.
func isEnded(id Identity, target int, running func(Identity) (bool, error)) (bool, error) {
	ok, err := running(id)
	if err != nil || ok {
		return false, err
	}
	if target < 0 {
		// Signal 0 only asks whether the group has a process to signal.
		return errors.Is(syscall.Kill(target, 0), syscall.ESRCH), nil
	}
	return true, nil
}
