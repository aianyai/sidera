package txn_test

import (
	"errors"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

// failureCase injects one failure into a run of multi and says what class the
// caller must see and why.
type failureCase struct {
	name  string
	role  string
	step  fsx.Step
	hit   int
	class fsx.Class
	// why is the rule that decides the class, for the failure message.
	why string
}

// Every step of multi, failed in turn. Before the commit point (the rename of
// the first deferred replacement completing) the class is the one the file
// system layer gave: Failed, except the directory sync of the prepared write.
// After it everything is Fatal, whatever the file system layer said.
func TestFailureClassBeforeAndAfterTheCommitPoint(t *testing.T) {
	const beforeCommit = "before the commit point the file system layer's class stands"
	const afterCommit = "after the commit point every failure is fatal"
	const fsFatal = "a failed directory sync is fatal in the file system layer"
	cases := []failureCase{
		{"prepare-create", "body", fsx.StepCreate, 1, fsx.Failed, beforeCommit},
		{"prepare-write", "body", fsx.StepWrite, 1, fsx.Failed, beforeCommit},
		{"prepare-sync", "body", fsx.StepSync, 1, fsx.Failed, beforeCommit},
		{"prepare-rename", "body", fsx.StepRename, 1, fsx.Failed, beforeCommit},
		{"prepare-sync-dir", "body", fsx.StepSyncDir, 1, fsx.Fatal, fsFatal},
		{"first-deferred-create", "in", fsx.StepCreate, 1, fsx.Failed, beforeCommit},
		{"first-deferred-write", "in", fsx.StepWrite, 1, fsx.Failed, beforeCommit},
		{"first-deferred-sync", "in", fsx.StepSync, 1, fsx.Failed, beforeCommit},
		{"first-deferred-rename-is-the-commit-step-itself", "in", fsx.StepRename, 1, fsx.Failed, beforeCommit},
		{"second-deferred-create", "in", fsx.StepCreate, 2, fsx.Fatal, afterCommit},
		{"second-deferred-write", "in", fsx.StepWrite, 2, fsx.Fatal, afterCommit},
		{"second-deferred-sync", "in", fsx.StepSync, 2, fsx.Fatal, afterCommit},
		{"second-deferred-rename", "in", fsx.StepRename, 2, fsx.Fatal, afterCommit},
		{"deferred-group-sync-dir", "in-dir", fsx.StepSyncDir, 1, fsx.Fatal, fsFatal},
		{"replace-after-commit-create", "out", fsx.StepCreate, 1, fsx.Fatal, afterCommit},
		{"replace-after-commit-write", "out", fsx.StepWrite, 1, fsx.Fatal, afterCommit},
		{"replace-after-commit-sync", "out", fsx.StepSync, 1, fsx.Fatal, afterCommit},
		{"replace-after-commit-rename", "out", fsx.StepRename, 1, fsx.Fatal, afterCommit},
		{"replace-after-commit-sync-dir", "out", fsx.StepSyncDir, 1, fsx.Fatal, fsFatal},
		{"append-after-commit-open", "log", fsx.StepOpen, 1, fsx.Fatal, afterCommit},
		{"append-after-commit-write", "log", fsx.StepWrite, 2, fsx.Fatal, afterCommit},
		{"sync-file-sync", "log", fsx.StepSync, 1, fsx.Fatal, fsFatal},
		{"sync-file-sync-dir", "log", fsx.StepSyncDir, 1, fsx.Fatal, fsFatal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var faulty *fsxtest.Faulty
			r := newRig(t, faultyOver(t, &faulty), multi)
			rule := faulty.Fail(fsxtest.Match{Tx: "multi", Role: c.role, Step: c.step, Hit: c.hit}, syscall.EIO)

			errs := r.runMulti()

			i, err := firstError(errs)
			if i < 0 {
				t.Fatalf("the injected failure did not surface; rule hits %d", rule.Hits())
			}
			te := txnError(t, err)
			if te.Class != c.class {
				t.Errorf("class = %v, want %v (%s)", te.Class, c.class, c.why)
			}
			if txn.IsFatal(err) != (c.class == fsx.Fatal) {
				t.Errorf("IsFatal = %v, want %v (%s)", txn.IsFatal(err), c.class == fsx.Fatal, c.why)
			}
			if te.Tx != "multi" || te.Point.Tag.Role != c.role || te.Point.Step != c.step || te.Point.Hit != c.hit {
				t.Errorf("failed at %v of %q, want %s/%s.%v#%d", te.Point, te.Tx, "multi", c.role, c.step, c.hit)
			}
			if !errors.Is(err, syscall.EIO) {
				t.Errorf("error %v does not wrap the system error", err)
			}
			if rule.Hits() != 1 {
				t.Errorf("rule fired %d times, want 1", rule.Hits())
			}
		})
	}
}

