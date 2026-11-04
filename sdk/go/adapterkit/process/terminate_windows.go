package process

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// terminate ends the process on the handle that proved its identity: the creation
// time is read from the handle and TerminateProcess is called on it, so the PID
// cannot name another process between the check and the end. There is no polite
// step: a process started detached has no console to ask, and no other process is
// a safe recipient of a close request.
func terminate(_ context.Context, id Identity, _ time.Duration) error {
	h, err := windows.OpenProcess(openAccess|windows.PROCESS_TERMINATE, false, uint32(id.PID))
	switch classifyOpen(err) {
	case absent:
		return ErrNotRunning
	case refused, failed:
		// Not a process to end by this identity unless the table shows it is the
		// one; and one that is, but may not be ended, is an error and not "gone".
		running, rerr := Running(id)
		switch {
		case rerr != nil:
			return rerr
		case !running:
			return ErrNotRunning
		}
		return fmt.Errorf("process: open process %d to end it: %w", id.PID, err)
	}
	defer windows.CloseHandle(h)

	alive, err := running(h)
	if err != nil {
		return fmt.Errorf("process: look up process %d: %w", id.PID, err)
	}
	if !alive {
		return ErrNotRunning
	}
	p, err := describe(h, id.PID)
	if err != nil {
		return err
	}
	if p.StartKey != id.StartKey {
		return ErrNotRunning
	}
	if err := windows.TerminateProcess(h, 1); err != nil {
		return fmt.Errorf("process: end process %d: %w", id.PID, err)
	}
	switch ev, err := windows.WaitForSingleObject(h, uint32(killWait.Milliseconds())); {
	case err != nil:
		return fmt.Errorf("process: wait for process %d to end: %w", id.PID, err)
	case ev == uint32(windows.WAIT_TIMEOUT):
		return fmt.Errorf("process: process %d still runs %v after it was ended", id.PID, killWait)
	}
	return nil
}
