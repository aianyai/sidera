package store_test

import (
	"path"
	"strings"
	"testing"

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

func corruptName(rec store.Record, id store.AlertID) string {
	return rec.Name() + "." + id.String() + ".corrupt"
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

func hasAlert(sc *store.Scan, id store.AlertID, kind string) bool {
	for _, a := range sc.Alerts {
		if a.ID == id && a.Kind == kind {
			return true
		}
	}
	return false
}

func hasCorrupt(sc *store.Scan, dir, original string, id store.AlertID) bool {
	for _, c := range sc.Corrupt {
		if c.Dir == dir && c.Original == original && c.Alert == id {
			return true
		}
	}
	return false
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
					seed(t, mem, srcOut(i, false))
				}
				seed(t, mem, store.SeqRecord{Sender: ep(1), Floors: map[ident.Address]uint64{pair: 2}})
				damageBody(t, mem, path.Join(store.OutDir, srcOut(3, false).Name()))
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			// A quarantine resumed after a crash raises the floor once more: a gap
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
					t.Errorf("%s: floor %d, want 5 (one quarantine) up to a few more for resumed ones", where, got)
				}
			},
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				name := srcOut(3, false).Name()
				id := alertFor(store.OutDir, name)
				out, _, _ := find(t, sc, name)
				if out == nil || out.Lost == nil || len(out.Lost.Deliveries) != 1 || len(out.Feedback) != 1 {
					t.Fatalf("the damaged record was not replaced by a lost record with one delivery and one feedback item: %#v", out)
				}
				if !hasCorrupt(sc, store.OutDir, name, id) || !hasAlert(sc, id, "corrupt_record") {
					t.Errorf("the quarantined file or its alert is missing: %v %v", sc.Corrupt, sc.Alerts)
				}
				// m = max(floor 2, highest of the others 4), one record lost: floor 5.
				if got := floorOf(t, sc, 1, pair); got != 5 {
					t.Errorf("floor = %d, want 5", got)
				}
				if got := store.NewSeqBook(sc).Next(ep(1), pair); got != 6 {
					t.Errorf("the next number = %d, want 6: the number 3 and 5 are never given again", got)
				}
			},
		},
		{
			name: "quarantine of a target record whose body is damaged",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seed(t, mem, inRev(1, 1), inRev(2, 1))
				damageBody(t, mem, path.Join(store.InDir, inRev(1, 1).Name()))
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				name := inRev(1, 1).Name()
				_, in, _ := find(t, sc, name)
				if in == nil || !in.Lost || in.Record.State != delivery.StateDeliveryUnknown {
					t.Fatalf("the damaged record was not replaced by a lost record: %#v", in)
				}
				if !hasCorrupt(sc, store.InDir, name, alertFor(store.InDir, name)) || len(sc.Seq) != 0 {
					t.Errorf("a target record's quarantine has a file, an alert and no sequence floor: %v %v", sc.Corrupt, sc.Seq)
				}
				if _, other, _ := find(t, sc, inRev(2, 1).Name()); other == nil || other.Lost {
					t.Errorf("the healthy record was touched")
				}
			},
		},
		controlQuarantine("quarantine of a control record whose body is damaged", damageBody, true),
		controlQuarantine("quarantine of a control record whose first line is damaged", damageHeader, false),
		{
			name: "quarantine of a source record whose first line is damaged",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seed(t, mem, srcOut(1, false), srcOut(2, false))
				damageHeader(t, mem, path.Join(store.OutDir, srcOut(2, false).Name()))
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				name := srcOut(2, false).Name()
				if out, _, _ := find(t, sc, name); out != nil {
					t.Errorf("a record whose key cannot be trusted was replaced")
				}
				if len(sc.Corrupt) != 1 || len(sc.Alerts) != 1 || len(sc.Seq) != 0 {
					t.Errorf("one file, one alert and no floor expected: %v %v %v", sc.Corrupt, sc.Alerts, sc.Seq)
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
				if !hasCorrupt(sc, store.EndpointsDir, name, alertFor(store.EndpointsDir, name)) || hasEndpoint(sc, ep(7)) || len(sc.Checkpoints) != 1 {
					t.Errorf("the damaged endpoint record must be quarantined without a replacement: %v %v", sc.Corrupt, sc.Endpoints)
				}
			},
		},
		{
			name: "quarantine of a replacement that is damaged in its turn",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				first := store.AlertID{7}
				lost := store.InRecord{Key: tgtKey(1), Lost: true, DiscoveredAt: base, Record: delivery.Record{Side: delivery.SideTarget, State: delivery.StateDeliveryUnknown, AcceptBefore: base, ReportBy: base.Add(3 * 3600e9)},
					Dedup: delivery.NewDedupRecord(tgtKey(1).Dedup(), delivery.BodyDigest{}, base.Add(3*3600e9))}
				seed(t, mem, lost, store.AlertRecord{ID: first, Kind: "corrupt_record", At: base, Detail: "d", Subject: "in"})
				seedRaw(t, mem, store.InDir, corruptName(lost, first), []byte("the first damaged file"))
				damageBody(t, mem, path.Join(store.InDir, lost.Name()))
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				if len(sc.Corrupt) != 2 || len(sc.Alerts) != 2 {
					t.Fatalf("two quarantined files and two alerts expected, got %v %v: names must not collide", sc.Corrupt, sc.Alerts)
				}
				if _, in, _ := find(t, sc, inRev(1, 1).Name()); in == nil || !in.Lost {
					t.Errorf("a new replacement was not written")
				}
			},
		},
		{
			name: "quarantine of a body whose content does not match its name",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				name := store.BodyRefOf(bodyBytes(1)).Name()
				seedRaw(t, mem, store.BodiesDir, name, []byte("tampered content"))
				ctl := sessionCtl(4, 5)
				ctl.Body = bodyRef(1)
				// Every record refers to the one body.
				in2, d3, in5 := inRev(2, 1), inRev(3, 1), inRev(5, 1)
				in2.Body, d3.Body, in5.Body = bodyRef(1), bodyRef(1), bodyRef(1)
				d3.Record.State = delivery.StateDelivered
				seed(t, mem, srcOut(1, false), in2, d3, ctl, in5)
			},
			durable: "quarantine-body/body.sync-dir#1",
			mode:    atomic,
			run: func(t *testing.T, r *rig, s *sim) error {
				if _, err := r.st.ReadBody(*bodyRef(1)); err == nil {
					t.Fatalf("ReadBody accepted a tampered body")
				}
				s.bodyMismatch(store.BodyRefOf(bodyBytes(1)).Name())
				s.recoverAll()
				return nil
			},
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				name := store.BodyRefOf(bodyBytes(1)).Name()
				id := alertFor(store.BodiesDir, name)
				if !hasAlert(sc, id, "corrupt_body") {
					t.Errorf("no alert of the body quarantine: %v", sc.Alerts)
				}
				found := false
				for _, c := range sc.Corrupt {
					found = found || (c.Dir == store.BodiesDir && c.Original == name && c.Alert == id)
				}
				if !found || len(sc.Bodies) != 0 {
					t.Errorf("the body was not quarantined: %v bodies %v", sc.Corrupt, sc.Bodies)
				}
				out, in2, _ := find(t, sc, srcOut(1, false).Name())
				if out == nil || out.Body != nil || out.Outbound.Recipients[0].Delivery.State != delivery.StateDeliveryUnknown {
					t.Errorf("a source record still waiting for a receipt was not marked: %#v", out)
				}
				_, in2, _ = find(t, sc, inRev(2, 1).Name())
				if in2 == nil || !in2.Lost {
					t.Errorf("a queued message that needs its body was not replaced")
				}
				if _, d, _ := find(t, sc, inRev(3, 1).Name()); d == nil || d.Lost || d.Body != nil || d.Record.State != delivery.StateDelivered {
					t.Errorf("a delivered message must lose only the reference: %#v", d)
				}
				if _, _, c := find(t, sc, sessionCtl(4, 5).Name()); c == nil || c.Body != nil {
					t.Errorf("a control record must lose only the reference: %#v", c)
				}
			},
		},
		{
			name: "orphan bodies",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				seedBodies(t, mem, bodyBytes(1), bodyBytes(2))
				seed(t, mem, inRev(2, 1))
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				if len(sc.Bodies) != 1 || sc.Bodies[0] != bodyRef(2).Name() {
					t.Errorf("bodies = %v, want only the one a record refers to", sc.Bodies)
				}
			},
		},
		{
			name: "orphan session endpoints",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				quarantined := sessionCtl(3, 7)
				seed(t, mem,
					session(5, store.EndpointCreating, ctlName(1)),                   // no control record: garbage
					session(6, store.EndpointCreating, ctlName(2)), sessionCtl(2, 6), // a control record: kept
					session(7, store.EndpointCreating, ctlName(3)), // its control record is quarantined: kept
					session(8, store.EndpointOpen, ctlName(9)),     // open: never an orphan
					store.AlertRecord{ID: store.AlertID{3}, Kind: "corrupt_record", At: base, Detail: "d"})
				seedRaw(t, mem, store.CtlDir, corruptName(quarantined, store.AlertID{3}), []byte("x"))
			},
			mode: atomic, mustComplete: true, run: recoverRun,
			verify: func(t *testing.T, r *rig) {
				sc := r.scan()
				var ids []string
				for _, e := range sc.Endpoints {
					ids = append(ids, e.ID.String())
				}
				want := []string{ep(1).String(), ep(2).String(), ep(4).String(), ep(6).String(), ep(7).String(), ep(8).String()}
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
				done.Record.State = delivery.StateDelivered
				done.Body = nil
				c := sessionCtl(3, 5)
				c.Key.Recipient = ep(7)
				c.Dedup = delivery.NewDedupRecord(c.Key.Dedup(), digestOf(3), c.Record.ReportBy)
				seed(t, mem, closed, directEndpoint(8, "chat"), inFor(1, 7, 1), done, c, inFor(4, 9, 1), inFor(5, 8, 1))
				seedBodies(t, mem, bodyBytes(1), bodyBytes(4), bodyBytes(5))
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
				if len(sc.Bodies) != 1 || sc.Bodies[0] != bodyRef(5).Name() {
					t.Errorf("the bodies of the closed deliveries must go and the other stay: %v", sc.Bodies)
				}
			},
		},
		{
			name: "endpoints of a disabled instance",
			setup: func(t *testing.T, mem *fsxtest.Mem) {
				closed := directEndpoint(8, "chat")
				closed.Status, closed.Token = store.EndpointClosed, nil
				seed(t, mem, directEndpoint(7, "chat"), closed, directEndpoint(9, "other"), inFor(1, 7, 1), inFor(2, 9, 1), ctlTo(4, 7))
				seedBodies(t, mem, bodyBytes(1), bodyBytes(2))
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
			},
		},
		followerScenario("prompt-replay", store.PendingNotified, delivery.CauseNone),
		followerScenario("blocked-replay", store.PendingBlocked, delivery.CauseAdapterOffline),
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
				seed(t, mem, a, b, c, n)
				seedBodies(t, mem, bodyBytes(1), bodyBytes(2), bodyBytes(3), []byte("feedback wording"))
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

