package proc

import (
	"context"

	"github.com/aianyai/sidera/sdk/go/adapterkit/process"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// ErrStartedUnidentified is wrapped by the error of a Start whose process did
// start but whose identity could not be read. Such a start is not a failure: the
// process runs, so what the caller does about it must not assume that nothing
// happened.
var ErrStartedUnidentified = process.ErrStartedUnidentified

// ErrNotRunning is wrapped by the error of an Interrupt whose identity no longer
// names a running process.
var ErrNotRunning = process.ErrNotRunning

// Identity names one process instance: its PID and the platform's start key,
// which together tell it from a later process given the same PID.
type Identity = process.Identity

// Info is a running process as the process table describes it.
type Info struct {
	Identity
	// StartUnix is the start in Unix seconds by the wall clock, for display and
	// for the creation flow's records. It is not identity: on Linux it derives
	// from the boot time, which moves when the clock is set. Identity is
	// the PID with the StartKey.
	StartUnix int64
	// NativeStart is the start in the platform's own unit as decimal text (see
	// process.Process): what the runtime's registry files hold on Linux and
	// Windows.
	NativeStart string
}

// Entry is a process of the listing with its argument vector, argv[0] included.
type Entry struct {
	Info
	Args []string
}

func infoOf(p process.Process) Info {
	return Info{Identity: p.Identity, StartUnix: p.StartTime.Unix(), NativeStart: p.NativeStart}
}

// Table reads the process table.
type Table interface {
	// Lookup describes the running process that holds a PID; found is false when
	// none does. An error means the table did not answer, never that the process
	// is gone.
	Lookup(pid int) (info Info, found bool, err error)
	// Running reports whether the identity still names a running process. It is
	// the only way to decide that a process the adapter knows still runs: a
	// process missing from List may still run. An error is an error, never "gone".
	Running(id Identity) (bool, error)
	// List lists the processes of the caller's user with their arguments, to find
	// the ones whose arguments show them to be turn processes. It is not a proof
	// that a process is not running.
	List() ([]Entry, error)
}

// Launch describes a process to start detached from the adapter.
type Launch struct {
	// Argv is the argument vector, argv[0] the executable as an absolute path,
	// which is started as it is written.
	Argv []string
	Env  []string
	// Dir is the working directory.
	Dir string
	// Stdin is a file the process reads as its standard input, so that reaching
	// its end is the same as closing the input and no pipe ties the process to
	// the adapter. Stdout and Stderr are files created (or truncated) for the
	// process's output.
	Stdin, Stdout, Stderr string
}

// Started is a process that was started.
type Started struct {
	Info
	// Container is where the process runs, as the protocol reports it.
	Container adapterproto.Container
	// Done receives the error of waiting for the process, nil for a clean exit,
	// once, when it has ended and been reaped.
	Done <-chan error
}

// Launcher starts detached processes.
type Launcher interface {
	// Start starts the process so that it outlives the adapter, with a console
	// of its own on Windows so that it can be interrupted. The identity it
	// returns is read right after the start. An error that wraps
	// ErrStartedUnidentified means a process was started; any other error means
	// none was.
	Start(Launch) (Started, error)
}

// Signaller interrupts processes.
type Signaller interface {
	// Interrupt asks the process to end its turn the way an interactive Ctrl-C
	// does, which the runtime ends cleanly. The error wraps ErrNotRunning when
	// the identity names no running process.
	Interrupt(ctx context.Context, id Identity) error
}
