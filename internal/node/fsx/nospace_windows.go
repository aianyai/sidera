//go:build windows

package fsx

import (
	"errors"

	"golang.org/x/sys/windows"
)

// The codes Windows gives for a full volume: for an operation by path, for one on
// an open handle, and for a storage quota that was exceeded. syscall.ENOSPC is
// not an error Windows returns (Go invents its value, and Errno.Is does not map
// any of these codes to it), so it is not recognised.
func isNoSpace(err error) bool {
	return errors.Is(err, windows.ERROR_DISK_FULL) ||
		errors.Is(err, windows.ERROR_HANDLE_DISK_FULL) ||
		errors.Is(err, windows.ERROR_DISK_QUOTA_EXCEEDED)
}
