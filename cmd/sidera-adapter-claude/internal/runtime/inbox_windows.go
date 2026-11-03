//go:build windows

package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// pipeRetry is how often a busy pipe is tried again: the open is repeated rather
// than waited on, so that the 3 second limit and the context apply to it.
const pipeRetry = 10 * time.Millisecond

// Write opens the inbox pipe of a session, writes the data a line at a time and
// closes it. Claude Code on Windows closes a connection whose first line is not a
// valid authentication line without delivering anything, and one that has not sent
// a complete line within 30 seconds, so the pipe is opened only now, when the
// message is ready, and the authentication line (which Wake.Lines puts first) goes
// before the message in a write of its own: a pipe that the other end closed after a
// wrong token fails the write of the message, and the failure is returned, never
// reported as a delivery.
//
// The handle is synchronous and has no deadline, so the wait for a server that does
// not read is given up on after inboxTimeout and leaves the writing goroutine
// behind until the server reads or closes the pipe.
func (osInbox) Write(ctx context.Context, pipe string, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, inboxTimeout)
	defer cancel()
	f, err := openPipe(ctx, pipe)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInboxUnreachable, withoutPipePath(err))
	}
	done := make(chan error, 1)
	go func() {
		defer f.Close()
		for _, line := range bytes.SplitAfter(data, []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			if _, err := f.Write(line); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("write to the inbox: %v", withoutPipePath(err))
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("write to the inbox: %w", ctx.Err())
	}
}

// openPipe opens the pipe for writing. A server that is busy with another client
// answers ERROR_PIPE_BUSY, and the open is tried again until ctx ends.
func openPipe(ctx context.Context, pipe string) (*os.File, error) {
	for {
		f, err := os.OpenFile(pipe, os.O_WRONLY, 0)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(pipeRetry):
		}
	}
}

// withoutPipePath is the cause of a file error without the pipe's name, which
// belongs to the session's wake.
func withoutPipePath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
