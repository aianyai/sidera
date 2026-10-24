package txn_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

// A Tx that passed its commit point and is closed without Commit and without a
// recorded failure is a state no caller may leave: the disk holds part of the
// transaction while memory shows none of it, and nothing says so. Failing
// loudly makes the process exit and recovery run, which is the right outcome
// for a state that is fatal by the rule of the commit point.
func TestCloseAfterTheCommitPointWithoutCommitOrFailurePanics(t *testing.T) {
	for _, c := range []struct {
		name string
		do   func(t *testing.T, tx *txn.Tx)
	}{
		{"after the first write that is not preparatory", func(t *testing.T, tx *txn.Tx) {
			mustWrite(t, tx.Replace("body", "b/body", []byte("b"), mode))
			mustWrite(t, tx.ReplaceDefer("in", "a/in-1", []byte("1"), mode))
		}},
		{"after the sync of a deferred group", func(t *testing.T, tx *txn.Tx) {
			mustWrite(t, tx.Replace("body", "b/body", []byte("b"), mode))
			mustWrite(t, tx.ReplaceDefer("in", "a/in-1", []byte("1"), mode))
			mustWrite(t, tx.SyncDir("in-dir", "a"))
		}},
		{"after every write but the last", func(t *testing.T, tx *txn.Tx) {
			mustWrite(t, tx.Replace("body", "b/body", []byte("b"), mode))
			mustWrite(t, tx.ReplaceDefer("in", "a/in-1", []byte("1"), mode))
			mustWrite(t, tx.SyncDir("in-dir", "a"))
			mustWrite(t, tx.Replace("out", "b/out", []byte("o"), mode))
			mustWrite(t, tx.Append("journal", "a/log", []byte("x\n")))
			mustWrite(t, tx.SyncFile("journal", "a/log"))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newMultiRig(t)
			tx := r.w.Begin(multi)
			c.do(t, tx)
			mustPanic(t, "commit point", tx.Close)
			// The declaration is free again, so the process can still be
			// inspected after the panic is recovered.
			mustNotPanic(t, func() { r.w.Begin(multi).Close() })
		})
	}
}

// A Close that is not a lapse does nothing: before the commit point the writes
// are garbage by design, after a failure the error is the record, and after
// Commit or a second Close there is nothing left to close.
func TestCloseThatIsNotALapseDoesNotPanic(t *testing.T) {
	t.Run("before any write", func(t *testing.T) {
		r := newMultiRig(t)
		mustNotPanic(t, r.w.Begin(multi).Close)
	})
	t.Run("with only preparatory writes made", func(t *testing.T) {
		r := newMultiRig(t)
		tx := r.w.Begin(multi)
		mustWrite(t, tx.Replace("body", "b/body", []byte("b"), mode))
		mustNotPanic(t, tx.Close)
	})
	t.Run("after Commit, twice", func(t *testing.T) {
		r := newMultiRig(t)
		tx := r.w.Begin(multi)
		if i, err := firstError(runMulti(tx)); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		mustNotPanic(t, tx.Close)
		mustNotPanic(t, tx.Close)
	})
	t.Run("after a failure past the commit point", func(t *testing.T) {
		var faulty *fsxtest.Faulty
		r := newRig(t, faultyOver(t, &faulty), multi)
		rule := faulty.Fail(fsxtest.Match{Tx: "multi", Role: "out", Step: fsx.StepSync}, errBoom)
		tx := r.w.Begin(multi)
		mustWrite(t, tx.Replace("body", "b/body", []byte("b"), mode))
		mustWrite(t, tx.ReplaceDefer("in", "a/in-1", []byte("1"), mode))
		mustWrite(t, tx.SyncDir("in-dir", "a"))
		err := tx.Replace("out", "b/out", []byte("o"), mode)
		if err == nil || !txn.IsFatal(err) || rule.Hits() == 0 {
			t.Fatalf("err = %v (hits %d), want the injected failure after the commit point", err, rule.Hits())
		}
		mustNotPanic(t, tx.Close) // no Commit: the failure is the record
	})
	t.Run("after a failure before the commit point", func(t *testing.T) {
		var faulty *fsxtest.Faulty
		r := newRig(t, faultyOver(t, &faulty), multi)
		faulty.Fail(fsxtest.Match{Tx: "multi", Role: "body", Step: fsx.StepSync}, errBoom)
		tx := r.w.Begin(multi)
		if err := tx.Replace("body", "b/body", []byte("b"), mode); err == nil || txn.IsFatal(err) {
			t.Fatalf("err = %v, want an ordinary failure before the commit point", err)
		}
		mustNotPanic(t, tx.Close)
	})
}

// A programming error of the Tx itself panics at its own call; the deferred
// Close that runs while that panic unwinds must not replace it with another
// message.
func TestCloseDoesNotReplaceThePanicOfTheTxItself(t *testing.T) {
	r := newMultiRig(t)
	tx := r.w.Begin(multi)
	mustWrite(t, tx.Replace("body", "b/body", []byte("b"), mode))
	mustWrite(t, tx.ReplaceDefer("in", "a/in-1", []byte("1"), mode))
	mustPanic(t, "out of order", func() {
		defer tx.Close()
		tx.Replace("out", "b/out", nil, mode) // out of order: the sync of the deferred group comes first
	})
}

func mustWrite(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
