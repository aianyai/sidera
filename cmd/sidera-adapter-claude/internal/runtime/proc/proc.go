package proc

import (
	"errors"
	"fmt"
)

// ErrUnsupported is wrapped by every error of an operation this platform does
// not implement yet. The adapter fails loudly with it: observing processes
// wrongly would end in a second writer on a session.
var ErrUnsupported = errors.New("proc: not supported on this platform")

// ErrStartedUnidentified is wrapped by the error of a Start whose process did
// start but whose identity could not be read. Such a start is not a failure: the
// process runs, so what the caller does about it must not assume that nothing
// happened.
var ErrStartedUnidentified = errors.New("proc: the process started but its identity cannot be read")

// Info is what the process table says about one process.
type Info struct {
	PID int
	// Alive: the process exists and is not a zombie, a process that has ended
	// and not been reaped yet. A PID that no process holds is Alive false.
	Alive bool
	// StartUnix and StartMicro are the process start, as seconds and the
	// microseconds past them; zero when the process does not exist. Together
	// with the PID they name one process, since PIDs are reused.
	StartUnix  int64
	StartMicro int64
}

// StartKey is the start as the adapter protocol's opaque start key: seconds
// and microseconds, "<sec>.<usec>", compared only for equality.
func (i Info) StartKey() string { return fmt.Sprintf("%d.%06d", i.StartUnix, i.StartMicro) }

// Entry is a process of the table with its argument vector, argv[0] included.
// A zombie has no readable arguments and none are given.
type Entry struct {
	Info
	Args []string
}

// Snapshot is the processes of the caller's user, all read at one moment.
type Snapshot []Entry

// Lookup returns the entry of a PID; a PID that is not in the snapshot is not
// alive, whoever holds it (a process of another user is not a turn process of
// ours).
func (s Snapshot) Lookup(pid int) Info {
	for _, e := range s {
		if e.PID == pid {
			return e.Info
		}
	}
	return Info{PID: pid}
}

// Table reads the process table.
type Table interface {
	// Lookup describes one process by PID.
	Lookup(pid int) (Info, error)
	// Snapshot lists the processes of the caller's user with their arguments. A
	// process whose arguments cannot be read is an error and not an absent
	// process: it could be a turn process, and "not running" would be a guess.
	Snapshot() (Snapshot, error)
}

// Launch describes a process to start detached from the adapter.
type Launch struct {
	// Argv is the argument vector, argv[0] the executable, which is started as
	// it is written.
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
	// PGID is the process group the process leads: a process started detached
	// leads a group of its own.
	PGID int
	// Done receives the error of waiting for the process, nil for a clean exit,
	// once, when it has ended and been reaped.
	Done <-chan error
}

// Launcher starts detached processes.
type Launcher interface {
	// Start starts the process in a session of its own, with no controlling
	// terminal, so that a signal to the adapter or its terminal does not reach
	// it and it outlives the adapter. The identity it returns is read from the
	// process table right away. Any other error means no process was started;
	// an error that wraps ErrStartedUnidentified means one was.
	Start(Launch) (Started, error)
}

// Signaller stops processes.
type Signaller interface {
	// Interrupt asks the process to end its turn the way an interactive Ctrl-C
	// does (SIGINT), which the runtime ends cleanly.
	Interrupt(pid int) error
}
