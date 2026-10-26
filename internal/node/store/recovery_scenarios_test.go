package store_test

import (
	"errors"
	"path"
	"strings"
	syncatomic "sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
)

// recoverRun is the workflow of a recovery scenario: the startup rules.
func recoverRun(t *testing.T, r *rig, s *sim) error {
	s.recoverAll()
	return nil
}

func find(t *testing.T, sc *store.Scan, name string) (out *store.OutRecord, in *store.InRecord, ctl *store.CtlRecord) {
	t.Helper()
	for i := range sc.Out {
		if sc.Out[i].Name() == name {
			out = &sc.Out[i]
		}
	}
	for i := range sc.In {
		if sc.In[i].Name() == name {
			in = &sc.In[i]
		}
	}
	for i := range sc.Ctl {
		if sc.Ctl[i].Name() == name {
			ctl = &sc.Ctl[i]
		}
	}
	return
}

// hasAlert reports whether the scan holds an alert of the kind for the file or
// record dir/name, which the alerts of sim.alert name as their subject.
func hasAlert(sc *store.Scan, dir, name, kind string) bool {
	for _, a := range sc.Alerts {
		if a.Subject == dir+"/"+name && a.Kind == kind {
			return true
		}
	}
	return false
}

// onlyAlert is hasAlert and no other alert: a settlement redone after a crash
// rewrites its alert and does not add a second.
func onlyAlert(sc *store.Scan, dir, name, kind string) bool {
	return len(sc.Alerts) == 1 && hasAlert(sc, dir, name, kind)
}

func floorOf(t *testing.T, sc *store.Scan, sender int, to ident.Address) uint64 {
	t.Helper()
	for _, s := range sc.Seq {
		if s.Sender == ep(sender) {
			return s.Floors[to]
		}
	}
	return 0
}

