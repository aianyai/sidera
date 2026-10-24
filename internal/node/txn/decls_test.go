package txn_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

// multi is a transaction over two directories that has one write of every
// shape the layer distinguishes: a preparatory write, a group of deferred
// replacements followed by the sync of their directory, an ordinary
// replacement, and appends made durable by one sync.
//
//	b/body   prepared, garbage if the transaction never commits
//	a/in-*   the first of them completing is the commit point
//	b/out    follows the deferred group
//	a/log    appended, then synced
var multi = &txn.Decl{
	Name: "multi",
	Kind: txn.Normal,
	Writes: []txn.Write{
		{Role: "body", Op: txn.OpReplace, Prepare: true},
		{Role: "in", Op: txn.OpReplaceDefer, Many: true},
		{Role: "in-dir", Op: txn.OpSyncDir},
		{Role: "out", Op: txn.OpReplace},
		{Role: "journal", Op: txn.OpAppend, Many: true},
		{Role: "journal", Op: txn.OpSyncFile},
	},
}

// The content multi writes, for the assertions that read it back.
var multiFiles = map[string]string{
	"b/body": "body",
	"a/in-1": "in one",
	"a/in-2": "in two",
	"b/out":  "out",
	"a/log":  "line 1\nline 2\n",
}

// multiPoints is every crash point of one run of multi, in the order the
// steps run, written out by hand from the declaration: the steps of each
// operation in order, the second deferred replacement after the first, the
// two appends sharing one open, and the commit last.
var multiPoints = []string{
	"multi/body.create#1", "multi/body.write#1", "multi/body.sync#1", "multi/body.rename#1", "multi/body.sync-dir#1",
	"multi/in.create#1", "multi/in.write#1", "multi/in.sync#1", "multi/in.rename#1",
	"multi/in.create#2", "multi/in.write#2", "multi/in.sync#2", "multi/in.rename#2",
	"multi/in-dir.sync-dir#1",
	"multi/out.create#1", "multi/out.write#1", "multi/out.sync#1", "multi/out.rename#1", "multi/out.sync-dir#1",
	"multi/journal.open#1", "multi/journal.write#1", "multi/journal.write#2", "multi/journal.sync#1", "multi/journal.sync-dir#1",
	"multi/commit#1",
}

// runMulti performs one run of multi and returns the error each call
// returned, in call order, Commit last.
func runMulti(tx *txn.Tx) []error {
	return []error{
		tx.Replace("body", "b/body", []byte(multiFiles["b/body"]), fsx.ModePrivate),
		tx.ReplaceDefer("in", "a/in-1", []byte(multiFiles["a/in-1"]), fsx.ModePrivate),
		tx.ReplaceDefer("in", "a/in-2", []byte(multiFiles["a/in-2"]), fsx.ModePrivate),
		tx.SyncDir("in-dir", "a"),
		tx.Replace("out", "b/out", []byte(multiFiles["b/out"]), fsx.ModePrivate),
		tx.Append("journal", "a/log", []byte("line 1\n")),
		tx.Append("journal", "a/log", []byte("line 2\n")),
		tx.SyncFile("journal", "a/log"),
		tx.Commit(),
	}
}

// firstError returns the index and value of the first non-nil error of a run.
func firstError(errs []error) (int, error) {
	for i, err := range errs {
		if err != nil {
			return i, err
		}
	}
	return -1, nil
}

// rig is a Mem under a Writer for a table of the given declarations.
type rig struct {
	mem *fsxtest.Mem
	rec *recorder
	w   *txn.Writer
	tab *txn.Table
}

// newRig builds the table, a Mem holding the directories a and b, and a Writer
// over base(mem), which is the Mem itself when base is nil.
func newRig(t testing.TB, base func(*fsxtest.Mem) fsx.FS, decls ...*txn.Decl) *rig {
	t.Helper()
	return newRigUnrecovered(t, base, decls...)
}

// newRigUnrecovered is a rig whose Writer has not run Recover.
func newRigUnrecovered(t testing.TB, base func(*fsxtest.Mem) fsx.FS, decls ...*txn.Decl) *rig {
	t.Helper()
	mem := fsxtest.NewMem()
	mkdirs(t, mem, "a", "b")
	var fsys fsx.FS = mem
	if base != nil {
		fsys = base(mem)
	}
	r := &rig{mem: mem, rec: &recorder{}, tab: table(t, decls...)}
	r.w = txn.New(fsys, r.tab, r.rec)
	return r
}

func (r *rig) runMulti() []error {
	tx := r.w.Begin(multi)
	defer tx.Close()
	return runMulti(tx)
}

// faultyOver returns a base for newRig that wraps the Mem in a Faulty, which it
// also hands out.
func faultyOver(t testing.TB, out **fsxtest.Faulty) func(*fsxtest.Mem) fsx.FS {
	return func(m *fsxtest.Mem) fsx.FS {
		f := fsxtest.NewFaulty(t, m)
		*out = f
		return f
	}
}
