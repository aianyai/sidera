//go:build windows

package fsx_test

import (
	"fmt"
	"io/fs"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// The codes Windows gives for a full volume, for an operation by path and for
// one on a handle, are recognised, and the errors a file held by another process
// gives are not. The codes come from the Windows system error list; the test is
// pending verification by CI on main.
func TestIsNoSpaceRecognisesTheWindowsCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"disk full", windows.ERROR_DISK_FULL, true},
		{"handle disk full", windows.ERROR_HANDLE_DISK_FULL, true},
		{"storage quota exceeded", windows.ERROR_DISK_QUOTA_EXCEEDED, true},
		{"disk full in a path error", &fs.PathError{Op: "write", Path: "x", Err: windows.ERROR_DISK_FULL}, true},
		{"handle disk full wrapped", fmt.Errorf("writing: %w", windows.ERROR_HANDLE_DISK_FULL), true},
		{"the errno Go invents for ENOSPC, which Windows never returns", syscall.ENOSPC, false},
		{"access denied", windows.ERROR_ACCESS_DENIED, false},
		{"sharing violation", windows.ERROR_SHARING_VIOLATION, false},
		{"file not found", windows.ERROR_FILE_NOT_FOUND, false},
	} {
		if got := fsx.IsNoSpace(tc.err); got != tc.want {
			t.Errorf("%s: IsNoSpace = %v, want %v", tc.name, got, tc.want)
		}
	}
}
