package fsx

import (
	"io/fs"
	"testing"

	"golang.org/x/sys/windows"
)

// The retried errors are the set the Go toolchain retries for transient
// holders of a file; anything else is a real answer.
func TestWindowsRetriesOnlyTheTransientErrorSet(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"access-denied", windows.ERROR_ACCESS_DENIED, true},
		{"sharing-violation", windows.ERROR_SHARING_VIOLATION, true},
		{"file-not-found", windows.ERROR_FILE_NOT_FOUND, true},
		{"wrapped-in-a-path-error", &fs.PathError{Op: "open", Path: "x", Err: windows.ERROR_SHARING_VIOLATION}, true},
		{"already-exists", windows.ERROR_ALREADY_EXISTS, false},
		{"path-not-found", windows.ERROR_PATH_NOT_FOUND, false},
		{"disk-full", windows.ERROR_DISK_FULL, false},
		{"nil", nil, false},
	} {
		if got := retryable(tc.err); got != tc.want {
			t.Errorf("%s: retryable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A read of a file that does not exist gets its answer at once; the other
// transient errors are retried for a read as they are for a rename.
func TestWindowsReadDoesNotRetryFileNotFound(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"file-not-found", windows.ERROR_FILE_NOT_FOUND, false},
		{"file-not-found-in-a-path-error", &fs.PathError{Op: "open", Path: "x", Err: windows.ERROR_FILE_NOT_FOUND}, false},
		{"access-denied", windows.ERROR_ACCESS_DENIED, true},
		{"sharing-violation", windows.ERROR_SHARING_VIOLATION, true},
		{"path-not-found", windows.ERROR_PATH_NOT_FOUND, false},
		{"nil", nil, false},
	} {
		if got := retryableRead(tc.err); got != tc.want {
			t.Errorf("%s: retryableRead = %v, want %v", tc.name, got, tc.want)
		}
	}
}
