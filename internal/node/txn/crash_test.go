package txn_test

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

// Every declared step of a run is reached, once and in the declared order,
// and the commit point comes last: the sequence is written out by hand from
// the declaration, not derived from the code.
func TestEveryDeclaredKeyIsReachedInOrder(t *testing.T) {
	r := newMultiRig(t)
	if i, err := firstError(r.runMulti()); err != nil {
		t.Fatalf("call %d: %v", i, err)
	}
	if got := r.rec.names(); !reflect.DeepEqual(got, multiPoints) {
		t.Errorf("points reached:\n got %v\nwant %v", got, multiPoints)
	}
	for _, k := range r.rec.kinds {
		if k != txn.Normal {
			t.Errorf("a point of a Normal transaction was reported as kind %d", k)
		}
	}

	declared := map[fsx.Key]bool{}
	for _, k := range r.tab.Keys() {
		if k.Tag.Tx == "multi" {
			declared[k.Key] = true
		}
	}
	reached := map[fsx.Key]bool{}
	for _, p := range r.rec.points {
		reached[p.Key] = true
	}
	if !reflect.DeepEqual(declared, reached) {
		t.Errorf("declared keys and reached keys differ:\n declared %v\n reached  %v", declared, reached)
	}
}

// crashStates are the disks a crash at this instant can leave: the process
// dying, the worst power loss, and some seeded ones.
type crashState struct {
	name string
	mem  *fsxtest.Mem
}

func crashStates(m *fsxtest.Mem, rng *rand.Rand, seeded int) []crashState {
	out := []crashState{{"process-crash", m.Clone()}, {"worst-power-loss", m.CrashCloneWorst()}}
	for i := 0; i < seeded; i++ {
		out = append(out, crashState{fmt.Sprintf("power-loss-%d", i), m.CrashClone(rng)})
	}
	return out
}

// The crash states at every point of multi, taken by an observer before the
// step runs.
//
//   - Until the first deferred replacement is renamed (the commit point) the
//     disk holds nothing of the transaction but its prepared file and
//     temporary files.
//   - At every point of every kind of crash, whatever has appeared is whole,
//     and the replacement that follows the deferred group never appears
//     without the whole group: the directory sync between them is what makes
//     the group durable first.
//   - A restart removes the leftovers and changes nothing else.
func TestCrashStatesAtEveryPointOfAMultiWriteTransaction(t *testing.T) {
	r := newMultiRig(t)
	rng := rand.New(rand.NewPCG(7, 11))
	committed := false
	points := 0
	r.rec.on = func(p fsx.Point, _ txn.Kind) {
		points++
		for _, s := range crashStates(r.mem, rng, 6) {
			got := snapshot(t, s.mem)
			where := fmt.Sprintf("%s before %v", s.name, p)

			if !committed {
				for k, content := range got {
					switch k {
					case "a/", "b/":
					case "b/body":
						if content != multiFiles[k] {
							t.Errorf("%s: %s = %q, want it whole", where, k, content)
						}
					default:
						t.Errorf("%s: %s exists before the commit point", where, k)
					}
				}
			}

			for _, k := range []string{"b/body", "a/in-1", "a/in-2", "b/out"} {
				if content, ok := got[k]; ok && content != multiFiles[k] {
					t.Errorf("%s: %s = %q, want it whole", where, k, content)
				}
			}
			if _, out := got["b/out"]; out {
				for _, k := range []string{"a/in-1", "a/in-2"} {
					if _, ok := got[k]; !ok {
						t.Errorf("%s: b/out exists but %s, which was made durable before it, does not", where, k)
					}
				}
			}

			// A restart: the leftovers go and nothing else changes.
			restarted := txn.New(s.mem, r.tab, nil)
			if err := restarted.Recover(); err != nil {
				t.Fatalf("%s: Recover: %v", where, err)
			}
			if after := snapshot(t, s.mem); !reflect.DeepEqual(after, got) {
				t.Errorf("%s: Recover changed more than leftovers:\n before %v\n after  %v", where, got, after)
			}
			if left := leftovers(t, s.mem); len(left) != 0 {
				t.Errorf("%s: leftovers after Recover: %v", where, left)
			}
		}
		if p.String() == "multi/in.rename#1" {
			committed = true // the step that is about to run is the commit step
		}
	}

	if i, err := firstError(r.runMulti()); err != nil {
		t.Fatalf("call %d: %v", i, err)
	}
	if points != len(multiPoints) {
		t.Errorf("observed %d points, want %d", points, len(multiPoints))
	}
	if !committed {
		t.Error("the commit step was never reached")
	}
}

// Everything a Commit that returned nil acknowledged survives any power loss,
// whole.
func TestAcknowledgedWritesSurvivePowerLoss(t *testing.T) {
	r := newMultiRig(t)
	if i, err := firstError(r.runMulti()); err != nil {
		t.Fatalf("call %d: %v", i, err)
	}
	rng := rand.New(rand.NewPCG(3, 5))
	for _, s := range crashStates(r.mem, rng, 32) {
		got := snapshot(t, s.mem)
		for p, want := range multiFiles {
			if got[p] != want {
				t.Errorf("%s: %s = %q, want %q", s.name, p, got[p], want)
			}
		}
	}
}

// Nothing is acknowledged before Commit returns, and a transaction that fails
// before the commit point leaves nothing but prepared files: the writes of a
// failed transaction never become visible.
func TestFailureBeforeTheCommitPointLeavesOnlyPreparedFiles(t *testing.T) {
	for _, c := range []struct {
		role string
		step fsx.Step
		hit  int
	}{
		{"body", fsx.StepCreate, 1}, {"body", fsx.StepSync, 1}, {"body", fsx.StepRename, 1},
		{"in", fsx.StepCreate, 1}, {"in", fsx.StepWrite, 1}, {"in", fsx.StepSync, 1}, {"in", fsx.StepRename, 1},
	} {
		t.Run(fmt.Sprintf("%s.%v#%d", c.role, c.step, c.hit), func(t *testing.T) {
			var faulty *fsxtest.Faulty
			r := newRig(t, faultyOver(t, &faulty), multi)
			faulty.Fail(fsxtest.Match{Tx: "multi", Role: c.role, Step: c.step, Hit: c.hit}, syscall.EIO)
			if _, err := firstError(r.runMulti()); err == nil {
				t.Fatal("injected failure did not surface")
			}
			rng := rand.New(rand.NewPCG(1, 2))
			for _, s := range crashStates(r.mem, rng, 4) {
				for k := range snapshot(t, s.mem) {
					if k != "a/" && k != "b/" && k != "b/body" {
						t.Errorf("%s: %s exists after a failure before the commit point", s.name, k)
					}
				}
			}
		})
	}
}
