package store_test

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

// operation is one call of the store with what it will write and remove, known
// before it runs, so that a crash in the middle of it can be judged: each file
// it touches is whole as before or whole as after.
type operation struct {
	name    string
	writes  map[string][]byte
	removes []string
	call    func() error
	// summaries are the archive summaries the plan of the call must name, by key.
	// They are best effort: a power loss may take them, so no clone is judged by them.
	summaries []string
	// floor is the number the call remembers for the pair of the fixtures: the
	// highest sequence number of the source records it removes.
	floor uint64
	// after runs once the call returned nil: the generator's own bookkeeping.
	after func()
}

// ledger is what was acknowledged: the content of every file whose write
// returned, and the floor the sweeps remembered.
type ledger struct {
	files map[string][]byte
	floor uint64
}

// powerLossWorld drives random operations over the store and judges power-loss
// clones against the ledger.
type powerLossWorld struct {
	t     *testing.T
	r     *rig
	rng   func(n int) int
	led   ledger
	stage map[int]int
	rev   map[int]uint64
	cur   *operation
	// damage and restarts turn on two more kinds of step, for the test that
	// compares the whole disk with the ledger.
	damage bool
	counts map[string]int
	stats  struct{ clones, mid, ahead, behind int }
}

const (
	messages = 10
	farAway  = 400 * 24 * time.Hour
)

var pairFixture = addr(local, 2)

func bodyPath(i int) string { return path.Join(store.BodiesDir, bodyRef(i).Name()) }

func (w *powerLossWorld) enc(rec store.Record) []byte {
	w.t.Helper()
	return mustEncode(w.t, rec)
}

// next picks an operation that is possible in the current state.
func (w *powerLossWorld) next() *operation {
	r := w.r
	i := 1 + w.rng(messages)
	switch w.rng(12) {
	case 0, 1, 2:
		switch w.stage[i] {
		case 0:
			out := srcOut(i, true)
			return &operation{name: "intake", call: func() error { return r.st.Intake(bodyBytes(i), out) },
				writes: map[string][]byte{path.Join(store.OutDir, out.Name()): w.enc(out), bodyPath(i): bodyBytes(i)},
				after:  func() { w.stage[i] = 1 }}
		case 1:
			in := tgtIn(i)
			return &operation{name: "deliver", call: func() error { return dropWritten(r.st.Deliver(nil, []store.InRecord{in})) },
				writes: map[string][]byte{path.Join(store.InDir, in.Name()): w.enc(withRev(in, 1))},
				after:  func() { w.stage[i], w.rev[i] = 2, 1 }}
		case 2:
			in := w.leased(i)
			return &operation{name: "lease", call: func() error { return dropWritten(r.st.Lease([]store.InRecord{in})) },
				writes: map[string][]byte{path.Join(store.InDir, in.Name()): w.enc(withRev(in, in.Rev+1))},
				after:  func() { w.stage[i] = 3; w.rev[i]++ }}
		case 3:
			in := w.leased(i)
			in.Record, in.Body = deliveredRecord(in.Record), nil
			return &operation{name: "ack", call: func() error { return dropWritten(r.st.Ack([]store.InRecord{in}, nil)) },
				writes: map[string][]byte{path.Join(store.InDir, in.Name()): w.enc(withRev(in, in.Rev+1))},
				after:  func() { w.stage[i] = 4; w.rev[i]++ }}
		case 4:
			out := srcOut(i, true)
			d, _, err := out.Outbound.Recipients[0].Delivery.Apply(base, delivery.Event{Type: delivery.EventDelivered})
			if err != nil {
				w.t.Fatal(err)
			}
			out.Outbound.Recipients[0].Delivery, out.Body = d, nil
			return &operation{name: "source-update", call: func() error { return r.st.SourceUpdate(out, []store.BodyRef{*bodyRef(i)}) },
				writes:  map[string][]byte{path.Join(store.OutDir, out.Name()): w.enc(out)},
				removes: []string{bodyPath(i)},
				after:   func() { w.stage[i] = 5 }}
		}
	case 3:
		e := directEndpoint(1+w.rng(6), "chat")
		e.Declaration = []byte(fmt.Sprintf(`{"name":"n%d"}`, w.rng(1000)))
		return &operation{name: "endpoint-update", call: func() error { return r.st.EndpointUpdate(e) },
			writes: map[string][]byte{path.Join(store.EndpointsDir, e.Name()): w.enc(e)}}
	case 4:
		c := store.CheckpointRecord{Key: store.CheckpointKey{Scope: "session", ID: ep(1 + w.rng(4)).String()}, State: []byte(fmt.Sprintf(`{"cursor":%d}`, w.rng(100)))}
		return &operation{name: "checkpoint-put", call: func() error { return r.st.CheckpointPut(c) },
			writes: map[string][]byte{path.Join(store.CheckpointsDir, c.Name()): w.enc(c)}}
	case 5:
		a := store.AlertRecord{ID: store.AlertID{byte(1 + w.rng(5))}, Kind: "version_untested", At: base, Detail: fmt.Sprintf("d%d", w.rng(100))}
		return &operation{name: "alert-raise", call: func() error { return r.st.AlertRaise(a) },
			writes: map[string][]byte{path.Join(store.AlertsDir, a.Name()): w.enc(a)}}
	case 6:
		return &operation{name: "archive-events", call: func() error {
			return r.st.ArchiveEvents(base.Add(time.Duration(w.rng(3))*24*time.Hour), []store.ArchiveEvent{eventAccepted(), eventChunk()})
		}}
	case 7:
		res := adminResult(1 + w.rng(5))
		return &operation{name: "daemon-stop", call: func() error { return r.st.DaemonStop(res) },
			writes: map[string][]byte{path.Join(store.AdminDir, res.Name()): w.enc(res)}}
	case 8:
		e := directEndpoint(1+w.rng(6), "chat")
		id := fmt.Sprintf("config/ids-%d", w.rng(5))
		return &operation{name: "cross-owner", writes: map[string][]byte{id: []byte("ids"), path.Join(store.EndpointsDir, e.Name()): w.enc(e)},
			call: func() error {
				pe, err := store.PrepareEndpoint(e)
				if err != nil {
					return err
				}
				tx := r.w.Begin(&declXAdmit)
				defer tx.Close()
				if err := tx.Replace("ids", id, []byte("ids"), fsx.ModePrivate); err != nil {
					return err
				}
				if err := store.PutEndpoint(tx, store.RoleEndpoint, pe); err != nil {
					return err
				}
				return tx.Commit()
			}}
	case 9:
		return w.sweep()
	case 10:
		if w.damage {
			return w.damageOp()
		}
	case 11:
		if w.damage {
			return &operation{name: "restart", call: func() error {
				w.r = openRig(w.t, w.r.mem.CrashCloneWorst())
				return nil
			}}
		}
	}
	return nil
}

