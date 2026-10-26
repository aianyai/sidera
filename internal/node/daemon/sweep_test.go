package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
)

func adminResult(b byte, retainUntil time.Time) store.AdminRecord {
	var op admin.OperationID
	op[0], op[15] = b, 1
	var fp admin.Fingerprint
	fp[0] = b
	return store.AdminRecord{
		Key: admin.RequestKey{ID: op}, Fingerprint: fp, Op: "endpoint.close",
		Result: []byte(`{"closed":true}`), At: t0.Add(-time.Hour), RetainUntil: retainUntil,
	}
}

// The daemon runs the archive sweep on a period of its own, on the injected clock: what
// has served its retention is removed from state/ one period after the start, and what
// has not is left.
func TestTheDaemonSweepsTheArchiveEveryPeriodOnTheInjectedClock(t *testing.T) {
	noLeaks(t)
	c, clk := initialized(t)
	expired, fresh := adminResult(1, t0.Add(-time.Minute)), adminResult(2, t0.Add(24*time.Hour))
	seedStore(t, c.Home, func(st *store.Store) {
		for _, r := range []store.AdminRecord{expired, fresh} {
			if err := st.DaemonStop(r); err != nil {
				t.Fatal(err)
			}
		}
	})
	file := func(r store.AdminRecord) string { return filepath.Join(c.Home, store.AdminDir, r.Name()) }
	// The start has no sweep of its own: the task is due one period after it.
	d := started(t, c)
	settle := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := d.Engine().Quiesce(ctx, engine.Expect{}); err != nil {
			t.Fatalf("Quiesce: %v", err)
		}
	}
	clk.Advance(daemon.ArchiveSweepEvery - time.Minute)
	settle()
	if _, err := os.Stat(file(expired)); err != nil {
		t.Fatalf("the expired result was removed before the period ended: %v", err)
	}
	clk.Advance(time.Minute)
	settle()
	if _, err := os.Stat(file(expired)); !os.IsNotExist(err) {
		t.Errorf("the expired result is still there after a period: %v", err)
	}
	if _, err := os.Stat(file(fresh)); err != nil {
		t.Errorf("a result that is still retained was removed: %v", err)
	}
}