// The first error is sticky: every later call, Commit included, returns that
// same error and runs no step.
func TestFirstErrorIsStickyAndNoStepRunsAfterIt(t *testing.T) {
	for _, failAt := range []struct {
		name string
		role string
		step fsx.Step
		hit  int
	}{
		{"before-the-commit-point", "in", fsx.StepSync, 1},
		{"after-the-commit-point", "out", fsx.StepWrite, 1},
		{"in-the-last-write", "log", fsx.StepSync, 1},
	} {
		t.Run(failAt.name, func(t *testing.T) {
			var faulty *fsxtest.Faulty
			r := newRig(t, faultyOver(t, &faulty), multi)
			faulty.Fail(fsxtest.Match{Tx: "multi", Role: failAt.role, Step: failAt.step, Hit: failAt.hit}, syscall.EIO)

			errs := r.runMulti()

			i, first := firstError(errs)
			if i < 0 {
				t.Fatal("the injected failure did not surface")
			}
			for j := i + 1; j < len(errs); j++ {
				if errs[j] != first {
					t.Errorf("call %d returned %v, want the first error %v", j, errs[j], first)
				}
			}
			points := r.rec.points
			if last := points[len(points)-1]; last != txnError(t, first).Point {
				t.Errorf("last point reached is %v, the failure was at %v: a step ran after the error", last, txnError(t, first).Point)
			}
			for _, p := range points {
				if p.Step == fsx.StepCommit {
					t.Errorf("a commit point was emitted for a failed transaction: %v", p)
				}
			}
		})
	}
}

// Calls after an error return it before any declaration check: a caller that
// ignores an error and goes on does not trip a panic that hides it.
func TestCallsAfterAnErrorReturnItEvenWhenOutOfOrder(t *testing.T) {
	var faulty *fsxtest.Faulty
	r := newRig(t, faultyOver(t, &faulty), multi)
	faulty.Fail(fsxtest.Match{Tx: "multi", Role: "body", Step: fsx.StepCreate}, syscall.EIO)
	tx := r.w.Begin(multi)
	defer tx.Close()
	first := tx.Replace("body", "b/body", []byte("x"), mode)
	if first == nil {
		t.Fatal("injected failure did not surface")
	}
	mustNotPanic(t, func() {
		if err := tx.Replace("no-such-role", "b/y", nil, mode); err != first {
			t.Errorf("got %v, want the first error", err)
		}
	})
}

// A Discard whose directory is already renamed away from its name has had its
// effect, so a failure of the deletion that follows is fatal here although the
// file system layer calls it an ordinary failure.
func TestFailureAfterTheCommitStepInsideOneOperationIsFatal(t *testing.T) {
	d := &txn.Decl{Name: "drop", Kind: txn.Normal, Writes: []txn.Write{{Role: "dir", Op: txn.OpDiscard}}}
	for _, c := range []struct {
		step  fsx.Step
		class fsx.Class
	}{
		{fsx.StepRenameDir, fsx.Failed},
		{fsx.StepSyncDir, fsx.Fatal},
		{fsx.StepRemoveAll, fsx.Fatal},
	} {
		t.Run(c.step.String(), func(t *testing.T) {
			var faulty *fsxtest.Faulty
			r := newRig(t, faultyOver(t, &faulty), d)
			put(t, r.mem, map[string]string{"a/f": "x"})
			faulty.Fail(fsxtest.Match{Tx: "drop", Step: c.step}, syscall.EIO)
			tx := r.w.Begin(d)
			defer tx.Close()
			err := tx.Discard("dir", "a")
			te := txnError(t, err)
			if te.Class != c.class {
				t.Errorf("class = %v, want %v", te.Class, c.class)
			}
		})
	}
}

