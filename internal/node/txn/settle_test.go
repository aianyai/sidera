package txn_test

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

// unsyncedChange is a change that a process crash leaves visible and a power
// loss may take back, because the call that made it never synced the directory.
type unsyncedChange struct {
	name string
	// before builds the durable disk the change starts from.
	before func(t *testing.T, mem *fsxtest.Mem)
	// change makes the change with the file system primitives, which sync
	// nothing.
	change func(t *testing.T, mem *fsxtest.Mem)
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

var unsyncedChanges = []unsyncedChange{
	{
		name: "file-renamed-in-the-root",
		before: func(t *testing.T, mem *fsxtest.Mem) {
			put(t, mem, map[string]string{"old": "content"})
		},
		change: func(t *testing.T, mem *fsxtest.Mem) { mustDo(t, mem.Rename(setupTag, "old", "new")) },
	},
	{
		name: "file-renamed-two-levels-down",
		before: func(t *testing.T, mem *fsxtest.Mem) {
			mkdirs(t, mem, "a", "a/b")
			put(t, mem, map[string]string{"a/b/old": "content"})
		},
		change: func(t *testing.T, mem *fsxtest.Mem) { mustDo(t, mem.Rename(setupTag, "a/b/old", "a/b/new")) },
	},
	{
		name: "file-removed",
		before: func(t *testing.T, mem *fsxtest.Mem) {
			mkdirs(t, mem, "a")
			put(t, mem, map[string]string{"a/gone": "content", "a/kept": "kept"})
		},
		change: func(t *testing.T, mem *fsxtest.Mem) { mustDo(t, mem.Remove(setupTag, "a/gone")) },
	},
	{
		name: "directory-renamed",
		before: func(t *testing.T, mem *fsxtest.Mem) {
			mkdirs(t, mem, "a", "a/old")
			put(t, mem, map[string]string{"a/old/f": "content"})
		},
		change: func(t *testing.T, mem *fsxtest.Mem) { mustDo(t, mem.RenameDir(setupTag, "a/old", "a/new")) },
	},
	{
		name: "directory-removed",
		before: func(t *testing.T, mem *fsxtest.Mem) {
			mkdirs(t, mem, "a", "a/gone")
			put(t, mem, map[string]string{"a/gone/f": "content"})
		},
		change: func(t *testing.T, mem *fsxtest.Mem) { mustDo(t, mem.RemoveAll(setupTag, "a/gone")) },
	},
	{
		name: "changes-in-several-directories",
		before: func(t *testing.T, mem *fsxtest.Mem) {
			mkdirs(t, mem, "a", "a/b", "c")
			put(t, mem, map[string]string{"x": "1", "a/y": "2", "a/b/z": "3", "c/w": "4"})
		},
		change: func(t *testing.T, mem *fsxtest.Mem) {
			mustDo(t, mem.Rename(setupTag, "x", "x2"))
			mustDo(t, mem.Remove(setupTag, "a/y"))
			mustDo(t, mem.Rename(setupTag, "a/b/z", "a/b/z2"))
			mustDo(t, mem.RemoveAll(setupTag, "c"))
		},
	},
}

// crashedDisk returns the disk a process crash leaves after the change, and
// the tree that is visible on it.
func (c unsyncedChange) crashedDisk(t *testing.T) (*fsxtest.Mem, map[string]string) {
	t.Helper()
	mem := fsxtest.NewMem()
	c.before(t, mem)
	c.change(t, mem)
	want := snapshot(t, mem)
	crashed := mem.Clone()
	// The change must be at risk, or the test shows nothing.
	if reflect.DeepEqual(snapshot(t, crashed.CrashCloneWorst()), want) {
		t.Fatal("the change is durable without any sync: the test shows nothing")
	}
	return crashed, want
}

// powerLosses returns the worst power loss and seeded ones of a disk.
func powerLosses(mem *fsxtest.Mem, seeds int) map[string]*fsxtest.Mem {
	out := map[string]*fsxtest.Mem{"worst": mem.CrashCloneWorst()}
	for seed := 1; seed <= seeds; seed++ {
		rng := rand.New(rand.NewPCG(uint64(seed), 0x5e771e))
		out[fmt.Sprintf("seed-%d", seed)] = mem.CrashClone(rng)
	}
	return out
}

func restartOn(t testing.TB, mem *fsxtest.Mem) *txn.Writer {
	return restartObserved(t, mem, nil)
}

func restartObserved(t testing.TB, mem *fsxtest.Mem, obs txn.Observer) *txn.Writer {
	t.Helper()
	return txn.New(mem, table(t), obs)
}

// After a process crash a rename or a removal can be visible and not durable.
// The recovery of every owner reads existence and acts on it durably, so a
// power loss after it could restore the change it acted on: a consequence that
// outlives its cause. Recover therefore makes what is visible durable before it
// returns, and a power loss afterwards keeps the tree the process crash left.
func TestPowerLossCloneAfterRecoverKeepsWhatAProcessCrashLeftVisible(t *testing.T) {
	for _, c := range unsyncedChanges {
		t.Run(c.name, func(t *testing.T) {
			crashed, want := c.crashedDisk(t)
			if err := restartOn(t, crashed).Recover(); err != nil {
				t.Fatalf("Recover: %v", err)
			}
			if got := snapshot(t, crashed); !reflect.DeepEqual(got, want) {
				t.Errorf("Recover changed the visible tree:\n got %v\nwant %v", got, want)
			}
			for name, lost := range powerLosses(crashed, 16) {
				if got := snapshot(t, lost); !reflect.DeepEqual(got, want) {
					t.Errorf("power loss %s after Recover:\n got %v\nwant %v", name, got, want)
				}
			}
		})
	}
}

// A crash during the settle transaction itself leaves a disk on which Recover
// runs again and ends with everything durable, whatever the fate of the
// directories the first run had not yet synced.
func TestPowerLossCloneAfterACrashDuringTheSettle(t *testing.T) {
	c := unsyncedChanges[len(unsyncedChanges)-1]
	crashed, want := c.crashedDisk(t)

	rec := &recorder{}
	stops := 0
	rec.on = func(p fsx.Point, _ txn.Kind) {
		if !strings.HasPrefix(p.String(), "txn-settle/") {
			return
		}
		stops++
		disks := map[string]*fsxtest.Mem{"process-crash": crashed.Clone()}
		for name, d := range powerLosses(crashed, 3) {
			disks[name] = d
		}
		for name, disk := range disks {
			where := fmt.Sprintf("before %v, %s", p, name)
			if err := restartOn(t, disk).Recover(); err != nil {
				t.Errorf("%s: the second Recover: %v", where, err)
				continue
			}
			after := snapshot(t, disk)
			if name == "process-crash" && !reflect.DeepEqual(after, want) {
				t.Errorf("%s: the tree after the second Recover:\n got %v\nwant %v", where, after, want)
			}
			if worst := snapshot(t, disk.CrashCloneWorst()); !reflect.DeepEqual(worst, after) {
				t.Errorf("%s: after the second Recover a power loss changes the tree:\n got %v\nwant %v", where, worst, after)
			}
		}
	}
	if err := restartObserved(t, crashed, rec).Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if stops == 0 {
		t.Fatal("Recover reached no step of the settle transaction: nothing was injected")
	}
}

// syncLog records the directories that the settle transaction syncs.
type syncLog struct {
	fsx.FS
	dirs *[]string
}

func (l syncLog) SyncDir(tag fsx.Tag, dir string) error {
	if tag.Tx == "txn-settle" {
		*l.dirs = append(*l.dirs, dir)
	}
	return l.FS.SyncDir(tag, dir)
}

// The settle transaction syncs every directory under the data directory,
// parents before their children, names sorted, once each. A directory that the
// cleanup removed is not synced.
func TestRecoverSettlesEveryDirectoryInAStableOrder(t *testing.T) {
	mem := fsxtest.NewMem()
	mkdirs(t, mem, "b", "a", "a/z", "a/m", "c", "c/.trash-1", "c/.trash-1/inner")
	put(t, mem, map[string]string{"a/.tmp-1": "x"})
	var dirs []string
	w := txn.New(syncLog{mem, &dirs}, table(t), nil)
	if err := w.Recover(); err != nil {
		t.Fatal(err)
	}
	want := []string{".", "a", "a/m", "a/z", "b", "c"}
	if !reflect.DeepEqual(dirs, want) {
		t.Errorf("settled directories = %v, want %v", dirs, want)
	}
}
