// Package fsx is the narrow file system every persistent byte of a node goes
// through, and the durable write sequences built on it.
//
// FS is system-call sized: each write method is one crash-relevant step. The
// composite operations in this package (Replace, Append, SyncFile, ...) are
// functions over any FS, so the same sequence of steps runs on the real file
// system and on fsxtest.Mem, and the crash model checks the code that ships.
// See README.md for the rules, and docs/references/storage for the reasons.
package fsx

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
)

// Tag names the write a step belongs to: the transaction and the role of the
// write inside it. Only the transaction layer creates tags; fsx does not
// interpret them, it only hands them to observers and fault injection.
type Tag struct {
	Tx   string // [a-z][a-z0-9-]{0,31}
	Role string // [a-z][a-z0-9-]{0,31}; empty only for StepCommit
}

// Transaction names reserved for writes that are not crash points. An
// observer numbers none of their steps and a fault rule matches them only when
// it names the transaction exactly.
const (
	// TxLog tags diagnostic log writes, which are lost harmlessly on a crash.
	TxLog = "log"
	// TxCleanup tags the removal of the temporary file of an operation that
	// failed. The leftover would be removed at the next start anyway, so a crash
	// before or after the removal is the same state as a crash before the step
	// that failed.
	TxCleanup = "cleanup"
)

// Reserved reports whether the tag belongs to writes that are not crash
// points.
func (t Tag) Reserved() bool { return t.Tx == TxLog || t.Tx == TxCleanup }

// Mode is the permission of a file a node creates. Only two exist; the zero
// value is the strictest.
type Mode uint8

const (
	ModePrivate     Mode = iota // 0600; the zero value
	ModePrivateExec             // 0700; adapter executables only
)

// Reader is the read side. Reads are not crash points.
type Reader interface {
	// ReadFile returns the whole file; the handle is closed before it returns.
	ReadFile(path string) ([]byte, error)
	// ReadDir lists a directory sorted by name.
	ReadDir(dir string) ([]Entry, error)
	// Stat does not follow a final symbolic link.
	Stat(path string) (Info, error)
	// Available is the number of bytes available to this user on the volume of
	// dir.
	Available(dir string) (uint64, error)
}

// Entry is one name in a directory.
type Entry struct {
	Name  string
	IsDir bool
}

// Info describes one file or directory.
type Info struct {
	Name  string
	Size  int64
	IsDir bool
	// OwnerOnly is true when only the owner can reach the file. Unix: owned by
	// the effective user with no group or other permission bit. Windows, which
	// fails closed: the owner is the user running the process, Administrators or
	// SYSTEM, and the DACL has only deny entries and allow entries for the user
	// running the process, SYSTEM and Administrators; an entry of any other type
	// or a descriptor that cannot be read makes it false.
	OwnerOnly bool
}

// FS is the narrow file system every persistent byte goes through. Paths are
// relative to the data directory root, separated by '/', and satisfy
// io/fs.ValidPath; "." is the root itself.
type FS interface {
	Reader

	// CreateTemp creates dir/.tmp-<hex> exclusively. StepCreate.
	CreateTemp(tag Tag, dir string, mode Mode) (File, error)
	// OpenAppend opens a file for writing positioned at its end, creating it
	// with ModePrivate when absent. StepOpen.
	OpenAppend(tag Tag, path string) (File, error)
	// Rename moves a file within its directory, replacing the target file.
	// StepRename.
	Rename(tag Tag, from, to string) error
	// SyncDir flushes a directory's entries. StepSyncDir.
	SyncDir(tag Tag, dir string) error
	// Remove unlinks a file. StepRemove.
	Remove(tag Tag, path string) error
	// RemoveAll removes a directory tree. StepRemoveAll.
	RemoveAll(tag Tag, dir string) error
	// Mkdir creates one owner-only directory; it is an error if it exists.
	// StepMkdir.
	Mkdir(tag Tag, dir string) error
	// RenameDir moves a directory within its parent; it is an error if the
	// target exists. StepRenameDir.
	RenameDir(tag Tag, from, to string) error

	// Lock takes an exclusive lock on a lock file, creating it when absent. It
	// is not a step: the kernel releases the lock when the process dies. A lock
	// held elsewhere gives ErrLocked at once.
	Lock(path string) (unlock func() error, err error)
}

// File is an open file. Write and Sync are steps attributed to the tag the
// file was opened with; Close is not a step.
type File interface {
	// Name is the path relative to the root.
	Name() string
	Write(p []byte) (int, error)
	Sync() error
	Close() error
}

// ErrLocked is returned by FS.Lock when another holder has the lock.
var ErrLocked = errors.New("fsx: locked by another process")

// Names that mark leftovers of interrupted operations. Startup removes every
// entry whose name starts with one of them.
const (
	TempPrefix  = ".tmp-"
	TrashPrefix = ".trash-"
)

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ValidName reports whether s is acceptable as a transaction or role name. The
// reserved names TxLog and TxCleanup are not: a transaction that took one would
// have its steps silently skipped by observers, so they are refused wherever a
// name is validated, key and point parsing and the transaction table included.
func ValidName(s string) bool {
	return nameRE.MatchString(s) && s != TxLog && s != TxCleanup
}

// CheckPath returns an error wrapping fs.ErrInvalid unless p is a valid
// relative path in the sense of io/fs.ValidPath whose elements hold no
// backslash, colon or NUL. On Windows a backslash separates elements and a colon
// names a drive or an alternate data stream, so a path holding one would reach a
// different file there than it means elsewhere; every platform refuses them, so
// that a node behaves the same everywhere.
func CheckPath(p string) error {
	if !fs.ValidPath(p) || strings.ContainsAny(p, "\\:\x00") {
		return &fs.PathError{Op: "path", Path: p, Err: fs.ErrInvalid}
	}
	return nil
}

// checkNotRoot is CheckPath for operations that cannot act on the root itself.
func checkNotRoot(p string) error {
	if err := CheckPath(p); err != nil {
		return err
	}
	if p == "." {
		return &fs.PathError{Op: "path", Path: p, Err: fs.ErrInvalid}
	}
	return nil
}

// CheckRename returns an error wrapping fs.ErrInvalid unless from and to are
// valid paths with the same parent directory, neither of them the root.
// Renames across directories are not offered: their crash behaviour differs
// between file systems.
func CheckRename(from, to string) error {
	for _, p := range []string{from, to} {
		if err := checkNotRoot(p); err != nil {
			return err
		}
	}
	if path.Dir(from) != path.Dir(to) {
		return fmt.Errorf("fsx: rename %s to %s: %w", from, to, errCrossDir)
	}
	return nil
}

var errCrossDir = fmt.Errorf("across directories is not supported: %w", fs.ErrInvalid)
