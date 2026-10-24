package store_test

import (
	"fmt"
	"path"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/store"
)

// The disk of the store after every step of a random run, and after restarts
// and damage, is exactly the disk a model predicts. The model is the ledger of the
// power-loss test, built from the fixtures and the encoder, not from the store's
// own state; the steps are the operations of the stage machine, the sweep, files
// damaged while the node is stopped and recovered, and restarts after a power
// loss. Seeds are fixed, and the test says how often each kind of step ran.
func TestRandomOperationsLeaveExactlyTheDiskAModelPredicts(t *testing.T) {
	counts := map[string]int{}
	for seed := uint64(11); seed <= 16; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			r := newRig(t)
			rng := newRand(seed)
			w := &powerLossWorld{
				t: t, r: r, rng: func(n int) int { return rng.IntN(n) }, damage: true,
				led:   ledger{files: map[string][]byte{}, summaries: map[string]bool{}},
				stage: map[int]int{}, rev: map[int]uint64{}, counts: counts,
			}
			e2 := directEndpoint(2, "chat")
			mustDo(t, r.st.EndpointUpdate(e2))
			w.led.files[path.Join(store.EndpointsDir, e2.Name())] = w.enc(e2)
			for step := 0; step < 250; step++ {
				op := w.next()
				if op == nil {
					continue
				}
				w.cur = op
				err := op.call()
				w.cur = nil
				if err != nil {
					t.Fatalf("step %d, %s: %v", step, op.name, err)
				}
				w.commit(op)
				counts[op.name]++
				if op.after != nil {
					op.after()
				}
				w.compareExactly(fmt.Sprintf("after step %d (%s)", step, op.name))
			}
		})
	}
	for n, min := range map[string]int{"intake": 20, "deliver": 20, "lease": 15, "ack": 10, "source-update": 8, "archive-sweep": 6, "damage": 6, "restart": 20, "cross-owner": 20} {
		if counts[n] < min {
			t.Errorf("step %s ran %d times in all, want at least %d", n, counts[n], min)
		}
	}
	t.Logf("steps: %v", counts)
}

// compareExactly checks that every file of the disk, but the archive, is a file
// of the ledger with its bytes, floor files included, and every file of the ledger
// is on the disk.
func (w *powerLossWorld) compareExactly(where string) {
	t := w.t
	t.Helper()
	got := snapshot(t, w.r.mem, ".")
	for p, want := range w.led.files {
		if have, ok := got[p]; !ok {
			t.Errorf("%s: %s is missing", where, p)
		} else if have != string(want) {
			t.Errorf("%s: %s has other content than the model says", where, p)
		}
	}
	for p := range got {
		if _, ok := w.led.files[p]; !ok && !strings.HasPrefix(p, store.ArchiveDir+"/") {
			t.Errorf("%s: %s is on the disk and not in the model", where, p)
		}
	}
	sc := w.r.scan()
	noWork(t, where, sc, func(string) bool { return true })
	if len(sc.Damaged) != 0 || len(sc.Unknown) != 0 {
		t.Errorf("%s: damaged %v unknown %v", where, sc.Damaged, sc.Unknown)
	}
}
