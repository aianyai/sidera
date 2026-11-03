//go:build windows

package fsx

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isHeld is the part of the retried error set that says another process has the
// file: access denied and a sharing violation. retryable adds file not found to
// it.
func isHeld(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
