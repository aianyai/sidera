package keys

import (
	"errors"
	"fmt"
)

// Failures. Every failure about a file is a *FileError that names the file, and
// wraps one of the first four; match them with errors.Is.
var (
	// ErrPermissionsTooBroad: someone other than the owner can reach the file.
	// The node never narrows the permissions itself: only the owner knows who
	// has read the file already.
	ErrPermissionsTooBroad = errors.New("permissions are too broad")
	// ErrMissing: the file is not there.
	ErrMissing = errors.New("file is missing")
	// ErrCorrupt: the file is not in the form this package writes, or what it
	// holds does not verify.
	ErrCorrupt = errors.New("file is damaged")
	// ErrInconsistent: the file is well formed but contradicts another file or
	// the identity key.
	ErrInconsistent = errors.New("file does not belong with the other keys")

	// ErrAlreadyInitialized: init has finished, and a second one is refused.
	ErrAlreadyInitialized = errors.New("already initialized")
	// ErrRandom: the injected random source failed or ran dry.
	ErrRandom = errors.New("the random source failed")
	// ErrRollbackPaused: the rollback pause is in force, so nothing is signed.
	ErrRollbackPaused = errors.New("the rollback pause is in force")
	// ErrClockRolledBack: the wall clock reads earlier than the persisted high
	// water mark by more than the clock tolerance, so nothing is signed.
	ErrClockRolledBack = errors.New("the wall clock is behind the high water mark")
	// ErrClockRegression: a write of the clock record would lower the high water
	// mark or lift the rollback pause.
	ErrClockRegression = errors.New("the clock record would go back")
	// ErrIdentityRetired: the identity of this node has been retired, so the node
	// does not run.
	ErrIdentityRetired = errors.New("the identity of this node is retired")
)

// FileError is a failure about one file of the data directory.
type FileError struct {
	// Path is the file, relative to the root of the data directory.
	Path string
	// Err says what is wrong with it.
	Err error
}

func (e *FileError) Error() string { return "keys: " + e.Path + ": " + e.Err.Error() }
func (e *FileError) Unwrap() error { return e.Err }

// fileError builds a *FileError of the kind given with a detail that tells the
// owner what to look at. The detail never holds the content of the file.
func fileError(path string, kind error, detail string, args ...any) error {
	if detail == "" {
		return &FileError{Path: path, Err: kind}
	}
	return &FileError{Path: path, Err: fmt.Errorf("%w: %s", kind, fmt.Sprintf(detail, args...))}
}
