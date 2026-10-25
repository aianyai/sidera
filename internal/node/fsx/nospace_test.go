package fsx_test

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

// The fault fsxtest injects for a full disk is recognised on every platform, however it
// is wrapped, and an unrelated failure is not.
func TestIsNoSpaceRecognisesTheInjectedFullDisk(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"the injected fault", fsxtest.DiskFull, true},
		{"wrapped with %w", fmt.Errorf("writing: %w", fsxtest.DiskFull), true},
		{"in a path error", &fs.PathError{Op: "write", Path: "x", Err: fsxtest.DiskFull}, true},
		{"an input and output error", syscall.EIO, false},
		{"a missing file", fs.ErrNotExist, false},
		{"an error that is not a system error", errors.New("no space left"), false},
		{"nil", nil, false},
	} {
		if got := fsx.IsNoSpace(tc.err); got != tc.want {
			t.Errorf("%s: IsNoSpace = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A write that failed for want of space returns an *fsx.Error that IsNoSpace
// sees through, at every step an operation can fail at, without the class of
// the failure changing.
func TestIsNoSpaceSeesThroughTheErrorOfAFailedOperation(t *testing.T) {
	for _, step := range []fsx.Step{fsx.StepCreate, fsx.StepWrite, fsx.StepSync, fsx.StepRename} {
		t.Run(step.String(), func(t *testing.T) {
			faulty := fsxtest.NewFaulty(t, fsxtest.NewMem())
			faulty.Fail(fsxtest.Match{Tx: tx.Tx, Step: step}, fsxtest.DiskFull)
			err := fsx.Replace(faulty, tx, "rec", []byte("new"), fsx.ModePrivate)
			if err == nil || !fsx.IsNoSpace(err) {
				t.Fatalf("error = %v, want one IsNoSpace recognises", err)
			}
			if fsx.IsFatal(err) {
				t.Errorf("a failure for want of space before the commit step is fatal: %v", err)
			}
		})
	}
}
