package txn_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

var mode = fsx.ModePrivate

func newMultiRig(t testing.TB) *rig { return newRig(t, nil, multi) }

func TestBeginPanicsOnADeclarationThatIsNotInTheTable(t *testing.T) {
	r := newMultiRig(t)
	other := &txn.Decl{Name: "other", Kind: txn.Normal, Writes: []txn.Write{{Role: "r", Op: txn.OpReplace}}}
	mustPanic(t, "not in the table", func() { r.w.Begin(other) })

	// A copy with the same name is another declaration: the table holds the
	// one it was built from.
	copied := *multi
	mustPanic(t, "not in the table", func() { r.w.Begin(&copied) })

	mustPanic(t, "not in the table", func() { r.w.Begin(nil) })
}

func TestBeginPanicsWhileATxOfTheSameDeclarationIsOpen(t *testing.T) {
	r := newRig(t, nil, multi, singleReplace)
	first := r.w.Begin(multi)
	mustPanic(t, "already open", func() { r.w.Begin(multi) })

	// A different declaration may run at the same time.
	mustNotPanic(t, func() { r.w.Begin(singleReplace).Close() })

	// Ending the first one, by Close or by Commit, frees the declaration.
	first.Close()
	mustNotPanic(t, func() { r.w.Begin(multi).Close() })

	second := r.w.Begin(singleReplace)
	if err := second.Replace("rec", "a/x", []byte("x"), mode); err != nil {
		t.Fatal(err)
	}
	if err := second.Commit(); err != nil {
		t.Fatal(err)
	}
	mustNotPanic(t, func() { r.w.Begin(singleReplace).Close() })
}

func TestBeginIsFreedByCommitEvenWhenTheTxFailed(t *testing.T) {
	var faulty *fsxtest.Faulty
	r := newRig(t, faultyOver(t, &faulty), singleReplace)
	faulty.Fail(fsxtest.Match{Tx: "single", Step: fsx.StepCreate}, errBoom)
	tx := r.w.Begin(singleReplace)
	if err := tx.Replace("rec", "a/x", []byte("x"), mode); err == nil {
		t.Fatal("injected failure did not surface")
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("Commit of a failed Tx returned nil")
	}
	mustNotPanic(t, func() { r.w.Begin(singleReplace).Close() })
}

// What an out-of-line call looks like to the layer: each is a programming
// error, so each panics, naming the rule.
func TestTxPanicsOnACallTheDeclarationDoesNotAllow(t *testing.T) {
	cases := []struct {
		name string
		want string
		do   func(tx *txn.Tx)
	}{
		{"role-not-in-the-declaration", "role", func(tx *txn.Tx) { tx.Replace("nope", "b/x", nil, mode) }},
		{"wrong-op-for-the-role", "operation", func(tx *txn.Tx) { tx.Remove("body", "b/body") }},
		{"deferred-op-on-an-ordinary-replace-role", "operation", func(tx *txn.Tx) { tx.ReplaceDefer("body", "b/body", nil, mode) }},
		{"second-write-before-the-first-required-one", "order", func(tx *txn.Tx) { tx.Replace("out", "b/out", nil, mode) }},
		{"skipping-a-required-write", "order", func(tx *txn.Tx) {
			tx.Replace("body", "b/body", nil, mode)
			tx.SyncDir("in-dir", "a")
		}},
		{"repeating-a-write-that-is-not-many", "order", func(tx *txn.Tx) {
			tx.Replace("body", "b/body", nil, mode)
			tx.Replace("body", "b/body", nil, mode)
		}},
		{"going-back-to-an-earlier-write", "order", func(tx *txn.Tx) {
			tx.Replace("body", "b/body", nil, mode)
			tx.ReplaceDefer("in", "a/in-1", nil, mode)
			tx.SyncDir("in-dir", "a")
			tx.Replace("out", "b/out", nil, mode)
			tx.ReplaceDefer("in", "a/in-2", nil, mode)
		}},
		{"commit-before-the-required-writes", "incomplete", func(tx *txn.Tx) {
			tx.Replace("body", "b/body", nil, mode)
			tx.Commit()
		}},
		{"commit-of-an-empty-transaction", "incomplete", func(tx *txn.Tx) { tx.Commit() }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newMultiRig(t)
			tx := r.w.Begin(multi)
			defer tx.Close()
			mustPanic(t, c.want, func() { c.do(tx) })
		})
	}
}

