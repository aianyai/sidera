package runtime

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Daemon is what the adapter sends to the daemon. *adapterkit.Conn is one.
type Daemon interface {
	CheckpointPut(ctx context.Context, p adapterproto.CheckpointPutParams) error
	PromptPersisted(ctx context.Context, p adapterproto.PromptPersistedParams) error
	TurnStarted(ctx context.Context, p adapterproto.TurnStartedParams) error
	TurnEnded(ctx context.Context, p adapterproto.TurnEndedParams) error
	Availability(ctx context.Context, p adapterproto.AvailabilityParams) error
}

// Files is the adapter's access to the file system.
type Files interface {
	// Open opens a file for reading from the given offset. A file that is not
	// there is an error that satisfies errors.Is(err, fs.ErrNotExist).
	Open(path string, offset int64) (io.ReadCloser, error)
	// ListDir returns the names of the entries of a directory. A directory that is
	// not there is an error that satisfies errors.Is(err, fs.ErrNotExist).
	ListDir(path string) ([]string, error)
	// WriteFile writes a file, creating its directories, private to the user.
	// A reader never sees half of it.
	WriteFile(path string, data []byte) error
	// RealDir returns the real path of a directory as Claude Code resolves its
	// working directory (symbolic links resolved; on Windows also the on-disk
	// case and the long form of each name), and fails if it is not a directory.
	// The turn process is started with this path as its working directory, so
	// that the record directory Claude Code derives is the one the adapter does.
	RealDir(path string) (string, error)
}

// Command is a program to run to completion.
type Command struct {
	Argv    []string
	Env     []string
	Dir     string
	Timeout time.Duration
}

// CommandResult is how a Command ended. A command that ran and failed is a
// result with an exit code; an error is for one that could not be run or did not
// end in time.
type CommandResult struct {
	Stdout, Stderr []byte
	ExitCode       int
}

// Runner runs short programs: `claude --version`, `claude auth status`,
// `sidera probe`.
type Runner interface {
	Run(ctx context.Context, c Command) (CommandResult, error)
}

// ErrInboxUnreachable is wrapped by the error of an Inbox write that could not
// connect: nothing was written, so nothing was delivered.
var ErrInboxUnreachable = errors.New("the inbox socket cannot be connected")

// Inbox writes to the inbox socket of a session that someone else runs. The data
// may hold a secret, so an error of a write never carries it, nor the socket's path.
type Inbox interface {
	// Write connects to the socket, writes the data and closes the connection. An
	// error that does not wrap ErrInboxUnreachable comes from a connection that was
	// made, so whether the data arrived is unknown.
	Write(ctx context.Context, socket string, data []byte) error
}
