//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package fakeadapter

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// lockInstance takes an exclusive lock on the file at path, creating it. The lock
// dies with the process, which is the point: a hung old adapter and its
// replacement never control sessions at once. Standard library only: a third
// party adapter would have no other.
func lockInstance(path string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
