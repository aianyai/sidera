//go:build windows

package swap

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isHeld reports whether a failed removal means that a process runs from the
// file: Windows refuses to delete an image that is mapped, with access denied or
// a sharing violation, the pair that fsx retries for the same reason. File not
// found is left out: the file is not there.
func isHeld(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
