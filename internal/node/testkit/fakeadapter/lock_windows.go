//go:build windows

package fakeadapter

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// errorSharingViolation is ERROR_SHARING_VIOLATION (32): another handle holds the file
// open without sharing. The standard library's syscall package does not name it.
const errorSharingViolation syscall.Errno = 32

// lockInstance takes an exclusive lock on the file at path, creating it: the file
// is opened with a share mode of 0, so no other open of it succeeds while this
// handle is open, and the handle dies with the process. How Windows reports a
// second open from another process is pending verification by CI on main.
func lockInstance(path string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, errorSharingViolation) {
			return nil, errLocked
		}
		return nil, err
	}
	return func() { _ = syscall.CloseHandle(h) }, nil
}