func recoveryScenarios() []scenario {
	pair := addr(2, 2)
	return []scenario{
		{
			name: "quarantine of a source record whose body is damaged",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				for i := 1; i <= 4; i++ {
					seedWithBodies(t, mem, srcOut(i, false))
				}
				seed(t, mem, store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{pair: 2}})
				damageBody(t, mem, path.Join(store.OutDir, srcOut(3, false).Name()))
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			// A settlement repeated after a crash raises the floor once more: a gap
			// grows, a number is never given twice. The floor file is not compared;
			// its range is.
			ignore: isIn(store.SeqDir),
			extra: func(t *testing.T, where string, crashed, fin map[string]string) {
				rec, dmg := store.DecodeRecord(store.SeqDir, store.SeqRecord{Sender: ep(1)}.Name(), []byte(fin[path.Join(store.SeqDir, store.SeqRecord{Sender: ep(1)}.Name())]))
				if dmg != nil {
					t.Errorf("%s: floor file: %+v", where, dmg)
					return
				}
				if got := rec.(store.SeqRecord).Floors[addr(2, 2)]; got < 5 || got > 9 {
					t.Errorf("%s: floor %d, want 5 (one settlement) up to a few more for repeated ones", where, got)
				}
			},
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				name := srcOut(3, false).Name()
				out, _, _ := find(t, sc, name)
				if out == nil || out.Lost == nil || len(out.Lost.Deliveries) != 1 || len(out.Feedback) != 1 {
					t.Fatalf("the damaged record was not overwritten by a lost record with one delivery and one feedback item: %#v", out)
				}
				if !onlyAlert(sc, store.OutDir, name, "corrupt_record") {
					t.Errorf("the alert is missing: %v", sc.Alerts)
				}
				// The lost record refers to no body, so the body the damaged record
				// owned is collected; the bodies of the others stay.
				wantNoBody(t, r, srcOut(3, false))
				for _, i := range []int{1, 2, 4} {
					wantBody(t, r, srcOut(i, false), bodyBytes(i))
				}
				wantBodyCount(t, r, 3)
				// m = max(floor 2, highest of the others 4): floor 5.
				if got := floorOf(t, sc, 1, pair); got != 5 {
					t.Errorf("floor = %d, want 5", got)
				}
				if got := store.NewSeqBook(sc).Next(ep(1), pair); got != 6 {
					t.Errorf("the next number = %d, want 6: the numbers 3 and 5 are never given again", got)
				}
			},
		},
		{
			name: "quarantine of a target record whose body is damaged",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seedWithBodies(t, mem, inRev(1, 1), inRev(2, 1))
				damageBody(t, mem, path.Join(store.InDir, inRev(1, 1).Name()))
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				name := inRev(1, 1).Name()
				if _, in, _ := find(t, sc, name); in != nil {
					t.Fatalf("the damaged record is still there: %#v", in)
				}
				if !onlyAlert(sc, store.InDir, name, "corrupt_record") || len(sc.Seq) != 0 {
					t.Errorf("a target record's quarantine has an alert and no sequence floor: %v %v", sc.Alerts, sc.Seq)
				}
				if _, other, _ := find(t, sc, inRev(2, 1).Name()); other == nil {
					t.Errorf("the healthy record was touched")
				}
				// The body of the record that is gone is an orphan; the other stays.
				wantNoBody(t, r, inRev(1, 1))
				wantBody(t, r, inRev(2, 1), bodyBytes(2))
				wantBodyCount(t, r, 1)
			},
		},
		controlQuarantine("quarantine of a control record whose body is damaged", damageBody),
		controlQuarantine("quarantine of a control record whose first line is damaged", damageHeader),
		unreadableSourceScenario(),
		{
			name: "quarantine of a floor file whose body is damaged",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seed(t, mem, srcOut(1, false), srcOut(2, false), store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{pair: 9}})
				damageBody(t, mem, path.Join(store.SeqDir, store.SeqRecord{Sender: ep(1)}.Name()))
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				name := store.SeqRecord{Sender: ep(1)}.Name()
				if got := floorOf(t, sc, 1, pair); got != 2 || !onlyAlert(sc, store.SeqDir, name, "corrupt_record") {
					t.Errorf("the floor file is treated as missing and overwritten with the highest number left (2), with an alert: floor %d alerts %v", got, sc.Alerts)
				}
			},
		},
		{
			name: "quarantine of a record of another kind",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seed(t, mem, directEndpoint(7, "chat"), store.CheckpointRecord{Key: store.CheckpointKey{Scope: "session", ID: ep(5).String()}, State: []byte(`{}`)})
				damageBody(t, mem, path.Join(store.EndpointsDir, directEndpoint(7, "chat").Name()))
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				name := directEndpoint(7, "chat").Name()
				if !onlyAlert(sc, store.EndpointsDir, name, "corrupt_record") || hasEndpoint(sc, ep(7)) || len(sc.Checkpoints) != 1 {
					t.Errorf("the damaged endpoint record must be alerted and gone, without a replacement: %v %v", sc.Alerts, sc.Endpoints)
				}
			},
		},
		{
			// A body that is not what its record says, found by a read: each record
			// has a body of its own, and each one whose body does not match is
			// settled by a call of its own, so a crash leaves any subset of them
			// settled, each file as before or after. A record with a good body is not
			// touched.
			name: "quarantine of the records whose body does not match",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				recs, healthy := mismatchRecords()
				seedWithBodies(t, mem, healthy)
				for _, rec := range recs {
					seed(t, mem, rec)
					seedBodyOf(t, mem, rec, []byte("tampered content"))
				}
			},
			mode: batch,
			run: func(t *testing.T, r *rig, s *sim) error {
				recs, healthy := mismatchRecords()
				for _, rec := range recs {
					if _, err := r.st.ReadBody(rec.Name(), *bodyHeld(rec)); !errors.Is(err, store.ErrBodyMismatch) {
						t.Fatalf("ReadBody of the tampered body of %s: %v, want ErrBodyMismatch", rec.Name(), err)
					}
				}
				if _, err := r.st.ReadBody(healthy.Name(), *bodyHeld(healthy)); err != nil {
					t.Fatalf("ReadBody of a good body: %v", err)
				}
				s.bodyMismatch()
				s.recoverAll()
				return nil
			},
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				_, healthy := mismatchRecords()
				if len(sc.Alerts) != 6 {
					t.Errorf("one alert for each record with a tampered body: %d alerts", len(sc.Alerts))
				}
				out, _, _ := find(t, sc, srcOut(1, false).Name())
				if out == nil || out.Lost == nil || len(out.Feedback) != 1 {
					t.Errorf("a source record still waiting for a receipt must become a lost record: %#v", out)
				}
				for _, n := range []int{2, 3, 5} {
					if _, in, _ := find(t, sc, inRev(n, 1).Name()); in == nil || in.Body != nil || in.Record.State != delivery.StateDelivered {
						t.Errorf("a delivered message must lose only the reference: %#v", in)
					}
				}
				if _, in, _ := find(t, sc, inRev(6, 1).Name()); in != nil {
					t.Errorf("a target record that needs the body is removed: %#v", in)
				}
				if _, _, c := find(t, sc, sessionCtl(4, 5).Name()); c == nil || c.Body != nil {
					t.Errorf("a control record must lose only the reference: %#v", c)
				}
				// Every tampered body is an orphan that goes, and the good one stays.
				if _, in, _ := find(t, sc, healthy.Name()); in == nil || in.Body == nil {
					t.Errorf("a record with a good body was settled: %#v", in)
				}
				wantBody(t, r, healthy, bodyBytes(7))
				wantBodyCount(t, r, 1)
			},
		},
		{
			// The notice was written and the crash came before the item it covers was
			// cleared: the replay clears it, once, whatever the crash left.
			name: "feedback replay of an out-lost record",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				a, b := lostNotice(1, 1, base), lostNotice(1, 2, base)
				seedWithBodies(t, mem, a, b, noticeForLost(a))
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				for _, o := range sc.OutLost {
					pending := len(o.Feedback)
					if (o.Name() == lostNotice(1, 1, base).Name()) == (pending != 0) {
						t.Errorf("%s: %d items pending: only the record no notice covers keeps its item", o.Name(), pending)
					}
				}
				if len(sc.OutLost) != 2 || len(sc.Alerts) != 0 {
					t.Errorf("out-lost %d alerts %d", len(sc.OutLost), len(sc.Alerts))
				}
			},
		},
		{
			// A body is kept exactly while the record it is named after holds a
			// reference to it: a body without a record, and the body a record that
			// holds none left behind, are collected.
			name: "orphan bodies",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seedWithBodies(t, mem, inRev(2, 1), ctlWithBody(5, 4), srcOut(6, false))
				seedBodyOf(t, mem, inRev(1, 1), bodyBytes(1)) // no record
				noRef := rejected(inRev(3, 1))
				seed(t, mem, noRef, finishedOut(4, false))
				seedBodyOf(t, mem, noRef, bodyBytes(3))                 // its record holds no reference
				seedBodyOf(t, mem, finishedOut(4, false), bodyBytes(4)) // nor does this one
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				var want []string
				for _, rec := range []store.Record{inRev(2, 1), ctlWithBody(5, 4), srcOut(6, false)} {
					want = append(want, store.BodyFileName(rec.Name()))
				}
				if strings.Join(sortedStrings(sc.Bodies), ",") != strings.Join(sortedStrings(want), ",") {
					t.Errorf("bodies = %v, want only those of the records that hold a reference: %v", sc.Bodies, want)
				}
			},
		},
		{
			name: "orphan session endpoints",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				noIntent := sessionCtl(6, 11)
				noIntent.Intent = nil
				seed(t, mem,
					session(5, store.EndpointCreating, ctlName(1)),                   // no control record: garbage
					session(6, store.EndpointCreating, ctlName(2)), sessionCtl(2, 6), // a control record: kept
					session(7, store.EndpointCreating, ctlName(3)), sessionCtl(3, 7), // its control record is damaged: kept, and closed with the record's quarantine
					session(8, store.EndpointOpen, ctlName(9)), // open: never an orphan
					// a control record that holds the creation of another endpoint, or none
					// (the intake that prepared these failed, and the signal was accepted again)
					session(9, store.EndpointCreating, ctlName(5)), sessionCtl(5, 10), session(10, store.EndpointCreating, ctlName(5)),
					session(11, store.EndpointCreating, ctlName(6)), noIntent)
				damageBody(t, mem, path.Join(store.CtlDir, sessionCtl(3, 7).Name()))
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				var ids []string
				for _, e := range sc.Endpoints {
					ids = append(ids, e.ID.String())
				}
				want := []string{ep(1).String(), ep(2).String(), ep(4).String(), ep(6).String(), ep(7).String(), ep(8).String(), ep(10).String()}
				if strings.Join(sortedStrings(ids), ",") != strings.Join(sortedStrings(want), ",") {
					t.Errorf("endpoints = %v, want only the orphan removed (%v)", ids, want)
				}
			},
		},
		{
			name: "effect of a control result not yet applied to its endpoint",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				c1 := created(t, 1, 5)
				seed(t, mem, session(5, store.EndpointCreating, ctlName(1)), c1)
				// a session closed by a control record
				c2 := closedBy(2, 6)
				seed(t, mem, session(6, store.EndpointOpen, ctlName(9)), c2)
				// the end of a creation whose endpoint is already closed or already open: nothing to do
				c3 := created(t, 3, 7)
				seed(t, mem, session(7, store.EndpointClosed, ctlName(3)), c3)
				c4 := created(t, 4, 8)
				op := openedBy(c4)
				seed(t, mem, op, c4)
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				by := map[ident.EndpointID]store.EndpointRecord{}
				for _, e := range sc.Endpoints {
					by[e.ID] = e
				}
				if e := by[ep(5)]; e.Status != store.EndpointOpen || e.Session == nil || e.Session.NativeRef != "native-5" || e.Token == nil {
					t.Errorf("a session whose creation ended must open: %#v", e)
				}
				if e := by[ep(6)]; e.Status != store.EndpointClosed || e.Token != nil {
					t.Errorf("a session a control record closed must close and lose its token: %#v", e)
				}
				if e := by[ep(7)]; e.Status != store.EndpointClosed {
					t.Errorf("a closed endpoint is never opened again: %#v", e)
				}
				if e := by[ep(8)]; e.Status != store.EndpointOpen {
					t.Errorf("an open endpoint was changed: %#v", e)
				}
			},
		},
		{
			name: "deliveries queued for closed endpoints",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				closed := directEndpoint(7, "chat")
				closed.Status, closed.Token = store.EndpointClosed, nil
				done := inFor(2, 7, 2)
				done.Record = deliveredRecord(done.Record)
				done.Body = nil
				c := sessionCtl(3, 5)
				c.Key.Recipient = ep(7)
				c.Body = bodyRef(3)
				c.Dedup = delivery.NewDedupRecord(c.Key.Dedup(), digestOf(3), c.Record.ReportBy)
				seedWithBodies(t, mem, closed, directEndpoint(8, "chat"), inFor(1, 7, 1), done, c, inFor(4, 9, 1), inFor(5, 8, 1))
				seedBodyOf(t, mem, done, bodyBytes(2)) // the file a crash left after the record dropped its reference
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				states := map[string]delivery.State{}
				for _, in := range sc.In {
					states[string(in.Key.Message)] = in.Record.State
				}
				for m, want := range map[string]delivery.State{"msg-0001": delivery.StateRejected, "msg-0002": delivery.StateDelivered, "msg-0004": delivery.StateRejected, "msg-0005": delivery.StateAccepted} {
					if states[m] != want {
						t.Errorf("%s is %v, want %v", m, states[m], want)
					}
				}
				// A control signal was handed to the adapter the moment it was accepted: a forced
				// close of it is "unknown", never "rejected".
				if len(sc.Ctl) != 1 || sc.Ctl[0].Record.State != delivery.StateDeliveryUnknown {
					t.Errorf("the control record for the closed endpoint must be closed as unknown: %#v", sc.Ctl)
				}
				if want := store.BodyFileName(inFor(5, 8, 1).Name()); len(sc.Bodies) != 1 || sc.Bodies[0] != want {
					t.Errorf("the bodies of the closed deliveries and of the record that dropped its reference must go, and the other stay: %v, want only %s", sc.Bodies, want)
				}
			},
		},
		{
			name: "endpoints of a disabled instance",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				closed := directEndpoint(8, "chat")
				closed.Status, closed.Token = store.EndpointClosed, nil
				seed(t, mem, directEndpoint(7, "chat"), closed, directEndpoint(9, "other"), inFor(1, 7, 1), inFor(2, 9, 1), ctlWithBody(4, 7))
				seedBodies(t, mem, inFor(1, 7, 1), inFor(2, 9, 1), ctlWithBody(4, 7))
			},
			enabled: func(inst string) bool { return inst != "chat" },
			mode:    atomic, mustComplete: true, run: recoverRun,
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				for _, e := range sc.Endpoints {
					want := e.Instance != "chat"
					if (e.Status == store.EndpointOpen) != want {
						t.Errorf("endpoint %s of %s is %v", e.ID, e.Instance, e.Status)
					}
				}
				if _, in, _ := find(t, sc, inFor(1, 7, 1).Name()); in == nil || in.Record.State != delivery.StateRejected {
					t.Errorf("a delivery queued for a closed endpoint of the instance must be closed")
				}
				if want := store.BodyFileName(inFor(2, 9, 1).Name()); len(sc.Bodies) != 1 || sc.Bodies[0] != want {
					t.Errorf("the bodies of the closed deliveries must go and the other stay: %v, want only %s", sc.Bodies, want)
				}
			},
		},
		{
			name: "feedback a notice already covers",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				a := srcOut(1, false)
				a.Feedback = []delivery.Item{feedbackItem(1), blockedItem(1)}
				b := srcOut(2, false)
				b.Feedback = []delivery.Item{feedbackItem(2)}
				c := srcOut(3, false)
				c.Feedback = []delivery.Item{feedbackItem(3)}
				cover := a
				cover.Feedback = []delivery.Item{feedbackItem(1)}
				n := noticeFor(cover)
				n.Covers = append(n.Covers, store.CoverRef{Out: b.Name(), Recipient: addr(2, 2), Kind: delivery.KindRejected})
				seedWithBodies(t, mem, a, b, c, n)
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				got := map[string]int{}
				for _, o := range sc.Out {
					got[string(o.Key.Message)] = len(o.Feedback)
				}
				if got["msg-0001"] != 1 || got["msg-0002"] != 0 || got["msg-0003"] != 1 {
					t.Errorf("pending feedback after the replay = %v, want 1, 0, 1: only the covered items go", got)
				}
				if len(sc.Bodies) != 4 {
					t.Errorf("every record still holds its body, so none is collected: %v", sc.Bodies)
				}
			},
		},
	}
}

