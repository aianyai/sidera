package store_test

import (
	"path"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
)

// The retention of a record is written out by hand from the design: a delivery
// is kept until the latest of what its facts need, which is report_by plus the
// clock tolerance (2 minutes) plus the reply window (24 hours): 24 hours and 2
// minutes after report_by. The fixtures' report_by is accept_before plus 3 hours
// and 2 minutes.
func TestRetainUntilIsTheLatestRetentionOfTheFactsARecordHolds(t *testing.T) {
	reportBy := base.Add(time.Minute).Add(3*time.Hour + 2*time.Minute) // fixture 1
	want := reportBy.Add(24*time.Hour + 2*time.Minute)
	// A replacement is reported until the report_by of the latest delivery that
	// can have been accepted before the loss was found: an hour of delivery
	// deadline, the clock tolerance and the report offset after the discovery.
	lostWant := base.Add(time.Hour + 2*time.Minute).Add(3*time.Hour + 2*time.Minute).Add(24*time.Hour + 2*time.Minute)
	finishedIn := tgtIn(1)
	finishedIn.Record = deliveredRecord(finishedIn.Record)
	rejected := tgtIn(1)
	rejected.Record.State, rejected.Record.Reject = delivery.StateRejected, delivery.RejectUnauthorized
	closedUnc := tgtIn(1)
	closedUnc.Record.State = delivery.StateClosedUnconfirmed
	deliveryUnknown := tgtIn(1)
	deliveryUnknown.Record.State = delivery.StateDeliveryUnknown
	idem := srcOutFinished(1)
	idem.Idempotency = &store.Idempotency{Key: "k", RetainUntil: want.Add(72 * time.Hour)}
	queued := srcOut(1, false)
	closedCtl := sessionCtl(1, 5)
	closedCtl.Record = deliveredRecord(closedCtl.Record)

	for _, c := range []struct {
		name     string
		rec      store.Record
		until    time.Time
		finished bool
	}{
		{"a delivered target record", finishedIn, want, true},
		{"a rejected target record", rejected, want, true},
		{"a closed unconfirmed target record", closedUnc, want, true},
		{"a target record that is accepted is not finished", tgtIn(1), want, false},
		{"a target record whose result is unknown may still be corrected", deliveryUnknown, want, false},
		{"a finished source record", srcOutFinished(1), want, true},
		{"a source record waiting for a receipt", queued, want, false},
		{"a source record's idempotency key outlives its facts", idem, want.Add(72 * time.Hour), true},
		{"a delivered control record", closedCtl, want, true},
		{"a control record refused at intake is kept like a finished one, so a resend gets the same report", refusedCtl(1, delivery.RejectEndpointMissing), want, true},
		{"a lost record is finished by its retention alone", lostOutAt(srcOut(1, false).Key, base), lostWant, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			until, finished := store.RetainUntil(c.rec)
			if !until.Equal(c.until) || finished != c.finished {
				t.Errorf("RetainUntil = %v, %v; want %v, %v", until, finished, c.until, c.finished)
			}
		})
	}
}

// Retention is monotone in the deadline it is computed from: a record with a
// later report_by is never kept for less.
func TestRetainUntilNeverDecreasesWithReportBy(t *testing.T) {
	var last time.Time
	for i := 0; i < 50; i++ {
		in := tgtIn(1)
		in.Record = deliveredRecord(in.Record)
		in.Record.ReportBy = base.Add(time.Duration(i) * 7 * time.Minute)
		in.Dedup.RetainUntil = in.Record.ReportBy
		until, _ := store.RetainUntil(in)
		if until.Before(last) {
			t.Fatalf("report_by %v is kept until %v, earlier than the %v of a record with an earlier report_by", in.Record.ReportBy, until, last)
		}
		last = until
	}
}

