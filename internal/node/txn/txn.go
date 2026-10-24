package txn

import (
	"errors"
	"fmt"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// Kind says on which path a transaction runs.
type Kind uint8

const (
	// Normal transactions run in normal operation, startup steps that load and
	// start the node included.
	Normal Kind = iota + 1
	// Recovery transactions run only to repair what a crash left: replay,
	// quarantine, orphan collection, cleanup of temporary files. A run with no
	// crash and no damage reaches none of their points.
	Recovery
)

// Op is the durable operation of a write. There is one per composite operation
// of fsx, and each has a fixed sequence of steps, which the keys of the table
// are expanded from.
type Op uint8

const (
	OpReplace      Op = iota + 1 // fsx.Replace: create, write, sync, rename, sync-dir
	OpReplaceDefer               // fsx.ReplaceDeferred: create, write, sync, rename; the directory sync is a later SyncDir
	OpAppend                     // fsx.OpenAppend once per path, then fsx.Append per call: open, write
	OpSyncFile                   // fsx.SyncFile of a path appended in this transaction: sync, sync-dir
	OpRenameFile                 // fsx.RenameFile: rename, sync-dir
	OpRemove                     // fsx.Remove: remove
	OpSyncDir                    // fsx.SyncDir: sync-dir
	OpMakeDir                    // fsx.MakeDir: mkdir, sync-dir
	OpRenameDir                  // fsx.RenameDir: rename-dir, sync-dir
	OpRemoveAll                  // fsx.RemoveAll: remove-all
	OpDiscard                    // fsx.Discard: rename-dir, sync-dir, remove-all
)

// Write declares one write of a transaction. Writes are declared in the order
// they are made.
type Write struct {
	// Role names the write inside the transaction; it is the role of the tag
	// every step of the write carries. Roles are unique in a declaration, except
	// that a SyncFile has the role of the Append whose path it syncs: the steps
	// of an appended file are attributed to the role it was opened under.
	Role string
	Op   Op
	// Many: the write may be made several times in a row, for example one file
	// of a role per call.
	Many bool
	// Optional: the write may not be made at all.
	Optional bool
	// Prepare: what is left of this write after a failure or a crash is garbage
	// that recovery removes, so it does not count towards the commit point.
	Prepare bool
}

// Decl is the static declaration of one transaction. It is a literal, a
// package-level value of the package that owns the files it writes; nothing
// registers it at run time.
type Decl struct {
	Name   string
	Kind   Kind
	Writes []Write
}

// Key is a crash point without its ordinal, with the kind of its transaction:
// the unit of the coverage assertion.
type Key struct {
	fsx.Key
	Kind Kind
}

// Count is how many times a key was reached.
type Count struct {
	Key  Key
	Hits int
}

// Observer is told about every point before its step runs, from the goroutine
// that is about to run it; transactions of different declarations may run at
// once, so it must be safe for concurrent use. It must not write through the
// Writer. The commit point of a transaction is reported after its last step
// has completed and before Commit returns: nothing has been acknowledged yet.
type Observer interface {
	Reached(p fsx.Point, kind Kind)
}

// Error is what a failed write of a Tx returns. Its Class is the one to act
// on: it is the class the file system layer gave, raised to Fatal when the
// commit point had been reached. Err is the system error, so errors.Is finds
// ENOSPC and the like.
type Error struct {
	Tx    string    // the transaction
	Point fsx.Point // where it failed
	Path  string    // the path the failed operation worked on
	Class fsx.Class
	Err   error
}

func (e *Error) Error() string {
	return fmt.Sprintf("txn %s: %v %s (%s): %v", e.Tx, e.Point, e.Path, e.Class, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// IsFatal reports whether the process must exit and rescan: the failure was
// fatal in the file system layer, or came after the commit point of its
// transaction. It accepts an error of either layer, so a caller needs only
// this one function.
func IsFatal(err error) bool {
	var te *Error
	if errors.As(err, &te) {
		return te.Class == fsx.Fatal
	}
	return fsx.IsFatal(err)
}
