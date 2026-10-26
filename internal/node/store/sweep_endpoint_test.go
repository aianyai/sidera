package store_test

import (
	"fmt"
	"path"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
)

// finishedOutOf is a finished source record sent by the endpoint numbered sender.
func finishedOutOf(sender, i int) store.OutRecord {
	o := srcOutFinished(i)
	o.Key.Sender, o.Outbound.From = ep(sender), ep(sender)
	return o
}

// outRetention is when the finished source record of fixture i may go, written
// out by hand: its report_by is three hours and two minutes after its
// accept_before (i minutes after base), and what a reply may read is kept for the
// clock tolerance and a day after that.
func outRetention(i int) time.Time {
	return base.Add(time.Duration(i) * time.Minute).Add(3*time.Hour + 2*time.Minute).Add(24*time.Hour + 2*time.Minute)
}

// sweepOnce plans and runs one sweep at now, as the engine's pass does.
func sweepOnce(t testing.TB, r *rig, now time.Time) store.SweepPlan {
	t.Helper()
	plan := store.PlanSweep(r.scan(), now)
	mustDo(t, r.st.ArchiveSweep(plan.Chunk()))
	return plan
}

func countOf(sc *store.Scan) (endpoints, outs, floors int) {
	return len(sc.Endpoints), len(sc.Out), len(sc.Seq)
}

// A closed session endpoint is not swept while a source record has it as sender:
// the sweep of that record writes the floor of its sender, and a floor file
// written after the endpoint's was removed with it would stay forever. The
// endpoint, its floor file and what hangs on it go together, in the sweep after
// the one that removed the last of its source records, and the floors of the
// records are written before they go as for any other sender.
func TestClosedSessionEndpointAndItsFloorFileGoOnceItsLastSourceRecordHasGone(t *testing.T) {
	pair := addr(2, 2)
	r := newRig(t)
	early, late := finishedOutOf(5, 1), finishedOutOf(5, 200)
	seed(t, r.mem, session(5, store.EndpointClosed, ctlName(4)), early, late,
		store.AttemptsRecord{Endpoint: ep(5)},
		store.CheckpointRecord{Key: store.CheckpointKey{Scope: "session", ID: ep(5).String()}, State: []byte(`{}`)},
		store.SeqRecord{Sender: ep(5), Floors: map[ident.Address]uint64{pair: 1}})
	expect := func(when string, endpoints, outs, floors int) {
		t.Helper()
		e, o, f := countOf(r.scan())
		if e != endpoints || o != outs || f != floors {
			t.Fatalf("%s: endpoints/out/seq = %d/%d/%d, want %d/%d/%d", when, e, o, f, endpoints, outs, floors)
		}
	}
	expect("at the start", 1, 2, 1)

	// The first record expires; the second is still there and keeps the endpoint.
	sweepOnce(t, r, outRetention(1))
	expect("after the first record expired", 1, 1, 1)
	if got := floorOf(t, r.scan(), 5, pair); got != 1 {
		t.Errorf("floor = %d, want 1: the number of the record that went", got)
	}

	// The second expires. Its floor is written before it goes, and the endpoint
	// stays for this pass: the plan was made with the record still there.
	plan := sweepOnce(t, r, outRetention(200))
	expect("after the last record expired", 1, 0, 1)
	if len(plan.Endpoints) != 0 {
		t.Errorf("the endpoint was planned for the sweep that removes its last source record: %v", plan.Endpoints)
	}
	if got := floorOf(t, r.scan(), 5, pair); got != 200 {
		t.Errorf("floor = %d, want 200: the number of the last record that went", got)
	}

	// The next sweep takes the endpoint, what hangs on it and its floor file,
	// and writes no floor for it.
	plan = sweepOnce(t, r, outRetention(200))
	expect("after the sweep that follows", 0, 0, 0)
	if sc := r.scan(); len(sc.Attempts)+len(sc.Checkpoints) != 0 {
		t.Errorf("files of the endpoint are left: %v %v", sc.Attempts, sc.Checkpoints)
	}
	if len(plan.Outs) != 0 {
		t.Errorf("source records were swept again: %v", plan.Outs)
	}
	if again := store.PlanSweep(r.scan(), outRetention(200)); len(again.Records)+len(again.Outs)+len(again.Endpoints)+len(again.Ctl) != 0 {
		t.Errorf("a sweep of nothing has work: %+v", again)
	}
}

