package runtime

import (
	"context"
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
	// WriteFile writes a file, creating its directories, private to the user.
	// A reader never sees half of it.
	WriteFile(path string, data []byte) error
	// RealDir returns the real path of a directory, symbolic links resolved,
	// and fails if it is not a directory.
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
