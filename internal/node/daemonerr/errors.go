// Package daemonerr declares the errors shared by the daemon and its wire mapper.
// It has no lifecycle operations or wire policy. See README.md.
package daemonerr

import "errors"

// The ways a start or an init is refused. Each is wrapped with what is wrong and
// where, so a caller matches with errors.Is and shows the text.
var (
	// ErrAlreadyRunning: another daemon, or an init, holds the instance lock of the
	// data directory.
	ErrAlreadyRunning = errors.New("daemon: a daemon is already running on this data directory")
	// ErrAlreadyInitialized: init finished on this data directory; it is not made
	// again and nothing is changed.
	ErrAlreadyInitialized = errors.New("daemon: this data directory is already initialized")
	// ErrNotInitialized: there is no data directory that init finished; run init.
	ErrNotInitialized = errors.New("daemon: this data directory is not initialized; run `sidera init`")
	// ErrPermissions: the data directory is not a directory only its owner can
	// reach. Nothing is narrowed: the owner decides what becomes of it.
	ErrPermissions = errors.New("daemon: the data directory is not private to its owner")
	// ErrEngineNotStopped: the engine did not end within the grace of the shutdown,
	// so it may still be writing, and the instance lock stays held until the
	// process exits. Whoever gets this ends the process.
	ErrEngineNotStopped = errors.New("daemon: the engine did not stop in time; the instance lock is held until the process exits")
)

// FatalError is how a daemon that ended because of a fatal failure of the disk
// reports it.
type FatalError struct{ Err error }

func (e *FatalError) Error() string {
	return "daemon: stopped by a failure of the disk: " + e.Err.Error()
}
func (e *FatalError) Unwrap() error { return e.Err }