// Whatever has the endpoint as sender keeps it: a record whole, queued, lost or
// damaged with a key that can be trusted, and so does a native session that is not
// recorded released, which the node is still to ask its adapter to release. A closed
// endpoint of another kind is not the case: its floor file is not removed with it.
func TestPlanSweepKeepsAClosedSessionEndpointWhileAnySourceRecordOfItIsThere(t *testing.T) {
	closedSession := session(5, store.EndpointClosed, ctlName(4))
	closedDirect := directEndpoint(5, "chat")
	closedDirect.Status, closedDirect.Token = store.EndpointClosed, nil
	damaged := finishedOutOf(5, 1)
	holding := closedSession
	holding.Session = &store.SessionInfo{Preset: "default", NativeRef: "native-5"}
	released := holding
	released.Session = &store.SessionInfo{Preset: "default", NativeRef: "native-5", Released: true}
	for _, c := range []struct {
		rule     string
		endpoint store.EndpointRecord
		seed     func(t *testing.T, r *rig)
		swept    bool
	}{
		{"nothing sent by it", closedSession, func(t *testing.T, r *rig) {}, true},
		{"a finished source record", closedSession, func(t *testing.T, r *rig) { seed(t, r.mem, finishedOutOf(5, 1)) }, false},
		{"a source record that is not finished", closedSession, func(t *testing.T, r *rig) { seed(t, r.mem, srcOutOf(5, 1)) }, false},
		{"the replacement of a lost source record", closedSession, func(t *testing.T, r *rig) {
			k := finishedOutOf(5, 1).Key
			seed(t, r.mem, lostOutAt(k, base))
		}, false},
		{"a damaged source record whose key can be trusted", closedSession, func(t *testing.T, r *rig) {
			seed(t, r.mem, damaged)
			damageBody(t, r.mem, path.Join(store.OutDir, damaged.Name()))
		}, false},
		{"a source record of another sender", closedSession, func(t *testing.T, r *rig) { seed(t, r.mem, finishedOutOf(6, 1)) }, true},
		{"a native session that is not released", holding, func(t *testing.T, r *rig) {}, false},
		{"a native session that is released", released, func(t *testing.T, r *rig) {}, true},
		{"a closed endpoint of another kind", closedDirect, func(t *testing.T, r *rig) { seed(t, r.mem, finishedOutOf(5, 1)) }, true},
	} {
		t.Run(c.rule, func(t *testing.T) {
			r := newRig(t)
			seed(t, r.mem, c.endpoint)
			c.seed(t, r)
			plan := store.PlanSweep(r.scan(), outRetention(1000))
			if got := len(plan.Endpoints) == 1; got != c.swept {
				t.Errorf("endpoint planned = %v, want %v", got, c.swept)
			}
		})
	}
}

// srcOutOf is a queued source record sent by the endpoint numbered sender.
func srcOutOf(sender, i int) store.OutRecord {
	o := srcOut(i, false)
	o.Key.Sender, o.Outbound.From = ep(sender), ep(sender)
	return o
}

