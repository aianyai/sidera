//go:build unix

package fsx_test

import (
	"fmt"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// The two errors Unix gives for a full volume and for an exhausted quota.
func TestIsNoSpaceRecognisesTheUnixCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"ENOSPC", syscall.ENOSPC, true},
		{"EDQUOT", syscall.EDQUOT, true},
		{"EDQUOT wrapped", fmt.Errorf("replacing: %w", syscall.EDQUOT), true},
		{"EACCES", syscall.EACCES, false},
		{"EROFS", syscall.EROFS, false},
	} {
		if got := fsx.IsNoSpace(tc.err); got != tc.want {
			t.Errorf("%s: IsNoSpace = %v, want %v", tc.name, got, tc.want)
		}
	}
}
