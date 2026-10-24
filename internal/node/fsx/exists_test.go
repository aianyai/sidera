package fsx_test

import (
	"errors"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

func TestExists(t *testing.T) {
	mem := fsxtest.NewMem()
	mustMkdir(t, mem, "dir")
	mustWrite(t, mem, "dir/file", "x")
	faulty := fsxtest.NewFaulty(t, mem)
	rule := faulty.Fail(fsxtest.Match{Path: "dir/unreadable"}, syscall.EIO)

	for _, c := range []struct {
		name string
		path string
		want bool
	}{
		{"a file", "dir/file", true},
		{"a directory", "dir", true},
		{"the root", ".", true},
		{"a name that is not there", "dir/missing", false},
		{"a name under a directory that is not there", "nodir/file", false},
	} {
		got, err := fsx.Exists(faulty, c.path)
		if err != nil || got != c.want {
			t.Errorf("%s: Exists(%q) = %v, %v; want %v, nil", c.name, c.path, got, err, c.want)
		}
	}

	got, err := fsx.Exists(faulty, "dir/unreadable")
	if got || !errors.Is(err, syscall.EIO) {
		t.Errorf("a failed Stat: Exists = %v, %v; want false and the failure, never a guess", got, err)
	}
	if rule.Hits() == 0 {
		t.Error("the failing Stat was not reached")
	}
}