// followerScenario: a head record of a prompt or a block that lists followers,
// some of which were written since, replaced, or are gone.
func followerScenario(name string, ev store.PendingEvent, cause delivery.Cause) scenario {
	return scenario{
		name: name,
		setup: func(t *testing.T, mem *fsxtest.Mem) {
			listed := []store.InRecord{inRev(1, 1), inRev(2, 1), inRev(3, 1), inRev(4, 1), inRev(5, 1)}
			head := store.AttemptsRecord{Endpoint: ep(2), Pending: pendingOf(ev, cause, listed...)}
			written := inRev(2, 2)                 // written since the head: revision moved
			replaced := inRev(4, 1)                // a new record of the same key after a sweep
			replaced.AcceptedAt = base.Add(3600e9) // accepted at another time
			if ev == store.PendingUnblocked {
				for i := range listed {
					listed[i].Record.Blocked, listed[i].Record.BlockedCause = true, delivery.CauseAdapterOffline
				}
			}
			seed(t, mem, head, inRev(1, 1), written, replaced, inRev(5, 1)) // in(3) is gone
		},
		mode: atomic, mustComplete: true, run: recoverRun,
		verify: func(t *testing.T, r *rig) {
			sc := r.scan()
			rev := map[string]uint64{}
			sub := map[string]delivery.SubState{}
			blocked := map[string]bool{}
			for _, in := range sc.In {
				rev[string(in.Key.Message)] = in.Rev
				sub[string(in.Key.Message)] = in.Record.Sub
				blocked[string(in.Key.Message)] = in.Record.Blocked
			}
			for m, want := range map[string]uint64{"msg-0001": 2, "msg-0002": 2, "msg-0004": 1, "msg-0005": 2} {
				if rev[m] != want {
					t.Errorf("%s: revision %d, want %d (only a follower that is still the same record is written)", m, rev[m], want)
				}
			}
			if _, gone := rev["msg-0003"]; gone {
				t.Errorf("a follower that was removed was written again")
			}
			if ev == store.PendingNotified && (sub["msg-0001"] != delivery.SubNotified || sub["msg-0004"] != delivery.SubNone) {
				t.Errorf("notified: %v", sub)
			}
			if ev == store.PendingBlocked && (!blocked["msg-0001"] || blocked["msg-0004"]) {
				t.Errorf("blocked: %v", blocked)
			}
		},
	}
}

