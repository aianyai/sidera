package txn_test

import (
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

// beginAndClose opens a transaction of d and closes it again without a write,
// so the only thing the call can do is refuse.
func beginAndClose(w *txn.Writer, d *txn.Decl) {
	w.Begin(d).Close()
}

// An owner's transaction is not allowed before Recover has returned nil: every
// recovery predicate of an owner reads existence and acts on it durably, and
// only after the settle of Recover is what it reads a state a power loss
// cannot take back.
func TestBeginOfAnOwnerDeclarationBeforeRecoverPanics(t *testing.T) {
	for _, d := range []*txn.Decl{multi, replayLike} {
		t.Run(d.Name, func(t *testing.T) {
			r := newRigUnrecovered(t, nil, multi, replayLike)
			mustPanic(t, `"`+d.Name+`" before Recover has returned`, func() { beginAndClose(r.w, d) })
		})
	}
}

func TestBeginOfAnOwnerDeclarationAfterASuccessfulRecoverWorks(t *testing.T) {
	r := newRigUnrecovered(t, nil, multi)
	if err := r.w.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	mustNotPanic(t, func() { beginAndClose(r.w, multi) })
}

// A Recover that returned an error did not make the disk settled, so the
// Writer is still not recovered. Both ways it can fail are covered: the
// settle fails, and the settle succeeds but the cleanup fails.
func TestBeginOfAnOwnerDeclarationAfterAFailedRecoverStillPanics(t *testing.T) {
	cases := []struct {
		name   string
		leftov bool // a leftover for the cleanup to remove
		match  fsxtest.Match
	}{
		{"settle-fails", false, fsxtest.Match{Tx: "txn-settle", Role: "dir", Step: fsx.StepSyncDir, Hit: 1}},
		{"cleanup-fails", true, fsxtest.Match{Tx: "txn-cleanup", Role: "tmp", Step: fsx.StepRemove, Hit: 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var faulty *fsxtest.Faulty
			r := newRigUnrecovered(t, faultyOver(t, &faulty), multi)
			if c.leftov {
				put(t, r.mem, map[string]string{"a/.tmp-1": "x"})
			}
			rule := faulty.Fail(c.match, syscall.EIO)
			if err := r.w.Recover(); err == nil {
				t.Fatal("Recover returned nil, want the injected failure")
			}
			if rule.Hits() == 0 {
				t.Fatalf("nothing was injected at %+v", c.match)
			}
			mustPanic(t, "Recover must run first", func() { beginAndClose(r.w, multi) })
		})
	}
}

// The check concerns owners: Recover begins the declarations of this package
// before it has returned, and the check must not stand in its way.
func TestBeginOfAnInternalDeclarationIsNotRefusedBeforeRecover(t *testing.T) {
	internal := txn.Internal()
	tab, err := txn.NewTable(internal...)
	if err != nil {
		t.Fatalf("NewTable: %v", err)
	}
	w := txn.New(fsxtest.NewMem(), tab, nil)
	for _, d := range internal {
		mustNotPanic(t, func() { beginAndClose(w, d) })
	}
}