// Everything the declaration allows must not panic: Many repeats, Optional
// skips and the longest and the shortest legal sequences.
func TestTxAcceptsEverySequenceTheDeclarationAllows(t *testing.T) {
	d := &txn.Decl{Name: "flex", Kind: txn.Normal, Writes: []txn.Write{
		{Role: "a", Op: txn.OpReplace, Optional: true},
		{Role: "b", Op: txn.OpReplace, Many: true, Optional: true},
		{Role: "c", Op: txn.OpReplace},
		{Role: "d", Op: txn.OpReplace, Many: true},
		{Role: "e", Op: txn.OpReplace, Optional: true},
	}}
	run := func(t *testing.T, calls ...string) {
		t.Helper()
		r := newRig(t, nil, d)
		tx := r.w.Begin(d)
		defer tx.Close()
		mustNotPanic(t, func() {
			for i, role := range calls {
				if err := tx.Replace(role, "a/f"+string(rune('0'+i)), []byte(role), mode); err != nil {
					t.Fatalf("%s: %v", role, err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("Commit: %v", err)
			}
		})
	}
	t.Run("only-the-required-writes", func(t *testing.T) { run(t, "c", "d") })
	t.Run("every-write-and-every-repeat", func(t *testing.T) { run(t, "a", "b", "b", "b", "c", "d", "d", "e") })
	t.Run("skipping-the-first-optional-only", func(t *testing.T) { run(t, "b", "c", "d") })
	t.Run("skipping-the-last-optional-only", func(t *testing.T) { run(t, "a", "c", "d") })
	t.Run("skipping-an-optional-between-required-ones", func(t *testing.T) { run(t, "c", "d", "e") })
}

// A group of deferred replacements is one directory and is closed by that
// directory's sync before anything else is written.
func TestDeferredReplacementsMustBeSyncedBeforeAnythingElse(t *testing.T) {
	d := &txn.Decl{Name: "grp", Kind: txn.Normal, Writes: []txn.Write{
		{Role: "in", Op: txn.OpReplaceDefer, Many: true},
		{Role: "in-dir", Op: txn.OpSyncDir, Optional: true}, // optional on purpose: the run time check is what catches a missing sync
		{Role: "x", Op: txn.OpReplace, Optional: true},
		{Role: "y", Op: txn.OpSyncDir, Optional: true},
	}}
	cases := []struct {
		name string
		want string
		do   func(tx *txn.Tx)
	}{
		{"a-second-directory-in-one-group", "directory", func(tx *txn.Tx) {
			tx.ReplaceDefer("in", "a/one", nil, mode)
			tx.ReplaceDefer("in", "b/two", nil, mode)
		}},
		{"another-write-before-the-sync", "SyncDir", func(tx *txn.Tx) {
			tx.ReplaceDefer("in", "a/one", nil, mode)
			tx.Replace("x", "b/x", nil, mode)
		}},
		{"the-sync-of-another-directory", "directory", func(tx *txn.Tx) {
			tx.ReplaceDefer("in", "a/one", nil, mode)
			tx.SyncDir("in-dir", "b")
		}},
		{"commit-before-the-sync", "SyncDir", func(tx *txn.Tx) {
			tx.ReplaceDefer("in", "a/one", nil, mode)
			tx.Commit()
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil, d)
			tx := r.w.Begin(d)
			defer tx.Close()
			mustPanic(t, c.want, func() { c.do(tx) })
		})
	}

	t.Run("the-sync-of-the-same-directory-ends-the-group", func(t *testing.T) {
		r := newRig(t, nil, d)
		tx := r.w.Begin(d)
		defer tx.Close()
		mustNotPanic(t, func() {
			for _, err := range []error{
				tx.ReplaceDefer("in", "a/one", []byte("1"), mode),
				tx.ReplaceDefer("in", "a/two", []byte("2"), mode),
				tx.SyncDir("in-dir", "a"),
				tx.Replace("x", "b/x", []byte("x"), mode),
				tx.Commit(),
			} {
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	})

	t.Run("a-group-after-a-group-may-use-another-directory", func(t *testing.T) {
		two := &txn.Decl{Name: "two", Kind: txn.Normal, Writes: []txn.Write{
			{Role: "a", Op: txn.OpReplaceDefer, Many: true}, {Role: "a-dir", Op: txn.OpSyncDir},
			{Role: "b", Op: txn.OpReplaceDefer, Many: true}, {Role: "b-dir", Op: txn.OpSyncDir},
		}}
		r := newRig(t, nil, two)
		tx := r.w.Begin(two)
		defer tx.Close()
		mustNotPanic(t, func() {
			for _, err := range []error{
				tx.ReplaceDefer("a", "a/1", nil, mode),
				tx.SyncDir("a-dir", "a"),
				tx.ReplaceDefer("b", "b/1", nil, mode),
				tx.SyncDir("b-dir", "b"),
				tx.Commit(),
			} {
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	})

	t.Run("a-replacement-at-the-root-is-the-root-directory", func(t *testing.T) {
		r := newRig(t, nil, d)
		tx := r.w.Begin(d)
		defer tx.Close()
		mustNotPanic(t, func() {
			if err := tx.ReplaceDefer("in", "top", []byte("1"), mode); err != nil {
				t.Fatal(err)
			}
			if err := tx.SyncDir("in-dir", "."); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		})
	})
}

// An appended file is synced through the handle that appended it, under the
// role that appended it.
func TestSyncFileMustMatchTheAppendInThisTx(t *testing.T) {
	two := &txn.Decl{Name: "logs", Kind: txn.Normal, Writes: []txn.Write{
		{Role: "first", Op: txn.OpAppend, Many: true, Optional: true},
		{Role: "second", Op: txn.OpAppend, Many: true, Optional: true},
		{Role: "first", Op: txn.OpSyncFile, Optional: true},
		{Role: "second", Op: txn.OpSyncFile, Optional: true},
	}}
	cases := []struct {
		name string
		want string
		do   func(tx *txn.Tx)
	}{
		{"a-path-that-was-not-appended", "appended", func(tx *txn.Tx) {
			tx.Append("first", "a/one", []byte("x"))
			tx.SyncFile("first", "a/other")
		}},
		{"the-role-of-another-append", "role", func(tx *txn.Tx) {
			tx.Append("first", "a/one", []byte("x"))
			tx.Append("second", "a/two", []byte("y"))
			tx.SyncFile("second", "a/one")
		}},
		{"one-path-appended-under-two-roles", "role", func(tx *txn.Tx) {
			tx.Append("first", "a/one", []byte("x"))
			tx.Append("second", "a/one", []byte("y"))
		}},
		{"two-paths-under-one-role-only-one-synced", "a/two", func(tx *txn.Tx) {
			tx.Append("first", "a/one", []byte("x"))
			tx.Append("first", "a/two", []byte("y"))
			tx.SyncFile("first", "a/one")
			tx.Commit()
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil, two)
			tx := r.w.Begin(two)
			defer tx.Close()
			mustPanic(t, c.want, func() { c.do(tx) })
		})
	}

	t.Run("two-files-each-synced-under-its-own-role", func(t *testing.T) {
		r := newRig(t, nil, two)
		tx := r.w.Begin(two)
		defer tx.Close()
		mustNotPanic(t, func() {
			for _, err := range []error{
				tx.Append("first", "a/one", []byte("x")),
				tx.Append("second", "a/two", []byte("y")),
				tx.SyncFile("first", "a/one"),
				tx.SyncFile("second", "a/two"),
				tx.Commit(),
			} {
				if err != nil {
					t.Fatal(err)
				}
			}
		})
		got := snapshot(t, r.mem.CrashCloneWorst())
		if got["a/one"] != "x" || got["a/two"] != "y" {
			t.Errorf("synced appends after power loss: %v", got)
		}
	})
}

// A transaction that made no write has nothing to commit: Commit returns nil
// and reaches no point, so an owner that opened one with nothing to do does not
// leave a commit point of its own on a start that had no work. A declaration
// with a required write still panics: that Commit is an incomplete transaction,
// not an empty one.
func TestCommitOfATransactionThatMadeNoWriteReachesNoPoint(t *testing.T) {
	d := &txn.Decl{Name: "idle", Kind: txn.Recovery, Writes: []txn.Write{
		{Role: "only", Op: txn.OpRemove, Many: true, Optional: true},
	}}
	r := newRig(t, nil, d)
	tx := r.w.Begin(d)
	defer tx.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit of an empty transaction: %v", err)
	}
	if n := r.rec.count(); n != 0 {
		t.Errorf("Commit of an empty transaction reached %d points: %v", n, r.rec.names())
	}
	for _, c := range r.w.Coverage() {
		if c.Hits != 0 {
			t.Errorf("%v has %d hits after an empty transaction", c.Key.Key, c.Hits)
		}
	}
	mustNotPanic(t, func() { r.w.Begin(d).Close() }) // the declaration is free again

	// One write is enough for the commit point.
	put(t, r.mem, map[string]string{"a/x": "1"})
	again := r.w.Begin(d)
	defer again.Close()
	if err := again.Remove("only", "a/x"); err != nil {
		t.Fatal(err)
	}
	if err := again.Commit(); err != nil {
		t.Fatal(err)
	}
	if names := r.rec.names(); len(names) != 2 || names[1] != "idle/commit#1" {
		t.Errorf("points = %v, want the remove and the commit point", names)
	}
}

func TestTxPanicsWhenUsedAfterItEnded(t *testing.T) {
	end := map[string]func(tx *txn.Tx){
		"commit": func(tx *txn.Tx) {
			if err := tx.Replace("rec", "a/x", []byte("x"), mode); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		},
		"close": func(tx *txn.Tx) { tx.Close() },
	}
	for name, ending := range end {
		t.Run("after-"+name, func(t *testing.T) {
			r := newRig(t, nil, singleReplace)
			tx := r.w.Begin(singleReplace)
			ending(tx)
			mustPanic(t, "ended", func() { tx.Replace("rec", "a/y", nil, mode) })
			mustPanic(t, "ended", func() { tx.Commit() })
			mustNotPanic(t, tx.Close) // Close is idempotent
		})
	}
}

func TestCloseWithoutCommitIsNotAnError(t *testing.T) {
	r := newMultiRig(t)
	tx := r.w.Begin(multi)
	if err := tx.Replace("body", "b/body", []byte("b"), mode); err != nil {
		t.Fatal(err)
	}
	mustNotPanic(t, tx.Close)
	if got := r.rec.names(); got[len(got)-1] == "multi/commit#1" {
		t.Errorf("Close emitted a commit point: %v", got)
	}
}
