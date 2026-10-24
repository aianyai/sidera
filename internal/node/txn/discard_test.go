package txn_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

// A directory that a process crash left renamed to a garbage name, with the
// rename visible and not durable, is removed by the next start. A power loss
// during that removal must never bring the directory back under its original
// name with only some of its files: an owner that finds the name present reads
// a whole directory, and one that finds it absent reads none. So the rename is
// made durable before the removal begins, and at every step of the cleanup,
// under every kind of crash, a second start ends with the original name either
// complete or absent and no leftover anywhere.
func TestPowerLossCloneAfterACrashDuringTheCleanupOfADiscardedDirectory(t *testing.T) {
	complete := map[string]string{
		"a/orig/":  "",
		"a/orig/1": "one",
		"a/orig/2": "two",
		"a/orig/3": "three",
	}
	mem := fsxtest.NewMem()
	mkdirs(t, mem, "a", "a/orig")
	put(t, mem, map[string]string{"a/orig/1": "one", "a/orig/2": "two", "a/orig/3": "three", "a/keep": "keep"})
	// The process died right after the rename of Discard: visible, not durable.
	mustDo(t, mem.RenameDir(setupTag, "a/orig", "a/.trash-1"))
	crashed := mem.Clone()
	if _, there := snapshot(t, crashed.CrashCloneWorst())["a/orig/"]; !there {
		t.Fatal("the rename is durable without any sync: the test shows nothing")
	}

	judge := func(where string, disk *fsxtest.Mem) {
		t.Helper()
		if err := restartOn(t, disk).Recover(); err != nil {
			t.Errorf("%s: the second Recover: %v", where, err)
			return
		}
		got := snapshot(t, disk)
		orig := map[string]string{}
		for p, c := range got {
			if strings.HasPrefix(p, "a/orig") {
				orig[p] = c
			}
		}
		if len(orig) != 0 && !reflect.DeepEqual(orig, complete) {
			t.Errorf("%s: the original name came back half-deleted: %v", where, orig)
		}
		if got["a/keep"] != "keep" {
			t.Errorf("%s: a file that was never discarded changed: %v", where, got)
		}
		if left := leftovers(t, disk); len(left) != 0 {
			t.Errorf("%s: leftovers after the second Recover: %v", where, left)
		}
		if worst := snapshot(t, disk.CrashCloneWorst()); !reflect.DeepEqual(worst, got) {
			t.Errorf("%s: after the second Recover a power loss changes the tree:\n got %v\nwant %v", where, worst, got)
		}
	}

	rec := &recorder{}
	stops := map[string]int{}
	rec.on = func(p fsx.Point, _ txn.Kind) {
		if !strings.HasPrefix(p.String(), "txn-cleanup/") {
			return
		}
		stops[p.String()]++
		disks := map[string]*fsxtest.Mem{"process-crash": crashed.Clone()}
		for name, d := range powerLosses(crashed, 6) {
			disks[name] = d
		}
		for name, disk := range disks {
			judge(fmt.Sprintf("before %v, %s", p, name), disk)
		}
	}
	if err := restartObserved(t, crashed, rec).Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	for _, want := range []string{"txn-cleanup/trash.remove-all#1", "txn-cleanup/dir.sync-dir#1", "txn-cleanup/commit#1"} {
		if stops[want] == 0 {
			t.Errorf("Recover never reached %s: nothing was injected there (reached %v)", want, stops)
		}
	}
}
