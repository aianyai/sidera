package process

import (
	"context"
	"time"
)

// killWait is how long Terminate waits for a process to be gone after it has been
// forced.
const killWait = 5 * time.Second

// Terminate ends the process id names, and on Unix the process group it leads, as
// Interrupt ends one for a person: politely first, then by force. It signals
// nothing unless the process table shows that id names a running process; an
// error from the table is returned as it is and never taken for "gone", so a
// process that could not be identified is left alone. ErrNotRunning when id names
// no running process.
//
// On Unix a process that leads its own process group (every process Start makes)
// is sent SIGTERM together with its group, and SIGKILL after grace if it or any
// member of its group still runs, even when the leader has already ended; a process that does not lead a group is signalled alone. A grace of zero
// or less skips the polite step. On Windows there is no polite step: the process
// is ended by TerminateProcess on the handle that checked its start, and grace is
// not used. Terminate returns once the process is gone; an error says it did not
// end in time. ctx bounds the wait for the polite step: when it is done the wait
// stops, the process is not forced, and the error is ctx's. Safe for concurrent
// use.
func Terminate(ctx context.Context, id Identity, grace time.Duration) error {
	return terminate(ctx, id, grace)
}
