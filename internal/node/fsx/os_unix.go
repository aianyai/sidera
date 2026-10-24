//go:build unix

package fsx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// Platform primitives for macOS and Linux. File.Sync is fsync(2), and
// F_FULLFSYNC on macOS (Go's File.Sync does that itself, falling back to fsync
// only where the file system does not support it): a plain fsync on macOS
// hands the data to the drive, which may reorder or delay it.

func readFile(abs string) ([]byte, error) { return os.ReadFile(abs) }

// openAppend opens for writing at the end, creating the file 0600. Under the
// instance lock there is one writer, so O_APPEND positioning is exact.
func openAppend(abs string) (*os.File, error) {
	return os.OpenFile(abs, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
}

// syncDir fsyncs the directory: fsync of a file does not make its directory
// entry durable.
func syncDir(abs string) error {
	d, err := os.Open(abs)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

func renameFile(from, to string) error { return os.Rename(from, to) }

// renameDir relies on the caller having checked that the target is absent;
// rename(2) itself refuses a non-empty directory target and Go refuses any
// existing directory target.
func renameDir(from, to string) error { return os.Rename(from, to) }

func removeFile(abs string) error { return os.Remove(abs) }

func removeAll(abs string) error { return os.RemoveAll(abs) }

func available(abs string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(abs, &st); err != nil {
		return 0, &fs.PathError{Op: "statfs", Path: abs, Err: err}
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// ownerOnly: owned by the effective user and no group or other permission bit.
func ownerOnly(_ string, fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == unix.Geteuid() && fi.Mode().Perm()&0o077 == 0
}

// lockFile takes flock(LOCK_EX|LOCK_NB) on the lock file. The lock belongs to
// the open file description, so it is released when the description is closed,
// including by the death of the process, and a second Lock in the same process
// conflicts like one from another process.
func lockFile(abs string) (func() error, error) {
	f, err := os.OpenFile(abs, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, &fs.PathError{Op: "flock", Path: abs, Err: err}
	}
	var once sync.Once
	var closeErr error
	return func() error {
		once.Do(func() { closeErr = f.Close() })
		return closeErr
	}, nil
}

func makeRoot(p string) error {
	if err := os.Mkdir(p, 0o700); err != nil {
		return err
	}
	return syncDir(filepath.Dir(p))
}