// A sweep removes what is finished and past retention, in the order the design
// gives, and nothing else.
func TestPlanSweepSelectsByRule(t *testing.T) {
	const farther = 100 * 24 * time.Hour
	now := base.Add(farther)
	soon := base.Add(2 * time.Hour) // inside the retention of every fixture

	build := func(t *testing.T) *rig {
		r := newRig(t)
		done := tgtIn(1)
		done.Record = deliveredRecord(done.Record) // still refers to its body: the release never happened
		held := tgtIn(2)                           // accepted: not finished, never swept
		c := created(t, 4, 5)
		c.Record = deliveredRecord(c.Record)
		c.Body = bodyRef(4) // a control record that still refers to its body
		derived := derivedIn(4, 5)
		derived.Record = deliveredRecord(derived.Record)
		derived.Body = bodyRef(2)        // same bytes as the body of a record that stays, in a file of its own
		orphanDerived := derivedIn(6, 9) // its control record is gone: swept by its own retention
		orphanDerived.Record = deliveredRecord(orphanDerived.Record)
		orphanDerived.Body = bodyRef(4)
		lost := lostOutAt(srcOut(7, false).Key, base)
		closed := session(5, store.EndpointClosed, ctlName(4))
		open := directEndpoint(7, "chat")
		stillQueued := inFor(8, 3) // targets a closed endpoint but is not final: the endpoint stays
		closedHeld := session(3, store.EndpointClosed, ctlName(77))
		att5 := store.AttemptsRecord{Endpoint: ep(5)}
		att7 := store.AttemptsRecord{Endpoint: ep(7)}
		cp5 := store.CheckpointRecord{Key: store.CheckpointKey{Scope: "session", ID: ep(5).String()}, State: []byte(`{}`)}
		cp7 := store.CheckpointRecord{Key: store.CheckpointKey{Scope: "session", ID: ep(7).String()}, State: []byte(`{}`)}
		oldAdmin := adminResult(1)
		oldAdmin.RetainUntil = base.Add(24 * time.Hour)
		freshAdmin := adminResult(2)
		freshAdmin.RetainUntil = now.Add(time.Hour)
		alertGone := store.AlertRecord{ID: store.AlertID{1}, Kind: "k", At: base, Detail: "d", Acked: &store.AlertAcked{At: base, Operation: adminResult(5).Key.ID}}
		alertFresh := store.AlertRecord{ID: store.AlertID{3}, Kind: "k", At: base, Detail: "d", Acked: &store.AlertAcked{At: now.Add(-time.Hour), Operation: adminResult(7).Key.ID}}
		alertOpen := store.AlertRecord{ID: store.AlertID{4}, Kind: "k", At: base, Detail: "d"}
		seed(t, r.mem, done, held, c, derived, orphanDerived, lost, closed, open, stillQueued, closedHeld, att5, att7, cp5, cp7,
			oldAdmin, freshAdmin, alertGone, alertFresh, alertOpen,
			store.SeqRecord{Sender: ep(5), Floors: map[ident.Address]uint64{addr(2, 2): 3}},
			store.SeqRecord{Sender: ep(7), Floors: map[ident.Address]uint64{addr(2, 2): 3}},
			srcOutFinished(10), srcOut(11, false))
		seedBodies(t, r.mem, done, held, c, derived, orphanDerived, stillQueued, srcOut(11, false))
		seedBodyOf(t, r.mem, tgtIn(9), bodyBytes(9)) // the body of a record that does not exist
		_ = soon
		return r
	}
	r := build(t)
	plan := store.PlanSweep(r.scan(), now)

	names := func(refs []store.FileRef) []string {
		var out []string
		for _, f := range refs {
			out = append(out, path.Join(f.Dir, f.Name))
		}
		sort.Strings(out)
		return out
	}
	p := func(dir string, rec store.Record) string { return path.Join(dir, rec.Name()) }
	c4 := created(t, 4, 5)

	wantRecords := []string{
		p(store.InDir, tgtIn(1)),        // finished, past retention
		p(store.InDir, derivedIn(4, 5)), // derived of a control record that goes in the same plan
		p(store.InDir, derivedIn(6, 9)), // derived whose control record is gone
		p(store.EndpointsDir, session(5, store.EndpointClosed, ctlName(4))),
		p(store.AttemptsDir, store.AttemptsRecord{Endpoint: ep(5)}),
		p(store.CheckpointsDir, store.CheckpointRecord{Key: store.CheckpointKey{Scope: "session", ID: ep(5).String()}}),
		p(store.SeqDir, store.SeqRecord{Sender: ep(5)}),
		p(store.AdminDir, adminResult(1)),
		p(store.AlertsDir, store.AlertRecord{ID: store.AlertID{1}}),
	}
	sort.Strings(wantRecords)
	// The record of a closed endpoint is its own group: it goes after the files that hang on it.
	if got := names(plan.Endpoints); len(got) != 1 || got[0] != p(store.EndpointsDir, session(5, store.EndpointClosed, "")) {
		t.Errorf("endpoints = %v, want only the closed one nothing points to", got)
	}
	if got := names(append(append([]store.FileRef(nil), plan.Records...), plan.Endpoints...)); strings.Join(got, "\n") != strings.Join(wantRecords, "\n") {
		t.Errorf("records:\n got  %s\n want %s", strings.Join(got, "\n      "), strings.Join(wantRecords, "\n      "))
	}
	if got := names(plan.Ctl); len(got) != 1 || got[0] != p(store.CtlDir, c4) {
		t.Errorf("control records = %v, want only the finished one, which goes before its derived delivery", got)
	}
	// The held ones: accepted, still queued for a closed endpoint, an alert
	// acknowledged too recently or not at all.
	held := []string{p(store.InDir, tgtIn(2)), p(store.InDir, inFor(8, 3)), p(store.EndpointsDir, session(3, store.EndpointClosed, "")),
		p(store.EndpointsDir, directEndpoint(7, "chat")), p(store.AdminDir, adminResult(2)),
		p(store.AlertsDir, store.AlertRecord{ID: store.AlertID{3}}), p(store.AlertsDir, store.AlertRecord{ID: store.AlertID{4}}), p(store.OutDir, srcOut(11, false))}
	var outNames []string
	for _, o := range plan.Outs {
		outNames = append(outNames, p(store.OutDir, o))
	}
	all := append(append(append(names(plan.Records), outNames...), names(plan.Endpoints)...), names(plan.Ctl)...)
	for _, h := range held {
		for _, a := range all {
			if a == h {
				t.Errorf("%s is swept but must be held", h)
			}
		}
	}
	// Bodies: the plan lists none; the sweep removes the own body file of every
	// record it removes and no other. The derived delivery and the held record
	// refer to the same bytes in files of their own, so the first goes and the
	// second stays; the body of a record that does not exist was an orphan before
	// and is the business of the orphan rule, not of the sweep.
	mustDo(t, r.st.ArchiveSweep(plan.Chunk()))
	bodyFiles := snapshot(t, r.mem, store.BodiesDir)
	for _, swept := range []store.Record{tgtIn(1), created(t, 4, 5), derivedIn(4, 5), derivedIn(6, 9)} {
		if _, there := bodyFiles[bodyPath(swept)]; there {
			t.Errorf("the body of the swept record %s is still there", swept.Name())
		}
	}
	for _, stays := range []store.Record{tgtIn(2), inFor(8, 3), srcOut(11, false), tgtIn(9)} {
		if _, there := bodyFiles[bodyPath(stays)]; !there {
			t.Errorf("the body %s of a record that stays (or of none) was removed", bodyPath(stays))
		}
	}
	if len(bodyFiles) != 4 {
		t.Errorf("body files after the sweep: %d, want the 4 of the records that stay and the orphan", len(bodyFiles))
	}
	// One summary per record of a delivery, none for the rest.
	var kinds []string
	for _, s := range plan.Summaries {
		kinds = append(kinds, string(s.Kind))
	}
	sort.Strings(kinds)
	if strings.Join(kinds, ",") != "ctl,in,in,in,out,out" {
		t.Errorf("summaries of %v, want ctl, in x3 and out x2", kinds)
	}
	if len(plan.Outs) != 2 {
		t.Errorf("source records swept: %v", plan.Outs)
	}
	for _, s := range plan.Summaries {
		if s.Kind == store.KindIn && strings.HasSuffix(s.Key, string(msgID(1))+" "+ep(2).String()) {
			if s.State != "delivered" || s.Quarantined {
				t.Errorf("summary of a delivered record: %+v", s)
			}
		}
	}

	// Before retention nothing of the deliveries goes.
	early := store.PlanSweep(build(t).scan(), base.Add(time.Hour))
	for _, f := range early.Records {
		if f.Dir == store.InDir {
			t.Errorf("%s swept before its retention ended", f.Name)
		}
	}
	if len(early.Outs) != 0 {
		t.Errorf("source records swept before their retention ended: %v", early.Outs)
	}
}

