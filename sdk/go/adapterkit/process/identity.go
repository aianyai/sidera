package process

import (
	"errors"
	"time"
)

var (
	// ErrStartedUnidentified is wrapped by the error of a Start whose process runs
	// but whose identity could not be read. Started.Handle, Done and PID are still
	// set: the caller must not act as if nothing started.
	ErrStartedUnidentified = errors.New("process: the process started but its identity cannot be read")
	// ErrNotRunning: the identity no longer names a running process.
	ErrNotRunning = errors.New("process: not running")
	// ErrUnsupported is wrapped by every error of an operation this platform does
	// not implement (process tables exist for darwin, linux and windows only).
	ErrUnsupported = errors.New("process: not supported on this platform")
)

// Identity names one process instance: its PID and the start the platform records
// for it, so that a later process given the same PID is told apart. Persist both
// fields to find the process again after the adapter or the daemon restarts.
type Identity struct {
	PID int
	// StartKey is the start as opaque text of at most 64 bytes, compared only for
	// equality. It is constant for the life of the process and kept the same across
	// versions of this package, so persisted keys stay valid. It is what
	// adapterproto.ProcessIdentity.StartKey carries.
	StartKey string
}

// Process is what the platform says about one running process.
type Process struct {
	Identity
	// StartTime is the start by the wall clock, for display, logs and comparing with
	// a start another program wrote as a time. Never use it as identity: on Linux it
	// is derived from the boot time, which moves when the clock is set.
	StartTime time.Time
	// NativeStart is the start in the platform's own unit, as decimal text: on Linux
	// the clock ticks after boot (/proc/<pid>/stat field 22); on Windows the creation
	// FILETIME (100 ns intervals since 1601-01-01 UTC); on macOS "<sec>.<usec>" after
	// the epoch. Use it to compare with a start another program recorded in that form.
	NativeStart string
	// Args is the argument vector, argv[0] included; List sets it, Lookup does not.
	Args []string
}

// Lookup describes the running process that holds pid. found is false when none
// does: no process holds it, or the one that does has ended (a zombie on Unix, a
// signalled process handle on Windows). An error means the platform did not answer,
// access denied included, and never that the process is gone: a caller that took it
// for gone could start a second writer beside one that runs.
func Lookup(pid int) (p Process, found bool, err error) {
	if pid <= 0 {
		return Process{}, false, nil
	}
	return lookup(pid, false)
}

// Running reports whether id still names a running process: a Lookup of its PID
// finds one with the same StartKey. Errors are Lookup's.
func Running(id Identity) (bool, error) {
	p, found, err := Lookup(id.PID)
	if err != nil || !found {
		return false, err
	}
	return p.StartKey == id.StartKey, nil
}

// List returns the running processes of the adapter's user (the effective uid on
// Unix, the token's user SID on Windows) with their arguments, in one pass. Left
// out: a process whose details are denied (another integrity level, or protected),
// which the adapter cannot have started, and one that ends during the pass. Any
// other failure is an error.
func List() ([]Process, error) { return list() }
