package keys_test

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/txn"
)

// After every step of a clean scenario, with no crash and no damage, the
// recovery predicates evaluated on a copy of the disk are empty: the one of this
// package and the one of the transaction layer. The steps are the lifecycle
// events of the keys: init, a start, renewals (the periodic ones and the one at
// the end of the validity), the periodic clock writes, a rollback pause and its
// clear, a restart, a second renewal after the clear.
func TestRecoveryPredicatesAreEmptyOnCleanScenarios(t *testing.T) {
	r := newRig(t)
	steps := 0
	check := func(what string) {
		t.Helper()
		steps++
		clone := r.mem.Clone()
		if work, err := keys.RecoveryWork(clone); err != nil || len(work) != 0 {
			t.Errorf("after %s: keys.RecoveryWork = %v, %v; want nothing", what, work, err)
		}
		if work, err := txn.CleanupWork(clone); err != nil || len(work) != 0 {
			t.Errorf("after %s: txn.CleanupWork = %v, %v; want nothing", what, work, err)
		}
	}

	now := t0
	r.mustInit(1, now)
	check("init")
	k := r.mustLoad()
	check("load")

	now = now.Add(time.Hour)
	if err := k.SaveClock(keys.ClockState{HighWater: now}); err != nil {
		t.Fatal(err)
	}
	check("a periodic clock write")

	now = now.Add(21 * 24 * time.Hour)
	if v, reason := k.RenewalDue(now); v != keys.RenewDue || reason != keys.RenewReasonExpiring {
		t.Fatalf("RenewalDue at day 21 = %v/%v", v, reason)
	}
	if err := k.Renew(now); err != nil {
		t.Fatal(err)
	}
	check("a renewal because the validity runs out")

	if err := k.SaveClock(keys.ClockState{HighWater: now.Add(time.Minute), RollbackPausedSince: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	check("the start of a rollback pause")

	tx := r.w.Begin(clearDecl)
	if err := keys.PutClockCleared(tx, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx.Close()
	check("the clear of the rollback pause")

	r = r.restart()
	check("a restart")
	k = r.mustLoad()
	now = now.Add(time.Hour)
	if err := k.Renew(now); err != nil {
		t.Fatal(err)
	}
	check("a renewal after the clear")
	if k.Certificate().Payload().Serial != 3 {
		t.Errorf("serial after two renewals = %d, want 3", k.Certificate().Payload().Serial)
	}
	if steps != 8 {
		t.Errorf("checked %d steps, want 8", steps)
	}
}

// The predicate is read-only and sees a damaged state as an error, not as
// nothing to do: a state R-CLEAN cannot call clean.
func TestRecoveryWorkReadsOnlyAndReportsDamage(t *testing.T) {
	r := newRig(t)
	r.mustInit(1, t0)
	before := snapshot(t, r.mem)
	clone := r.mem.Clone()
	if _, err := keys.RecoveryWork(clone); err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, clone)
	if len(after) != len(before) {
		t.Error("RecoveryWork changed the set of files")
	}
	b := append([]byte(nil), r.read(keys.SerialPath)...)
	b[len(b)-3] ^= 1
	put(t, clone, keys.SerialPath, b)
	if _, err := keys.RecoveryWork(clone); err == nil {
		t.Error("RecoveryWork of a damaged serial file reported no error")
	}
}
