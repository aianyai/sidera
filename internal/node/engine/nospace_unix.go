//go:build !windows

package engine

import (
	"errors"
	"syscall"
)

// isNoSpace reports whether a write failed because the volume is full.
func isNoSpace(err error) bool { return errors.Is(err, syscall.ENOSPC) }
