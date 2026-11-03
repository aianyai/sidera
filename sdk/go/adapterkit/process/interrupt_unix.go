//go:build darwin || linux

package process

import (
	"context"
	"errors"
	"fmt"
	"syscall"
)

// interrupt sends SIGINT to the process alone, as a terminal's Ctrl-C would to the
// foreground process. The process was checked a moment ago; one that ended since
// is not running, and a PID reused in that time is the usual risk of a signal by
// PID, too small a window to guard further.
func interrupt(_ context.Context, id Identity) error {
	err := syscall.Kill(id.PID, syscall.SIGINT)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.ESRCH):
		return ErrNotRunning
	}
	return fmt.Errorf("process: interrupt process %d: %w", id.PID, err)
}