// A control record that is not finished keeps its derived delivery, even when the
// derived delivery's own retention has ended.
func TestPlanSweepKeepsADerivedDeliveryWhileItsControlRecordIsLive(t *testing.T) {
	r := newRig(t)
	c := sessionCtl(4, 5) // accepted: not finished
	derived := derivedIn(4, 5)
	derived.Record = deliveredRecord(derived.Record)
	derived.Body = nil
	seed(t, r.mem, c, derived)
	plan := store.PlanSweep(r.scan(), base.Add(100*24*time.Hour))
	if len(plan.Records)+len(plan.Ctl) != 0 {
		t.Errorf("the derived delivery was swept while its control record is live: %+v", plan)
	}
}

// A finished target record is kept until the instant its retention ends and goes
// from that instant: one nanosecond earlier it stays. The instant is written out by
// hand (outRetention).
func TestPlanSweepTakesAFinishedTargetRecordAtItsRetentionAndNotBefore(t *testing.T) {
	until := outRetention(1)
	in := tgtIn(1)
	in.Record = deliveredRecord(in.Record)
	out := srcOutFinished(1)
	for _, c := range []struct {
		at    time.Time
		swept bool
	}{
		{until.Add(-time.Nanosecond), false},
		{until, true},
	} {
		r := newRig(t)
		seed(t, r.mem, in, out)
		plan := store.PlanSweep(r.scan(), c.at)
		if got := plannedRecord(plan, store.InDir, in.Name()); got != c.swept {
			t.Errorf("at retention %+v: target record planned = %v, want %v", c.at.Sub(until), got, c.swept)
		}
		if got := len(plan.Outs) == 1; got != c.swept {
			t.Errorf("at retention %+v: source record planned = %v, want %v", c.at.Sub(until), got, c.swept)
		}
	}
}