// For every operation, as the first write that is not preparatory: its commit
// step failing leaves the class of the file system layer, and the same
// failure of a later write is fatal. A preparatory write before it does not
// make the transaction committed.
func TestEachOperationHasItsCommitStep(t *testing.T) {
	type opCase struct {
		name   string
		op     txn.Op
		extra  []txn.Write // declared right after the operation's write
		commit fsx.Step    // the step whose completion is the commit point
		early  fsx.Step    // a step before it, 0 when the commit step comes first
		setup  map[string]string
		dirs   []string
		do     func(tx *txn.Tx) error // performs the operation and what follows it in extra
	}
	ops := []opCase{
		{name: "replace", op: txn.OpReplace, commit: fsx.StepRename, early: fsx.StepSync,
			do: func(tx *txn.Tx) error { return tx.Replace("x", "a/x", []byte("1"), mode) }},
		{name: "replace-defer", op: txn.OpReplaceDefer, extra: []txn.Write{{Role: "x-dir", Op: txn.OpSyncDir}},
			commit: fsx.StepRename, early: fsx.StepSync,
			do: func(tx *txn.Tx) error {
				if err := tx.ReplaceDefer("x", "a/x", []byte("1"), mode); err != nil {
					return err
				}
				return tx.SyncDir("x-dir", "a")
			}},
		{name: "rename-file", op: txn.OpRenameFile, commit: fsx.StepRename, setup: map[string]string{"a/src": "s"},
			do: func(tx *txn.Tx) error { return tx.RenameFile("x", "a/src", "a/dst") }},
		{name: "remove", op: txn.OpRemove, commit: fsx.StepRemove, setup: map[string]string{"a/old": "o"},
			do: func(tx *txn.Tx) error { return tx.Remove("x", "a/old") }},
		{name: "make-dir", op: txn.OpMakeDir, commit: fsx.StepMkdir,
			do: func(tx *txn.Tx) error { return tx.MakeDir("x", "a/new") }},
		{name: "rename-dir", op: txn.OpRenameDir, commit: fsx.StepRenameDir, dirs: []string{"a/stage"},
			do: func(tx *txn.Tx) error { return tx.RenameDir("x", "a/stage", "a/pkg") }},
		{name: "remove-all", op: txn.OpRemoveAll, commit: fsx.StepRemoveAll, dirs: []string{"a/junk"},
			do: func(tx *txn.Tx) error { return tx.RemoveAll("x", "a/junk") }},
		{name: "discard", op: txn.OpDiscard, commit: fsx.StepRenameDir, dirs: []string{"a/old"},
			do: func(tx *txn.Tx) error { return tx.Discard("x", "a/old") }},
		{name: "sync-dir", op: txn.OpSyncDir, commit: fsx.StepSyncDir,
			do: func(tx *txn.Tx) error { return tx.SyncDir("x", "a") }},
		{name: "append-then-sync-file", op: txn.OpAppend,
			extra:  []txn.Write{{Role: "x", Op: txn.OpSyncFile}},
			commit: fsx.StepSync, // the append alone is not a commit step
			do: func(tx *txn.Tx) error {
				if err := tx.Append("x", "a/log", []byte("l\n")); err != nil {
					return err
				}
				return tx.SyncFile("x", "a/log")
			}},
	}
	build := func(c opCase) *txn.Decl {
		ws := []txn.Write{{Role: "prep", Op: txn.OpReplace, Prepare: true}, {Role: "x", Op: c.op}}
		ws = append(ws, c.extra...)
		ws = append(ws, txn.Write{Role: "next", Op: txn.OpReplace})
		return &txn.Decl{Name: "ops", Kind: txn.Normal, Writes: ws}
	}
	prepare := func(t *testing.T, c opCase, faulty **fsxtest.Faulty) (*rig, *txn.Decl) {
		d := build(c)
		r := newRig(t, faultyOver(t, faulty), d)
		put(t, r.mem, c.setup)
		mkdirs(t, r.mem, c.dirs...)
		return r, d
	}
	for _, c := range ops {
		t.Run(c.name+"/later-write-is-fatal", func(t *testing.T) {
			var faulty *fsxtest.Faulty
			r, d := prepare(t, c, &faulty)
			faulty.Fail(fsxtest.Match{Tx: "ops", Role: "next", Step: fsx.StepCreate}, syscall.EIO)
			tx := r.w.Begin(d)
			defer tx.Close()
			if err := tx.Replace("prep", "b/prep", []byte("p"), mode); err != nil {
				t.Fatal(err)
			}
			if err := c.do(tx); err != nil {
				t.Fatal(err)
			}
			err := tx.Replace("next", "b/next", []byte("n"), mode)
			if te := txnError(t, err); te.Class != fsx.Fatal {
				t.Errorf("a failure after the %v step completed is %v, want fatal", c.commit, te.Class)
			}
		})
		t.Run(c.name+"/commit-step-failing-is-not-yet-committed", func(t *testing.T) {
			var faulty *fsxtest.Faulty
			r, d := prepare(t, c, &faulty)
			faulty.Fail(fsxtest.Match{Tx: "ops", Role: "x", Step: c.commit, Hit: 1}, syscall.EIO)
			tx := r.w.Begin(d)
			defer tx.Close()
			if err := tx.Replace("prep", "b/prep", []byte("p"), mode); err != nil {
				t.Fatal(err)
			}
			err := c.do(tx)
			te := txnError(t, err)
			want := fsx.Failed
			if c.commit == fsx.StepSyncDir || (c.op == txn.OpAppend && c.commit == fsx.StepSync) {
				want = fsx.Fatal // the file system layer's own class
			}
			if te.Class != want {
				t.Errorf("a failure of the %v step itself is %v, want %v", c.commit, te.Class, want)
			}
		})
		t.Run(c.name+"/preparatory-write-before-it-does-not-commit", func(t *testing.T) {
			var faulty *fsxtest.Faulty
			r, d := prepare(t, c, &faulty)
			if c.early == 0 {
				t.Skip("the operation has no step before its commit step")
			}
			faulty.Fail(fsxtest.Match{Tx: "ops", Role: "x", Step: c.early, Hit: 1}, syscall.EIO)
			tx := r.w.Begin(d)
			defer tx.Close()
			if err := tx.Replace("prep", "b/prep", []byte("p"), mode); err != nil {
				t.Fatal(err)
			}
			err := c.do(tx)
			if te := txnError(t, err); te.Class != fsx.Failed {
				t.Errorf("a failure of the %v step before the commit step, after a completed prepare write, is %v, want failed", c.early, te.Class)
			}
		})
	}
}

