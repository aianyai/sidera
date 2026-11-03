//go:build unix

package fsx_test

import (
	"io/fs"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// A file in use can be removed on Unix, so no failure means that a process holds
// it, not even the permission error that looks like access denied.
func TestIsHeldIsNeverTrueOnUnix(t *testing.T) {
	for _, err := range []error{syscall.EACCES, syscall.EPERM, syscall.EBUSY, syscall.ETXTBSY, fs.ErrPermission, nil} {
		if fsx.IsHeld(err) {
			t.Errorf("IsHeld(%v) = true, want false", err)
		}
	}
}