// damageOp damages the body of a target record that is on the disk, as the
// disk may while the node is stopped, and runs the startup rules over it: the
// record is alerted and removed, and nothing replaces it: a target that
// lost a record keeps none.
func (w *powerLossWorld) damageOp() *operation {
	if w.rng(6) != 0 {
		return nil // rare: most messages live a full life
	}
	r := w.r
	for off := 0; off < messages; off++ {
		i := 1 + (w.rng(messages)+off)%messages
		if st := w.stage[i]; st < 2 || st > 4 {
			continue
		}
		in := tgtIn(i)
		name := in.Name()
		p := path.Join(store.InDir, name)
		if _, there := w.led.files[p]; !there {
			continue // swept already
		}
		damaged := append([]byte(nil), w.led.files[p]...)
		damaged[len(damaged)-2] ^= 1
		s := newSim(w.t, r.st)
		id := alertFor(store.InDir, name)
		alert := s.alert(id, "corrupt_record", store.InDir+"/"+name)
		return &operation{
			name: "damage",
			call: func() error {
				if err := fsx.Replace(r.mem, setupTag, p, damaged, fsx.ModePrivate); err != nil {
					return err
				}
				s.recoverAll()
				return nil
			},
			writes: map[string][]byte{
				path.Join(store.AlertsDir, alert.Name()): w.enc(alert),
			},
			removes: []string{p},
			after:   func() { w.stage[i] = 7 },
		}
	}
	return nil
}

func withRev(in store.InRecord, rev uint64) store.InRecord { in.Rev = rev; return in }

// leased is the target record of message i as the ledger holds it, leased.
func (w *powerLossWorld) leased(i int) store.InRecord {
	in := inRev(i, w.rev[i])
	in.Key.Recipient = ep(2)
	in.Dedup = delivery.NewDedupRecord(in.Key.Dedup(), digestOf(i), in.Record.ReportBy)
	in.Record = leasedRecord(in.Record, base)
	if w.stage[i] == 3 {
		in.Record.State = delivery.StateAccepted
	}
	return in
}