// The degenerate transaction of one replacement behaves exactly like the
// replacement alone: everything before the rename fails the operation, the
// directory sync after it is fatal.
func TestSingleReplaceKeepsTheFileSystemClasses(t *testing.T) {
	for _, c := range []struct {
		step  fsx.Step
		class fsx.Class
	}{
		{fsx.StepCreate, fsx.Failed}, {fsx.StepWrite, fsx.Failed}, {fsx.StepSync, fsx.Failed},
		{fsx.StepRename, fsx.Failed}, {fsx.StepSyncDir, fsx.Fatal},
	} {
		t.Run(c.step.String(), func(t *testing.T) {
			var faulty *fsxtest.Faulty
			r := newRig(t, faultyOver(t, &faulty), singleReplace)
			faulty.Fail(fsxtest.Match{Tx: "single", Step: c.step}, syscall.EIO)
			tx := r.w.Begin(singleReplace)
			defer tx.Close()
			err := tx.Replace("rec", "a/rec", []byte("new"), mode)
			if te := txnError(t, err); te.Class != c.class {
				t.Errorf("class = %v, want %v", te.Class, c.class)
			}
		})
	}
}

// A transaction that only appends never reaches a commit step by appending,
// so a failed append stays an ordinary failure however many appends came
// before it.
func TestFailedAppendOfAnUnsyncedBatchIsAnOrdinaryFailure(t *testing.T) {
	var faulty *fsxtest.Faulty
	r := newRig(t, faultyOver(t, &faulty), eventsOnly)
	faulty.Fail(fsxtest.Match{Tx: "events", Step: fsx.StepWrite, Hit: 3}, syscall.ENOSPC)
	tx := r.w.Begin(eventsOnly)
	defer tx.Close()
	for i := 0; i < 2; i++ {
		if err := tx.Append("events", "a/events", []byte("e\n")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	err := tx.Append("events", "a/events", []byte("e\n"))
	te := txnError(t, err)
	if te.Class != fsx.Failed || txn.IsFatal(err) {
		t.Errorf("class = %v, IsFatal = %v; want an ordinary failure", te.Class, txn.IsFatal(err))
	}
	if !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("error %v does not wrap ENOSPC", err)
	}
}

// The digest is made durable before what it describes is deleted: the sync of
// the appended file is the commit point of the sweep, so a failure to replace
// the floor or to delete is fatal, and a failed append is not.
func TestSyncFileOfADigestIsTheCommitPointOfASweep(t *testing.T) {
	run := func(t *testing.T, fail fsxtest.Match) error {
		var faulty *fsxtest.Faulty
		r := newRig(t, faultyOver(t, &faulty), sweepLike)
		put(t, r.mem, map[string]string{"a/old-1": "1", "a/old-2": "2"})
		faulty.Fail(fail, syscall.EIO)
		tx := r.w.Begin(sweepLike)
		defer tx.Close()
		for _, err := range []error{
			tx.Append("digest", "a/digest", []byte("d1\n")),
			tx.Append("digest", "a/digest", []byte("d2\n")),
			tx.SyncFile("digest", "a/digest"),
			tx.Replace("floor", "b/floor", []byte("f"), mode),
			tx.Remove("old", "a/old-1"),
			tx.Remove("old", "a/old-2"),
			tx.SyncDir("old-dir", "a"),
		} {
			if err != nil {
				return err
			}
		}
		return nil
	}
	for _, c := range []struct {
		name  string
		match fsxtest.Match
		class fsx.Class
	}{
		{"second-append", fsxtest.Match{Tx: "sweep", Role: "digest", Step: fsx.StepWrite, Hit: 2}, fsx.Failed},
		{"floor-after-the-digest-is-durable", fsxtest.Match{Tx: "sweep", Role: "floor", Step: fsx.StepCreate}, fsx.Fatal},
		{"first-delete-after-the-digest-is-durable", fsxtest.Match{Tx: "sweep", Role: "old", Step: fsx.StepRemove, Hit: 1}, fsx.Fatal},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := run(t, c.match)
			if err == nil {
				t.Fatal("injected failure did not surface")
			}
			if te := txnError(t, err); te.Class != c.class {
				t.Errorf("class = %v, want %v", te.Class, c.class)
			}
		})
	}
}

