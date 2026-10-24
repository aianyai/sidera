package txn_test

import (
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

// The predicate of the cleanup transaction: which debris is there to remove.
// Written out by hand for a tree that has every shape of it.
func TestCleanupWorkListsEveryTemporaryFileAndDiscardedDirectory(t *testing.T) {
	mem := fsxtest.NewMem()
	mkdirs(t, mem, "a", "a/b", "c", ".trash-1", ".trash-1/inner")
	put(t, mem, map[string]string{
		".tmp-1":              "x",
		"a/.tmp-2":            "x",
		"a/b/.tmp-3":          "x",
		"a/keep":              "x",
		".trash-1/.tmp-9":     "x", // inside a leftover directory: removed with it
		".trash-1/inner/f":    "x",
		"c/not.tmp-in-a-name": "x",
	})
	mkdirs(t, mem, "c/.trash-4")

	got, err := txn.CleanupWork(mem)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, l := range got {
		paths = append(paths, l.Path)
	}
	want := []string{".tmp-1", ".trash-1", "a/.tmp-2", "a/b/.tmp-3", "c/.trash-4"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("cleanup work = %v, want %v", paths, want)
	}
}

func TestCleanupWorkOfACleanTreeIsEmpty(t *testing.T) {
	mem := fsxtest.NewMem()
	mkdirs(t, mem, "a")
	put(t, mem, map[string]string{"a/x": "1", "y": "2"})
	got, err := txn.CleanupWork(mem)
	if err != nil || len(got) != 0 {
		t.Errorf("CleanupWork = %v, %v; want nothing", got, err)
	}
}

// Recover removes every leftover of every kind and durably, and afterwards
// its predicate is empty.
func TestRecoverRemovesAllLeftoversAndTheirPredicateIsEmptyAfterwards(t *testing.T) {
	mem := fsxtest.NewMem()
	mkdirs(t, mem, "a", "a/b", "c", ".trash-1", ".trash-1/inner", "c/.trash-4")
	put(t, mem, map[string]string{
		".tmp-1": "x", "a/.tmp-2": "x", "a/b/.tmp-3": "x", "a/keep": "keep", "y": "y",
		".trash-1/.tmp-9": "x", ".trash-1/inner/f": "x", "c/.trash-4/f": "x",
	})
	rec := &recorder{}
	w := txn.New(mem, table(t), rec)

	if err := w.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if work, err := txn.CleanupWork(mem.Clone()); err != nil || len(work) != 0 {
		t.Errorf("predicate after Recover = %v, %v; want empty", work, err)
	}
	if got, want := snapshot(t, mem), map[string]string{"a/": "", "a/b/": "", "c/": "", "a/keep": "keep", "y": "y"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tree after Recover:\n got %v\nwant %v", got, want)
	}
	// The removal is durable.
	worst := mem.CrashCloneWorst()
	if work, err := txn.CleanupWork(worst); err != nil || len(work) != 0 {
		t.Errorf("predicate after a power loss following Recover = %v, %v; want empty", work, err)
	}

	// The cleanup transaction is a recovery one: the files go before the
	// directories and the directories are synced last. The settle transaction
	// follows it, of the normal kind, so that only the cleanup counts as a
	// recovery.
	names := rec.names()
	wantPrefix := []string{
		"txn-cleanup/tmp.remove#1", "txn-cleanup/tmp.remove#2", "txn-cleanup/tmp.remove#3",
		"txn-cleanup/trash.remove-all#1", "txn-cleanup/trash.remove-all#2",
	}
	end := slices.Index(names, "txn-cleanup/commit#1")
	if end < len(wantPrefix)+1 || !reflect.DeepEqual(names[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("points = %v, want them to start with %v and have the cleanup commit after the directory syncs", names, wantPrefix)
	}
	for _, n := range names[len(wantPrefix):end] {
		if !strings.HasPrefix(n, "txn-cleanup/dir.sync-dir#") {
			t.Errorf("point %s among the directory syncs of the cleanup", n)
		}
	}
	settle := names[end+1:]
	if len(settle) < 2 || settle[len(settle)-1] != "txn-settle/commit#1" {
		t.Fatalf("points after the cleanup = %v, want the settle transaction ending in txn-settle/commit#1", settle)
	}
	for _, n := range settle[:len(settle)-1] {
		if !strings.HasPrefix(n, "txn-settle/dir.sync-dir#") {
			t.Errorf("point %s among the directory syncs of the settle", n)
		}
	}
	for i, k := range rec.kinds {
		want := txn.Recovery
		if i > end {
			want = txn.Normal
		}
		if k != want {
			t.Errorf("point %s reported as kind %d, want %d", names[i], k, want)
		}
	}
}

// With nothing to remove, Recover removes nothing and reaches no point of the
// recovery kind: a start on a clean disk is not a recovery. The only points it
// reaches are the directory syncs of the settle transaction, which is of the
// normal kind.
func TestRecoverOnACleanDiskReachesNoRecoveryPoint(t *testing.T) {
	r := newRig(t, nil)
	put(t, r.mem, map[string]string{"a/x": "1"})
	if err := r.w.Recover(); err != nil {
		t.Fatal(err)
	}
	for i, k := range r.rec.kinds {
		if k == txn.Recovery {
			t.Errorf("Recover on a clean disk reached the recovery point %s", r.rec.names()[i])
		}
	}
	for _, c := range r.w.Coverage() {
		if c.Key.Kind == txn.Recovery && c.Hits != 0 {
			t.Errorf("%v has %d hits on a clean disk", c.Key.Key, c.Hits)
		}
	}
}

// The table of a composition root that forgot Internal() has no cleanup
// transaction. That is found at the first start, on a disk with nothing to
// clean, not at the first crash.
func TestRecoverPanicsWithoutTheCleanupDeclarationEvenOnACleanDisk(t *testing.T) {
	tab, err := txn.NewTable(singleReplace)
	if err != nil {
		t.Fatal(err)
	}
	mem := fsxtest.NewMem()
	mkdirs(t, mem, "a")
	put(t, mem, map[string]string{"a/x": "1"})
	w := txn.New(mem, tab, nil)
	mustPanic(t, "txn-cleanup", func() { w.Recover() })
}

// A failure of the cleanup is classified like any other transaction's: the
// first removal changes nothing if it fails, a later failure is fatal.
func TestRecoverFailureClasses(t *testing.T) {
	for _, c := range []struct {
		name  string
		match fsxtest.Match
		class fsx.Class
	}{
		{"first-removal", fsxtest.Match{Tx: "txn-cleanup", Role: "tmp", Step: fsx.StepRemove, Hit: 1}, fsx.Failed},
		{"second-removal", fsxtest.Match{Tx: "txn-cleanup", Role: "tmp", Step: fsx.StepRemove, Hit: 2}, fsx.Fatal},
		{"directory-removal", fsxtest.Match{Tx: "txn-cleanup", Role: "trash", Step: fsx.StepRemoveAll}, fsx.Fatal},
		{"directory-sync", fsxtest.Match{Tx: "txn-cleanup", Role: "dir", Step: fsx.StepSyncDir}, fsx.Fatal},
	} {
		t.Run(c.name, func(t *testing.T) {
			var faulty *fsxtest.Faulty
			r := newRig(t, faultyOver(t, &faulty))
			put(t, r.mem, map[string]string{"a/.tmp-1": "x", "a/.tmp-2": "x"})
			mkdirs(t, r.mem, "b/.trash-1")
			faulty.Fail(c.match, syscall.EIO)
			err := r.w.Recover()
			if err == nil {
				t.Fatal("Recover returned nil after an injected failure")
			}
			if te := txnError(t, err); te.Class != c.class || te.Tx != "txn-cleanup" {
				t.Errorf("error = %+v, want class %v of txn-cleanup", te, c.class)
			}
		})
	}
}

// Recover is the same on a restart after a crash at every point of a
// transaction that leaves debris: whatever the crash left, afterwards there is
// no leftover and the data is unchanged. (The crash-state test of multi
// repeats this at every point of a larger transaction.)
func TestRecoverAfterACrashDuringAReplace(t *testing.T) {
	for _, point := range []string{"single/rec.write#1", "single/rec.sync#1", "single/rec.rename#1"} {
		t.Run(point, func(t *testing.T) {
			r := newRig(t, nil, singleReplace)
			put(t, r.mem, map[string]string{"a/rec": "old"})
			var crashed *fsxtest.Mem
			r.rec.on = func(p fsx.Point, _ txn.Kind) {
				if p.String() == point {
					crashed = r.mem.Clone()
				}
			}
			tx := r.w.Begin(singleReplace)
			if err := tx.Replace("rec", "a/rec", []byte("new"), mode); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if crashed == nil {
				t.Fatalf("point %s was not reached", point)
			}
			if len(leftovers(t, crashed)) == 0 {
				t.Fatal("the crash left no temporary file; the test shows nothing")
			}
			if err := txn.New(crashed, table(t, singleReplace), nil).Recover(); err != nil {
				t.Fatalf("Recover: %v", err)
			}
			if left := leftovers(t, crashed); len(left) != 0 {
				t.Errorf("leftovers after Recover: %v", left)
			}
			if got := snapshot(t, crashed)["a/rec"]; got != "old" {
				t.Errorf("a/rec = %q after a crash before the rename completed, want the old content", got)
			}
		})
	}
}

// On every clean state the predicate is empty: after each committed
// transaction, and after a failure the file system layer cleaned up itself.
func TestCleanupPredicateIsEmptyOnEveryCleanState(t *testing.T) {
	check := func(t *testing.T, mem *fsxtest.Mem, when string) {
		t.Helper()
		if work, err := txn.CleanupWork(mem.Clone()); err != nil || len(work) != 0 {
			t.Errorf("%s: predicate = %v, %v; want empty", when, work, err)
		}
	}
	t.Run("after-committed-transactions", func(t *testing.T) {
		r := newRig(t, nil, multi, singleReplace)
		check(t, r.mem, "before any transaction")
		for i := 0; i < 3; i++ {
			if _, err := firstError(r.runMulti()); err != nil {
				t.Fatal(err)
			}
			check(t, r.mem, "after multi")
			tx := r.w.Begin(singleReplace)
			if err := tx.Replace("rec", "a/rec", []byte{byte(i)}, mode); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			check(t, r.mem, "after a single replacement")
		}
	})
	t.Run("after-a-discard", func(t *testing.T) {
		d := &txn.Decl{Name: "drop", Kind: txn.Normal, Writes: []txn.Write{{Role: "dir", Op: txn.OpDiscard}}}
		r := newRig(t, nil, d)
		put(t, r.mem, map[string]string{"a/f": "x"})
		tx := r.w.Begin(d)
		if err := tx.Discard("dir", "a"); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		check(t, r.mem, "after Discard")
	})
	t.Run("after-a-failure-before-the-rename", func(t *testing.T) {
		var faulty *fsxtest.Faulty
		r := newRig(t, faultyOver(t, &faulty), singleReplace)
		faulty.Fail(fsxtest.Match{Tx: "single", Step: fsx.StepSync}, syscall.EIO)
		tx := r.w.Begin(singleReplace)
		defer tx.Close()
		if err := tx.Replace("rec", "a/rec", []byte("x"), mode); err == nil {
			t.Fatal("injected failure did not surface")
		}
		check(t, r.mem, "after a failed replacement")
	})
	t.Run("the-predicate-is-not-vacuous", func(t *testing.T) {
		r := newRig(t, nil, singleReplace)
		var mid *fsxtest.Mem
		r.rec.on = func(p fsx.Point, _ txn.Kind) {
			if p.String() == "single/rec.rename#1" {
				mid = r.mem.Clone()
			}
		}
		tx := r.w.Begin(singleReplace)
		tx.Replace("rec", "a/rec", []byte("x"), mode)
		tx.Commit()
		if work, err := txn.CleanupWork(mid); err != nil || len(work) == 0 {
			t.Errorf("predicate on a crash in the middle of a replacement = %v, %v; want the temporary file", work, err)
		}
	})
}