// sweepClosedSessionScenario is the sweep of a closed session endpoint and the
// source records it sent: the first pass removes the records after writing the
// floors of their numbers, the next one the endpoint with what hangs on it and
// its floor file. A crash anywhere must leave nothing that no later sweep would
// remove, and the passes the engine repeats must end with the disk empty of all
// of it.
func sweepClosedSessionScenario(farFuture time.Time) scenario {
	passes := func(r *rig) error {
		for i := 0; i < 4; i++ {
			plan := store.PlanSweep(r.scan(), farFuture)
			if len(plan.Records)+len(plan.Outs)+len(plan.Endpoints)+len(plan.Ctl) == 0 {
				return nil
			}
			if err := r.st.ArchiveSweep(plan.Chunk()); err != nil {
				return err
			}
		}
		return fmt.Errorf("the sweep did not settle in 4 passes")
	}
	return scenario{
		name: "archive-sweep of a closed session endpoint and its source records",
		setup: func(t *testing.T, mem *fsxtest.Mem) {
			seed(t, mem, session(5, store.EndpointClosed, ctlName(4)), finishedOutOf(5, 1), finishedOutOf(5, 2),
				store.AttemptsRecord{Endpoint: ep(5)},
				store.CheckpointRecord{Key: store.CheckpointKey{Scope: "session", ID: ep(5).String()}, State: []byte(`{}`)},
				store.SeqRecord{Sender: ep(5), Floors: map[ident.Address]uint64{addr(2, 2): 1}})
		},
		mode: batch, mustComplete: true,
		run: func(t *testing.T, r *rig, s *sim) error { return passes(r) },
		retry: func(t *testing.T, r *rig, s *sim) {
			mustDo(t, passes(r))
		},
		verify: func(t *testing.T, r *rig) {
			sc := r.scan()
			if _, o, f := countOf(sc); hasEndpoint(sc, ep(5)) || o+f != 0 || len(sc.Attempts)+len(sc.Checkpoints) != 0 {
				t.Errorf("endpoint 5 there %v, out %d, seq %d, attempts %d, checkpoints %d, want all gone", hasEndpoint(sc, ep(5)), o, f, len(sc.Attempts), len(sc.Checkpoints))
			}
		},
		crashed: func(t *testing.T, where string, snap map[string]string) {
			endpoint := path.Join(store.EndpointsDir, session(5, store.EndpointClosed, "").Name())
			dependents := []string{
				path.Join(store.AttemptsDir, store.AttemptsRecord{Endpoint: ep(5)}.Name()),
				path.Join(store.CheckpointsDir, store.CheckpointRecord{Key: store.CheckpointKey{Scope: "session", ID: ep(5).String()}}.Name()),
				path.Join(store.SeqDir, store.SeqRecord{Sender: ep(5)}.Name()),
			}
			outsLeft := 0
			var gone []string
			for i := 1; i <= 2; i++ {
				o := finishedOutOf(5, i)
				if _, there := snap[path.Join(store.OutDir, o.Name())]; there {
					outsLeft++
					continue
				}
				gone = append(gone, o.Name())
			}
			if _, there := snap[endpoint]; !there {
				// The endpoint record is the last to go: nothing is left that only
				// it identifies, and no source record of it is.
				for _, d := range dependents {
					if _, left := snap[d]; left {
						t.Errorf("%s: the endpoint record is gone and %s, which hangs on it, is left for no sweep to remove", where, d)
					}
				}
				if outsLeft != 0 {
					t.Errorf("%s: the endpoint record is gone while %d of its source records are still there", where, outsLeft)
				}
			}
			// A source record that is gone left its floor durable.
			if len(gone) > 0 {
				seqFile := path.Join(store.SeqDir, store.SeqRecord{Sender: ep(5)}.Name())
				data, there := snap[seqFile]
				if !there {
					// The floor file goes with the endpoint, in the sweep after the
					// last source record's: never while one of them is still there.
					if outsLeft != 0 {
						t.Errorf("%s: a source record is gone and so is the floor file of its sender, while %d of its source records are still there", where, outsLeft)
					}
					return
				}
				rec, dmg := store.DecodeRecord(store.SeqDir, store.SeqRecord{Sender: ep(5)}.Name(), []byte(data))
				if dmg != nil {
					t.Errorf("%s: floor file damaged: %+v", where, dmg)
					return
				}
				if got := rec.(store.SeqRecord).Floors[addr(2, 2)]; got < 1 {
					t.Errorf("%s: a source record is gone and the floor is %d", where, got)
				}
			}
		},
	}
}
