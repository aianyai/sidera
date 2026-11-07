package process

import (
	"errors"
	"fmt"
	"testing"

	"golang.org/x/sys/unix"
)

// A process that ends or is ending while the listing reads its arguments, or whose
// arguments are denied, is left out of the listing. Any other error is no answer
// and fails the listing: leaving it out would hide a process that may be running.
func TestUnlistable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"the PID is not found", unix.EINVAL, true},
		{"no such process", unix.ESRCH, true},
		{"exiting, its address space cannot be copied", unix.EIO, true},
		{"exiting, wrapped", fmt.Errorf("sysctl kern.procargs2: %w", unix.EIO), true},
		{"not permitted", unix.EPERM, true},
		{"access denied", unix.EACCES, true},
		{"no error", nil, false},
		{"out of memory", unix.ENOMEM, false},
		{"a malformed answer", errors.New("short procargs2 of 2 bytes"), false},
	} {
		if got := unlistable(tc.err); got != tc.want {
			t.Errorf("%s: unlistable(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}
