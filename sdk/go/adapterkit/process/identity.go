package process

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrStartedUnidentified is wrapped by the error of a Start whose process runs
	// but whose identity could not be read. Started.Kill, Done and PID are still
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
// finds one with the same StartKey. Errors are Lookup's, with one exception on
// Windows: a process that cannot be opened (a SYSTEM or protected process that
// holds a PID the identity once had, after a reboot for instance) but whose start
// the system reports without opening it is told apart by that start. A different
// start is not this identity, so the answer is false; the same start leaves the
// error, because the process cannot be shown to be running.
func Running(id Identity) (bool, error) {
	p, found, err := Lookup(id.PID)
	return named(id, p, found, err)
}

// named decides, from the answer of Lookup, whether the process it found is the
// one id names.
func named(id Identity, p Process, found bool, err error) (bool, error) {
	var d *deniedError
	if errors.As(err, &d) && d.startKey != id.StartKey {
		return false, nil
	}
	if err != nil || !found {
		return false, err
	}
	return p.StartKey == id.StartKey, nil
}

// deniedError is the error of a lookup of a process that exists but may not be
// opened and whose StartKey the platform reports without opening it (Windows). A
// refused process whose start cannot be read that way is a plain error.
type deniedError struct {
	pid      int
	startKey string // of the refused process
	err      error
}

func (e *deniedError) Error() string {
	return fmt.Sprintf("process: look up process %d: %v", e.pid, e.err)
}
func (e *deniedError) Unwrap() error { return e.err }

// List returns the running processes of the adapter's user (the effective uid on
// Unix, the token's user SID on Windows) with their arguments, in one pass. It is a
// listing for finding what the adapter can recognise by its arguments, not a proof
// of what is not running: a process that is not in it may still run. Left out are
// a process of another user, one whose details are denied and one that ends during
// the pass; on Windows the denied ones are the processes of a higher integrity
// level and protected processes, whose owner cannot be read without opening them,
// and they are left out whoever owns them. To know whether a process the adapter
// knows by identity still runs, ask Running. Any other failure is an error.
func List() ([]Process, error) { return list() }