func sortedStrings(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// mismatchRecords are the records whose bodies the mismatch scenario tampers
// with, and one record whose body is good. The target records of 2, 3 and 5 no
// longer need their bodies, the one of 6 still does, and so does the source
// record 1, which is seeded apart.
func mismatchRecords() (tampered []store.Record, healthy store.InRecord) {
	ctl := sessionCtl(4, 5)
	ctl.Body = bodyRef(4)
	in2, in3, in5 := inRev(2, 1), inRev(3, 1), inRev(5, 1)
	in2.Record, in3.Record, in5.Record = deliveredRecord(in2.Record), deliveredRecord(in3.Record), deliveredRecord(in5.Record)
	return []store.Record{srcOut(1, false), in2, in3, ctl, in5, inRev(6, 1)}, inRev(7, 1)
}

func blockedItem(i int) delivery.Item {
	return delivery.Item{
		Ref:      delivery.DeliveryRef{Sender: addr(local, 1), Message: msgID(i), Recipient: addr(2, 2)},
		Feedback: delivery.Feedback{Kind: delivery.KindDeliveryBlocked, Cause: delivery.CauseAdapterOffline}, At: base,
	}
}

// closedBy is a control record of a successful session.close of endpoint e.
func closedBy(i, e int) store.CtlRecord {
	c := sessionCtl(i, e)
	c.Kind = envelopeKindSessionClose()
	c.Preset, c.SessionEndpoint, c.Intent = "", ident.EndpointID{}, nil
	c.ClosedEndpoint = ep(e)
	c.Record = applied0(c.Record)
	return c
}

// controlQuarantine is the quarantine of a control record of a session creation
// whose endpoint is still being created, with a delivery derived from it.
func controlQuarantine(name string, damage func(testing.TB, *fsxtest.Mem, string)) scenario {
	return scenario{
		name: name,
		setup: func(t *testing.T, mem *fsxtest.Mem) {
			c := sessionCtl(1, 5)
			c.Body = bodyRef(1)
			derived := derivedIn(1, 5, 1)
			derived.Body = bodyRef(1)
			seedWithBodies(t, mem, session(5, store.EndpointCreating, ctlName(1)), c, derived)
			damage(t, mem, path.Join(store.CtlDir, c.Name()))
		},
		mode: atomic, mustComplete: true, run: recoverRun,
		verify: func(t *testing.T, r *rig) {
			sc := r.scan()
			name := sessionCtl(1, 5).Name()
			if !onlyAlert(sc, store.CtlDir, name, "control_unknown") {
				t.Errorf("the alert must say a native session may remain: %v", sc.Alerts)
			}
			if _, _, c := find(t, sc, name); c != nil {
				t.Errorf("the damaged control record is still there")
			}
			if _, _, c := find(t, sc, name); c != nil {
				t.Errorf("a control record has no replacement, yet one is there: %#v", c)
			}
			if e := endpointOf(sc, ep(5)); e == nil || e.Status != store.EndpointClosed || e.Token != nil {
				t.Errorf("the session endpoint still being created must be closed: %#v", e)
			}
			if _, d, _ := find(t, sc, derivedIn(1, 5, 1).Name()); d == nil || d.Record.State != delivery.StateRejected || d.Record.Reject != delivery.RejectEndpointClosed {
				t.Errorf("the delivery derived from the quarantined control record must be rejected as the endpoint closed: %#v", d)
			}
			if len(sc.Bodies) != 0 {
				t.Errorf("no record refers to a body any more, so both are collected: %v", sc.Bodies)
			}
		},
	}
}

func endpointOf(sc *store.Scan, id ident.EndpointID) *store.EndpointRecord {
	for i := range sc.Endpoints {
		if sc.Endpoints[i].ID == id {
			return &sc.Endpoints[i]
		}
	}
	return nil
}

func hasEndpoint(sc *store.Scan, id ident.EndpointID) bool { return endpointOf(sc, id) != nil }

// lostRuns numbers the runs of the workflow of unreadableSourceScenario, so that
// each takes the discovery time of its own.
var lostRuns syncatomic.Int64

// unreadableSourceScenario is the settlement of a source file whose first line
// cannot be read, which writes the out-lost record that tells its sender, raises
// every floor of the sender, raises the alert and only then removes the file. Each
// run of the workflow, and each recovery, finds the damage at another time, so a
// name that depended on the time would give a second record: what the walk
// compares is that exactly one record and one alert are there, not the time in
// them.
func unreadableSourceScenario() scenario {
	pair, other := addr(2, 2), addr(2, 3)
	victim := srcOut(2, false)
	damagedHash := strings.TrimSuffix(strings.TrimPrefix(victim.Name(), ep(1).String()+"."), ".rec")
	lostName := store.NewOutLostRecord(store.OutLostKey{Sender: ep(1), Damaged: damagedHash}, node(local), base).Name()
	// countKind counts the out-lost records or the alerts of a snapshot.
	countKind := func(snap map[string]string, kind store.Kind) (n int) {
		for p, content := range snap {
			dir, name := path.Split(p)
			dir = strings.TrimSuffix(dir, "/")
			if dir != store.OutDir && dir != store.AlertsDir {
				continue
			}
			rec, dmg := store.DecodeRecord(dir, name, []byte(content))
			if dmg != nil {
				continue
			}
			switch rec.(type) {
			case store.OutLostRecord:
				if kind == store.KindOutLost {
					n++
				}
			case store.AlertRecord:
				if kind == store.KindAlert {
					n++
				}
			}
		}
		return n
	}
	return scenario{
		name: "quarantine of a source file whose first line is unreadable",
		setup: func(t *testing.T, mem *fsxtest.Mem) {
			seed(t, mem, srcOut(1, false), victim, srcOut(4, false), store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{pair: 2, other: 7}})
			damageHeader(t, mem, path.Join(store.OutDir, victim.Name()))
		},
		mode: atomic, mustComplete: true,
		run: func(t *testing.T, r *rig, s *sim) error {
			s.now = s.now.Add(time.Duration(lostRuns.Add(1)) * time.Second)
			s.recoverAll()
			return nil
		},
		// What is compared is that the damaged file is gone, the healthy records are
		// as they were and exactly the record named after the file is there. Its
		// time, the alert's and the floors (a settlement repeated after a crash
		// raises them once more: a gap grows, a number is never given twice) are
		// checked by hand below.
		ignore: func(p string) bool {
			return strings.HasPrefix(p, store.AlertsDir+"/") || strings.HasPrefix(p, store.SeqDir+"/") || p == path.Join(store.OutDir, lostName)
		},
		crashed: func(t *testing.T, where string, snap map[string]string) {
			if n := countKind(snap, store.KindOutLost); n > 1 {
				t.Errorf("%s: %d out-lost records", where, n)
			}
			if n := countKind(snap, store.KindAlert); n > 1 {
				t.Errorf("%s: %d alerts", where, n)
			}
			if _, there := snap[path.Join(store.OutDir, victim.Name())]; there {
				return
			}
			// The file is gone: everything it was settled for is durable.
			if countKind(snap, store.KindOutLost) != 1 || countKind(snap, store.KindAlert) != 1 {
				t.Errorf("%s: the damaged file is gone without its out-lost record and its alert", where)
			}
			if f := floorsIn(t, where, snap); f[pair] < 5 || f[other] < 8 {
				t.Errorf("%s: the damaged file is gone and the floors are %v, want at least 5 and 8", where, f)
			}
		},
		extra: func(t *testing.T, where string, _, fin map[string]string) {
			if countKind(fin, store.KindOutLost) != 1 || countKind(fin, store.KindAlert) != 1 {
				t.Errorf("%s: want exactly one out-lost record and one alert, got %d and %d", where, countKind(fin, store.KindOutLost), countKind(fin, store.KindAlert))
			}
			if f := floorsIn(t, where, fin); f[pair] < 5 || f[pair] > 9 || f[other] < 8 || f[other] > 12 {
				t.Errorf("%s: floors %v, want 5 and 8 (one settlement) up to a few more for repeated ones", where, f)
			}
		},
		verify: func(t *testing.T, r *rig) {
			sc := r.scan()
			if len(sc.OutLost) != 1 || sc.OutLost[0].Name() != lostName || len(sc.OutLost[0].Feedback) != 1 || len(sc.Out) != 2 || len(sc.Damaged) != 0 {
				t.Fatalf("out-lost %#v, source records %d", sc.OutLost, len(sc.Out))
			}
			if !onlyAlert(sc, store.OutDir, victim.Name(), "corrupt_record") {
				t.Errorf("alerts %v", sc.Alerts)
			}
			if floorOf(t, sc, 1, pair) != 5 || floorOf(t, sc, 1, other) != 8 {
				t.Errorf("floors %d and %d, want 5 and 8", floorOf(t, sc, 1, pair), floorOf(t, sc, 1, other))
			}
		},
	}
}

// floorsIn is the floors of sender 1 in a snapshot of the disk.
func floorsIn(t *testing.T, where string, snap map[string]string) map[ident.Address]uint64 {
	t.Helper()
	name := store.SeqRecord{Sender: ep(1)}.Name()
	data, ok := snap[path.Join(store.SeqDir, name)]
	if !ok {
		return nil
	}
	rec, dmg := store.DecodeRecord(store.SeqDir, name, []byte(data))
	if dmg != nil {
		t.Errorf("%s: floor file: %+v", where, dmg)
		return nil
	}
	return rec.(store.SeqRecord).Floors
}
