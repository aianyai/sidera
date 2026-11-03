//go:build windows

package fsx_test

import (
	"fmt"
	"io/fs"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// A file a running program is mapped from, or that another process keeps open
// without sharing, fails with access denied or a sharing violation; the other
// errors, file not found included, are not "held".
func TestIsHeldRecognisesAccessDeniedAndSharingViolation(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"access denied", windows.ERROR_ACCESS_DENIED, true},
		{"sharing violation", windows.ERROR_SHARING_VIOLATION, true},
		{"sharing violation in a path error", &fs.PathError{Op: "remove", Path: "x", Err: windows.ERROR_SHARING_VIOLATION}, true},
		{"access denied wrapped", fmt.Errorf("removing: %w", windows.ERROR_ACCESS_DENIED), true},
		{"file not found", windows.ERROR_FILE_NOT_FOUND, false},
		{"path not found", windows.ERROR_PATH_NOT_FOUND, false},
		{"disk full", windows.ERROR_DISK_FULL, false},
		{"nil", nil, false},
	} {
		if got := fsx.IsHeld(tc.err); got != tc.want {
			t.Errorf("%s: IsHeld = %v, want %v", tc.name, got, tc.want)
		}
	}
}