// controlQuarantine is the quarantine of a control record of a session creation
// whose endpoint is still being created, with a delivery derived from it.
func controlQuarantine(name string, damage func(testing.TB, *fsxtest.Mem, string), replaced bool) scenario {
	return scenario{
		name: name,
		setup: func(t *testing.T, mem *fsxtest.Mem) {
			c := sessionCtl(1, 5)
			c.Body = bodyRef(1)
			derived := derivedIn(1, 5, 1)
			derived.Body = bodyRef(1)
			seed(t, mem, session(5, store.EndpointCreating, ctlName(1)), c, derived)
			seedBodies(t, mem, bodyBytes(1))
			damage(t, mem, path.Join(store.CtlDir, c.Name()))
		},
		mode: atomic, mustComplete: true, run: recoverRun,
		verify: func(t *testing.T, r *rig) {
			sc := r.scan()
			name := sessionCtl(1, 5).Name()
			id := alertFor(store.CtlDir, name)
			if !hasAlert(sc, id, "control_unknown") {
				t.Errorf("the alert must say a native session may remain: %v", sc.Alerts)
			}
			if !hasCorrupt(sc, store.CtlDir, name, id) {
				t.Errorf("the control record was not quarantined")
			}
			if _, _, c := find(t, sc, name); (c != nil) != replaced || (c != nil && !c.Lost) {
				t.Errorf("replacement present = %v, want %v", c != nil, replaced)
			}
			if e := endpointOf(sc, ep(5)); e == nil || e.Status != store.EndpointClosed || e.Token != nil {
				t.Errorf("the session endpoint still being created must be closed: %#v", e)
			}
			if _, d, _ := find(t, sc, derivedIn(1, 5, 1).Name()); d == nil || d.Record.State != delivery.StateRejected || d.Record.Reject != delivery.RejectEndpointClosed {
				t.Errorf("the delivery derived from the lost control record must be rejected as the endpoint closed: %#v", d)
			}
			if len(sc.Bodies) != 0 {
				t.Errorf("no record refers to the body any more: %v", sc.Bodies)
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