// Delete a batch of files: a failure on the third deletion comes after two
// deletions the process can see, so it is fatal; a failure of the first
// deletion changed nothing.
func TestFailedDeletionInTheMiddleOfABatchIsFatal(t *testing.T) {
	for _, c := range []struct {
		hit   int
		class fsx.Class
	}{{1, fsx.Failed}, {2, fsx.Fatal}, {3, fsx.Fatal}} {
		t.Run(string(rune('0'+c.hit)), func(t *testing.T) {
			var faulty *fsxtest.Faulty
			r := newRig(t, faultyOver(t, &faulty), sweepLike)
			put(t, r.mem, map[string]string{"a/o1": "1", "a/o2": "2", "a/o3": "3"})
			faulty.Fail(fsxtest.Match{Tx: "sweep", Role: "old", Step: fsx.StepRemove, Hit: c.hit}, syscall.EIO)
			tx := r.w.Begin(sweepLike)
			defer tx.Close()
			var err error
			for _, p := range []string{"a/o1", "a/o2", "a/o3"} {
				if err = tx.Remove("old", p); err != nil {
					break
				}
			}
			if te := txnError(t, err); te.Class != c.class {
				t.Errorf("class = %v, want %v", te.Class, c.class)
			}
		})
	}
}

// IsFatal answers for what the file system layer returned too, and for nothing
// else.
func TestIsFatal(t *testing.T) {
	if txn.IsFatal(nil) {
		t.Error("IsFatal(nil)")
	}
	if txn.IsFatal(errBoom) {
		t.Error("IsFatal of a plain error")
	}
	if !txn.IsFatal(&fsx.Error{Class: fsx.Fatal, Err: errBoom}) {
		t.Error("IsFatal of a fatal file system error")
	}
	if txn.IsFatal(&fsx.Error{Class: fsx.Failed, Err: errBoom}) {
		t.Error("IsFatal of an ordinary file system failure")
	}
	if !txn.IsFatal(&txn.Error{Class: fsx.Fatal, Err: errBoom}) {
		t.Error("IsFatal of a fatal transaction error")
	}
	if txn.IsFatal(&txn.Error{Class: fsx.Failed, Err: errBoom}) {
		t.Error("IsFatal of an ordinary transaction failure")
	}
}