// A source record whose delivery is still queued or accepted after report_by is
// waiting for the pull before its close, which only a running node makes: it is not
// planned, however late the sweep runs. It is planned once the close has written a
// final state, and then only by its retention.
func TestPlanSweepNeverPlansASourceRecordWaitingForItsFinalPull(t *testing.T) {
	withState := func(o store.OutRecord, ev delivery.Event, at time.Time) store.OutRecord {
		d, _, err := o.Outbound.Recipients[0].Delivery.Apply(at, ev)
		if err != nil {
			t.Fatal(err)
		}
		o.Outbound.Recipients[0].Delivery = d
		return o
	}
	queued := srcOut(1, false)
	accepted := withState(srcOut(2, false), delivery.Event{Type: delivery.EventReceipt}, base)
	pastReportBy := base.Add(farAway)
	for _, o := range []store.OutRecord{queued, accepted} {
		if o.Outbound.Recipients[0].Delivery.ReportBy.After(pastReportBy) {
			t.Fatal("the scenario's sweep is not past report_by")
		}
		r := newRig(t)
		seed(t, r.mem, o)
		plan := store.PlanSweep(r.scan(), pastReportBy)
		if len(plan.Outs) != 0 || len(plan.Summaries) != 0 {
			t.Errorf("%s: a record that waits for its final pull is planned: outs %v, summaries %v", o.Outbound.Recipients[0].Delivery.State, plan.Outs, plan.Summaries)
		}
	}
	closed := withState(accepted, delivery.Event{Type: delivery.EventReportByPassed}, pastReportBy)
	if st := closed.Outbound.Recipients[0].Delivery.State; st != delivery.StateClosedUnconfirmed {
		t.Fatalf("the close left the delivery %s, want closed_unconfirmed", st)
	}
	r := newRig(t)
	seed(t, r.mem, closed)
	if plan := store.PlanSweep(r.scan(), outRetention(2).Add(-time.Nanosecond)); len(plan.Outs) != 0 {
		t.Errorf("a closed record is planned before its retention: %v", plan.Outs)
	}
	if plan := store.PlanSweep(r.scan(), outRetention(2)); len(plan.Outs) != 1 {
		t.Errorf("a closed record is not planned at its retention: %v", plan.Outs)
	}
}
