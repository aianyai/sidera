package fsx_test

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

// listTemporary returns the names of leftover temporary files in dir.
func listTemporary(t *testing.T, r fsx.Reader, dir string) []string {
	t.Helper()
	entries, err := r.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name, ".tmp-") {
			names = append(names, e.Name)
		}
	}
	return names
}

// A failed fsync of the temporary file happens before the rename: the target
// has not been touched and nothing was acknowledged, so the operation fails
// and the caller rejects or applies backpressure. The process must not be
// told to exit.
func TestFsyncFailureBeforeRenameFailsOperation(t *testing.T) {
	mem := fsxtest.NewMem()
	faulty := fsxtest.NewFaulty(t, mem)
	tag := fsx.Tag{Tx: "intake", Role: "out"}
	if err := fsx.Replace(mem, tag, "record", []byte("old"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}

	rule := faulty.Fail(fsxtest.Match{Tx: "intake", Role: "out", Step: fsx.StepSync, Hit: 1}, syscall.EIO)
	err := fsx.Replace(faulty, tag, "record", []byte("new"), fsx.ModePrivate)

	var fe *fsx.Error
	if !errors.As(err, &fe) {
		t.Fatalf("Replace error = %v, want *fsx.Error", err)
	}
	if fe.Class != fsx.Failed {
		t.Errorf("class = %v, want Failed", fe.Class)
	}
	if fsx.IsFatal(err) {
		t.Error("IsFatal = true for a failed fsync before the rename")
	}
	if !errors.Is(err, syscall.EIO) {
		t.Errorf("error %v does not wrap the system error EIO", err)
	}
	if fe.Point.Step != fsx.StepSync || fe.Point.Tag != tag {
		t.Errorf("failed at %v, want the sync step of %v", fe.Point.Key, tag)
	}
	if got, _ := mem.ReadFile("record"); string(got) != "old" {
		t.Errorf("target = %q, want it unchanged (%q)", got, "old")
	}
	if left := listTemporary(t, mem, "."); len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
	if rule.Hits() != 1 {
		t.Errorf("rule fired %d times, want exactly 1", rule.Hits())
	}
}

// A failed directory sync happens after the rename: whether the rename is
// durable is unknown and memory may disagree with disk, so the operation is
// fatal. The class survives wrapping on its way out of the transaction layer.
func TestDirSyncFailureAfterRenameIsFatal(t *testing.T) {
	mem := fsxtest.NewMem()
	faulty := fsxtest.NewFaulty(t, mem)
	tag := fsx.Tag{Tx: "intake", Role: "out"}

	rule := faulty.Fail(fsxtest.Match{Tx: "intake", Role: "out", Step: fsx.StepSyncDir, Hit: 1}, syscall.EIO)
	err := fsx.Replace(faulty, tag, "record", []byte("new"), fsx.ModePrivate)

	var fe *fsx.Error
	if !errors.As(err, &fe) {
		t.Fatalf("Replace error = %v, want *fsx.Error", err)
	}
	if fe.Class != fsx.Fatal {
		t.Errorf("class = %v, want Fatal", fe.Class)
	}
	if !fsx.IsFatal(err) {
		t.Error("IsFatal = false")
	}
	if wrapped := fmt.Errorf("commit: %w", err); !fsx.IsFatal(wrapped) {
		t.Error("IsFatal = false after the error was wrapped")
	}
	if !errors.Is(err, syscall.EIO) {
		t.Errorf("error %v does not wrap the system error EIO", err)
	}
	if fe.Point.Step != fsx.StepSyncDir {
		t.Errorf("failed at step %v, want sync-dir", fe.Point.Step)
	}
	// The rename did happen: the process must exit and rescan, not trust either state.
	if got, _ := mem.ReadFile("record"); string(got) != "new" {
		t.Errorf("target = %q, want the renamed content", got)
	}
	if rule.Hits() != 1 {
		t.Errorf("rule fired %d times, want exactly 1", rule.Hits())
	}
}
