package txn_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

// A removal unbinds a name and does not make that durable: a power loss after
// Commit could bring the name back after the caller acknowledged its removal,
// and with it whatever the owner deleted because the name was gone. So Commit
// refuses a Tx in which a name was removed from a directory that was not synced
// after the removal. The sync is declared Optional in these declarations on
// purpose: the run time check is what catches a missing one.
func TestCommitPanicsWhenARemovalWasNotSynced(t *testing.T) {
	d := &txn.Decl{Name: "rm", Kind: txn.Normal, Writes: []txn.Write{
		{Role: "early-dir", Op: txn.OpSyncDir, Optional: true},
		{Role: "file", Op: txn.OpRemove, Many: true, Optional: true},
		{Role: "tree", Op: txn.OpRemoveAll, Many: true, Optional: true},
		{Role: "dir", Op: txn.OpSyncDir, Many: true, Optional: true},
	}}
	files := map[string]string{"a/f1": "1", "a/f2": "2", "b/f3": "3", "a/.trash-1/x": "x"}
	setup := func(t *testing.T) *rig {
		r := newRig(t, nil, d)
		mkdirs(t, r.mem, "a/.trash-1")
		put(t, r.mem, files)
		return r
	}
	for _, c := range []struct {
		name  string
		want  string // empty: Commit must not panic
		calls func(t *testing.T, tx *txn.Tx)
	}{
		{"a removal with no sync", `["a"]`, func(t *testing.T, tx *txn.Tx) {
			mustWrite(t, tx.Remove("file", "a/f1"))
		}},
		{"a removal of a tree with no sync", `["a"]`, func(t *testing.T, tx *txn.Tx) {
			mustWrite(t, tx.RemoveAll("tree", "a/.trash-1"))
		}},
		{"two directories and the sync of one", `["b"]`, func(t *testing.T, tx *txn.Tx) {
			mustWrite(t, tx.Remove("file", "a/f1"))
			mustWrite(t, tx.Remove("file", "b/f3"))
			mustWrite(t, tx.SyncDir("dir", "a"))
		}},
		{"a sync that came before the removal", `["a"]`, func(t *testing.T, tx *txn.Tx) {
			mustWrite(t, tx.SyncDir("early-dir", "a"))
			mustWrite(t, tx.Remove("file", "a/f1"))
		}},
		{"a batch followed by the sync of its directory", "", func(t *testing.T, tx *txn.Tx) {
			mustWrite(t, tx.Remove("file", "a/f1"))
			mustWrite(t, tx.Remove("file", "a/f2"))
			mustWrite(t, tx.SyncDir("dir", "a"))
		}},
		{"a removal and a tree in one directory, synced once", "", func(t *testing.T, tx *txn.Tx) {
			mustWrite(t, tx.Remove("file", "a/f1"))
			mustWrite(t, tx.RemoveAll("tree", "a/.trash-1"))
			mustWrite(t, tx.SyncDir("dir", "a"))
		}},
		{"no removal", "", func(t *testing.T, tx *txn.Tx) {
			mustWrite(t, tx.SyncDir("early-dir", "a"))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := setup(t)
			tx := r.w.Begin(d)
			defer tx.Close()
			c.calls(t, tx)
			if c.want == "" {
				mustNotPanic(t, func() { mustWrite(t, tx.Commit()) })
				return
			}
			mustPanic(t, c.want, func() { tx.Commit() })
		})
	}

	t.Run("a removal at the root", func(t *testing.T) {
		r := newRig(t, nil, d)
		put(t, r.mem, map[string]string{"top": "x"})
		tx := r.w.Begin(d)
		defer tx.Close()
		mustWrite(t, tx.Remove("file", "top"))
		mustPanic(t, `["."]`, func() { tx.Commit() })
	})

	t.Run("a failed removal is the error of Commit, not a panic", func(t *testing.T) {
		var faulty *fsxtest.Faulty
		r := newRig(t, faultyOver(t, &faulty), d)
		mkdirs(t, r.mem, "a/.trash-1")
		put(t, r.mem, files)
		rule := faulty.Fail(fsxtest.Match{Tx: "rm", Role: "file", Step: fsx.StepRemove}, errBoom)
		tx := r.w.Begin(d)
		defer tx.Close()
		if err := tx.Remove("file", "a/f1"); err == nil || rule.Hits() == 0 {
			t.Fatalf("Remove: %v (hits %d), want the injected failure", err, rule.Hits())
		}
		mustNotPanic(t, func() {
			if err := tx.Commit(); err == nil {
				t.Error("Commit of a failed Tx returned nil")
			}
		})
	})
}