// sweep removes whatever the model says is finished, long after every
// retention. What goes, the summaries and the floor are derived here from the
// stages of the messages and the fixtures, never from the store's own plan: the
// plan is what the call below uses, and the plan is checked against this.
//
// A target record is finished once acknowledged (stage 4 and after); one that
// was quarantined (stage 7) is no longer there, and its alert stays for the owner
// until it is acknowledged. A source record is finished once
// its delivery was reported (stage 5). Administrative
// results are past their seven days. Nothing else is finished in this world: the
// alerts are not acknowledged, the endpoints are open, and the bodies a swept
// record referred to were released by the update that finished it.
func (w *powerLossWorld) sweep() *operation {
	r := w.r
	now := base.Add(farAway)
	op := &operation{name: "archive-sweep", writes: map[string][]byte{}}
	has := func(p string) bool { _, ok := w.led.files[p]; return ok }
	swept := map[int]bool{}
	for i := 1; i <= messages; i++ {
		in, out := tgtIn(i), srcOut(i, true)
		inFile, outFile := path.Join(store.InDir, in.Name()), path.Join(store.OutDir, out.Name())
		switch stage := w.stage[i]; {
		case has(inFile) && (stage == 4 || stage == 5):
			op.removes = append(op.removes, inFile)
			op.summaries = append(op.summaries, tgtKey(i).String())
		}
		if has(outFile) && w.stage[i] == 5 {
			kb, err := out.Key.Bytes()
			if err != nil {
				w.t.Fatal(err)
			}
			op.removes = append(op.removes, outFile)
			op.summaries = append(op.summaries, string(kb))
			swept[i] = true
			op.floor = max(op.floor, uint64(i)) // srcOut numbers message i with i
		}
	}
	for p := range w.led.files {
		if strings.HasPrefix(p, store.AdminDir+"/") {
			op.removes = append(op.removes, p)
		}
	}
	if len(op.removes) == 0 {
		return nil
	}
	if op.floor > 0 {
		seqFile := path.Join(store.SeqDir, store.SeqRecord{Sender: ep(1)}.Name())
		op.writes[seqFile] = w.enc(store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{pairFixture: max(op.floor, w.led.floor)}})
	}
	op.after = func() {
		for i := range swept {
			w.stage[i] = 6
		}
	}
	op.call = func() error {
		plan := store.PlanSweep(r.scan(), now)
		var planned []string
		for _, group := range [][]store.FileRef{plan.Ctl, plan.Records, plan.Endpoints} {
			for _, f := range group {
				planned = append(planned, path.Join(f.Dir, f.Name))
			}
		}
		for _, o := range plan.Outs {
			planned = append(planned, path.Join(store.OutDir, o.Name()))
		}
		for _, b := range plan.Bodies {
			planned = append(planned, path.Join(store.BodiesDir, b.Name()))
		}
		var keys []string
		for _, sm := range plan.Summaries {
			keys = append(keys, sm.Key)
		}
		for _, c := range []struct {
			what      string
			got, want []string
		}{{"files", planned, op.removes}, {"summaries", keys, op.summaries}} {
			got, want := append([]string(nil), c.got...), append([]string(nil), c.want...)
			sort.Strings(got)
			sort.Strings(want)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				w.t.Errorf("the sweep plan does not match what the model says is finished (%s):\n plan  %v\n model %v", c.what, got, want)
			}
		}
		return r.st.ArchiveSweep(plan.Chunk(now))
	}
	return op
}

// commit applies an acknowledged operation to the ledger and the stages.
func (w *powerLossWorld) commit(op *operation) {
	for p, b := range op.writes {
		w.led.files[p] = b
	}
	for _, p := range op.removes {
		delete(w.led.files, p)
	}
	if op.floor > w.led.floor {
		w.led.floor = op.floor
	}
}

