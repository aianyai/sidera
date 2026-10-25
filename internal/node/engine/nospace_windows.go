//go:build windows

package engine

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isNoSpace reports whether a write failed because the volume is full. The two
// codes are the ones Windows gives for a full volume and for a full quota;
// pending verification by CI on main.
func isNoSpace(err error) bool {
	return errors.Is(err, windows.ERROR_DISK_FULL) || errors.Is(err, windows.ERROR_HANDLE_DISK_FULL)
}