// judge restarts over a clone and checks the ledger against it.
func (w *powerLossWorld) judge(where string, clone *fsxtest.Mem) {
	t := w.t
	t.Helper()
	w.stats.clones++
	r := openRig(t, clone)
	sc := r.scan()
	if len(sc.Damaged) != 0 || len(sc.Unknown) != 0 {
		t.Errorf("%s: a record is half written: damaged %v, unknown %v", where, sc.Damaged, sc.Unknown)
	}
	got := snapshot(t, clone, ".")
	cur := w.cur
	for p, want := range w.led.files {
		have, ok := got[p]
		inflightWant, touched := []byte(nil), false
		if cur != nil {
			inflightWant, touched = cur.writes[p]
			for _, rm := range cur.removes {
				if rm == p {
					touched = true
					if !ok {
						w.stats.ahead++
						continue
					}
				}
			}
		}
		switch {
		case !ok && (cur == nil || !touched):
			t.Errorf("%s: acknowledged file %s is gone", where, p)
		case ok && string(have) != string(want) && !(touched && string(have) == string(inflightWant)):
			t.Errorf("%s: acknowledged file %s has other content than the last write returned for it", where, p)
		case ok && touched && string(have) == string(inflightWant) && string(have) != string(want):
			w.stats.ahead++
		}
	}
	for p, have := range got {
		if _, acked := w.led.files[p]; acked {
			continue
		}
		if strings.HasPrefix(p, store.ArchiveDir+"/") {
			continue
		}
		if cur != nil {
			if want, ok := cur.writes[p]; ok && string(have) == string(want) {
				w.stats.ahead++
				continue
			}
		}
		t.Errorf("%s: %s exists and nothing acknowledged it, nor is it the whole result of the operation in flight", where, p)
	}
	// What an operation in flight did not reach stays as it was.
	if cur != nil {
		for p := range cur.writes {
			if _, ok := got[p]; !ok {
				w.stats.behind++
			}
		}
	}
	// The floor of the pair is at least the highest number swept, and the next number is above it.
	if w.led.floor > 0 {
		book := store.NewSeqBook(sc)
		if next := book.Next(ep(1), pairFixture); next <= w.led.floor {
			t.Errorf("%s: the next sequence number is %d, but %d was used before a sweep removed its record", where, next, w.led.floor)
		}
	}
	// The index can be rebuilt: the scan decoded every record, and when nothing
	// was in flight an engine finds no recovery work in it.
	if cur == nil {
		noWork(t, where, sc, func(string) bool { return true })
	}
}

func TestPowerLossCloneKeepsAcknowledgedWrites(t *testing.T) {
	var total struct{ clones, mid, ahead, behind int }
	counts := map[string]int{}
	for seed := uint64(1); seed <= 6; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			r := newRig(t)
			rng := newRand(seed)
			w := &powerLossWorld{
				t: t, r: r, rng: func(n int) int { return rng.IntN(n) },
				led:   ledger{files: map[string][]byte{}},
				stage: map[int]int{}, rev: map[int]uint64{}, counts: counts,
			}
			// The endpoint the messages are for exists: a record for an endpoint with
			// no record is work for the drain rule.
			e2 := directEndpoint(2, "chat")
			mustDo(t, r.st.EndpointUpdate(e2))
			w.led.files[path.Join(store.EndpointsDir, e2.Name())] = w.enc(e2)
			clones := newRand(seed + 1000)
			r.rec.on = func(p fsx.Point, k txn.Kind) {
				if w.cur == nil || rng.IntN(3) != 0 {
					return
				}
				w.stats.mid++
				for _, cs := range []struct {
					n string
					m *fsxtest.Mem
				}{{"worst", r.mem.CrashCloneWorst()}, {"seeded", r.mem.CrashClone(clones)}, {"process", r.mem.Clone()}} {
					w.judge(fmt.Sprintf("%s power loss before %v during %s", cs.n, p, w.cur.name), cs.m)
				}
			}
			for step := 0; step < 150; step++ {
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
				for _, m := range []*fsxtest.Mem{r.mem.CrashCloneWorst(), r.mem.CrashClone(clones)} {
					w.judge(fmt.Sprintf("power loss after step %d (%s)", step, op.name), m)
				}
			}
			total.clones += w.stats.clones
			total.mid += w.stats.mid
			total.ahead += w.stats.ahead
			total.behind += w.stats.behind
		})
	}
	var names []string
	for n := range counts {
		names = append(names, n)
	}
	sort.Strings(names)
	t.Logf("%d clones judged, %d of them in the middle of an operation; an operation in flight was whole in %d judgments and absent in %d; operations: %v", total.clones, total.mid, total.ahead, total.behind, counts)
	for _, n := range []string{"intake", "deliver", "lease", "ack", "source-update", "endpoint-update", "checkpoint-put", "alert-raise", "archive-events", "daemon-stop", "cross-owner", "archive-sweep"} {
		if counts[n] < 6 {
			t.Errorf("operation %s ran %d times in all, want at least 6: the generator does not cover it", n, counts[n])
		}
	}
	if total.clones < 3000 || total.mid < 500 {
		t.Errorf("judged %d clones, %d in the middle of an operation: too few to mean anything", total.clones, total.mid)
	}
	if total.ahead == 0 || total.behind == 0 {
		t.Errorf("an operation in flight was whole %d times and absent %d times: both outcomes must be seen", total.ahead, total.behind)
	}
}
