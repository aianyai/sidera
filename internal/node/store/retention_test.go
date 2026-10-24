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
	finishedIn := inRev(1, 1)
	finishedIn.Record = deliveredRecord(finishedIn.Record)
	rejected := inRev(1, 1)
	rejected.Record.State, rejected.Record.Reject = delivery.StateRejected, delivery.RejectUnauthorized
	closedUnc := inRev(1, 1)
	closedUnc.Record.State = delivery.StateClosedUnconfirmed
	deliveryUnknown := inRev(1, 1)
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
		{"a target record that is accepted is not finished", inRev(1, 1), want, false},
		{"a target record whose result is unknown may still be corrected", deliveryUnknown, want, false},
		{"a finished source record", srcOutFinished(1), want, true},
		{"a source record waiting for a receipt", queued, want, false},
		{"a source record's idempotency key outlives its facts", idem, want.Add(72 * time.Hour), true},
		{"a delivered control record", closedCtl, want, true},
		{"a lost record is finished by its retention alone", lostInAt(tgtKey(1), base), lostWant, true},
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
		in := inRev(1, 1)
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
		done := inRev(1, 2)
		done.Record = deliveredRecord(done.Record) // still refers to its body: the release never happened
		held := inRev(2, 1)                        // accepted: not finished, never swept
		c := created(t, 4, 5)
		c.Record = deliveredRecord(c.Record)
		derived := derivedIn(4, 5, 2)
		derived.Record = deliveredRecord(derived.Record)
		derived.Body = bodyRef(2)           // shares its body with a record that stays
		orphanDerived := derivedIn(6, 9, 2) // its control record is gone: swept by its own retention
		orphanDerived.Record = deliveredRecord(orphanDerived.Record)
		orphanDerived.Body = bodyRef(4)
		lost := lostInAt(tgtKey(7), base)
		closed := session(5, store.EndpointClosed, ctlName(4))
		open := directEndpoint(7, "chat")
		stillQueued := inFor(8, 3, 1) // targets a closed endpoint but is not final: the endpoint stays
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
		alertHeld := store.AlertRecord{ID: store.AlertID{2}, Kind: "k", At: base, Detail: "d", Acked: &store.AlertAcked{At: base, Operation: adminResult(6).Key.ID}}
		alertFresh := store.AlertRecord{ID: store.AlertID{3}, Kind: "k", At: base, Detail: "d", Acked: &store.AlertAcked{At: now.Add(-time.Hour), Operation: adminResult(7).Key.ID}}
		alertOpen := store.AlertRecord{ID: store.AlertID{4}, Kind: "k", At: base, Detail: "d"}
		seed(t, r.mem, done, held, c, derived, orphanDerived, lost, closed, open, stillQueued, closedHeld, att5, att7, cp5, cp7,
			oldAdmin, freshAdmin, alertGone, alertHeld, alertFresh, alertOpen,
			store.SeqRecord{Sender: ep(5), Floors: map[ident.Address]uint64{addr(2, 2): 3}},
			store.SeqRecord{Sender: ep(7), Floors: map[ident.Address]uint64{addr(2, 2): 3}},
			srcOutFinished(10), srcOut(11, false))
		seedRaw(t, r.mem, store.InDir, lost.Name()+"."+store.AlertID{9}.String()+".corrupt", []byte("junk"))
		seedRaw(t, r.mem, store.EndpointsDir, zeroRec+"."+alertHeld.ID.String()+".corrupt", []byte("junk"))
		seedBodies(t, r.mem, bodyBytes(1), bodyBytes(2), bodyBytes(4), bodyBytes(9))
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
		p(store.InDir, inRev(1, 2)),        // finished, past retention
		p(store.InDir, derivedIn(4, 5, 2)), // derived of a control record that goes in the same plan
		p(store.InDir, derivedIn(6, 9, 2)), // derived whose control record is gone
		p(store.InDir, store.InRecord{Key: tgtKey(7)}),
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
	wantCorrupt := []string{path.Join(store.InDir, inRev(1, 1).Name()[:0]+store.InRecord{Key: tgtKey(7)}.Name()+"."+store.AlertID{9}.String()+".corrupt")}
	if got := names(plan.Corrupt); strings.Join(got, "\n") != strings.Join(wantCorrupt, "\n") {
		t.Errorf("quarantined files = %v, want %v: a replacement's quarantined file goes with it, before it", got, wantCorrupt)
	}
	// The held ones: accepted, still queued for a closed endpoint, a quarantined
	// file whose alert is not acknowledged, an alert acknowledged too recently.
	held := []string{p(store.InDir, inRev(2, 1)), p(store.InDir, inFor(8, 3, 1)), p(store.EndpointsDir, session(3, store.EndpointClosed, "")),
		p(store.EndpointsDir, directEndpoint(7, "chat")), p(store.AdminDir, adminResult(2)), p(store.AlertsDir, store.AlertRecord{ID: store.AlertID{2}}),
		p(store.AlertsDir, store.AlertRecord{ID: store.AlertID{3}}), p(store.AlertsDir, store.AlertRecord{ID: store.AlertID{4}}), p(store.OutDir, srcOut(11, false))}
	var outNames []string
	for _, o := range plan.Outs {
		outNames = append(outNames, p(store.OutDir, o))
	}
	all := append(append(append(names(plan.Records), outNames...), names(plan.Endpoints)...), append(names(plan.Ctl), names(plan.Corrupt)...)...)
	for _, h := range held {
		for _, a := range all {
			if a == h {
				t.Errorf("%s is swept but must be held", h)
			}
		}
	}
	// Bodies: those a swept record referred to and no record left refers to. Body 2
	// is held by an accepted record, body 9 was an orphan before and is the business
	// of the orphan rule, not of the sweep.
	var bodies []string
	for _, b := range plan.Bodies {
		bodies = append(bodies, b.Name())
	}
	sort.Strings(bodies)
	wantBodies := []string{bodyRef(1).Name(), bodyRef(4).Name()}
	sort.Strings(wantBodies)
	if strings.Join(bodies, ",") != strings.Join(wantBodies, ",") {
		t.Errorf("bodies = %v, want %v", bodies, wantBodies)
	}
	// One summary per record of a delivery, none for the rest.
	var kinds []string
	for _, s := range plan.Summaries {
		kinds = append(kinds, string(s.Kind))
	}
	sort.Strings(kinds)
	if strings.Join(kinds, ",") != "ctl,in,in,in,in,out" {
		t.Errorf("summaries of %v, want ctl, in x4 and out", kinds)
	}
	if len(plan.Outs) != 1 || plan.Outs[0].Key.Message != msgID(10) {
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
	derived := derivedIn(4, 5, 2)
	derived.Record = deliveredRecord(derived.Record)
	derived.Body = nil
	seed(t, r.mem, c, derived)
	plan := store.PlanSweep(r.scan(), base.Add(100*24*time.Hour))
	if len(plan.Records)+len(plan.Ctl) != 0 {
		t.Errorf("the derived delivery was swept while its control record is live: %+v", plan)
	}
}

// The quarantined file of a delivery record whose first line cannot be read has
// no replacement, so nothing says when it may go but the time the node found it
// (the time of its alert). It stays until what a lost record found at that time
// would be kept: the longest delivery deadline an accepted message can have (1
// hour), the clock tolerance (2 minutes), the report offset (3 hours 2 minutes)
// and the retention of the facts reply permission reads (24 hours 2 minutes),
// counted from the discovery. Not a nanosecond earlier.
func TestHeaderUnreadableDeliveryQuarantineIsSweptAfterTheLostReportByBound(t *testing.T) {
	bound := base.Add(time.Hour + 2*time.Minute).Add(3*time.Hour + 2*time.Minute).Add(24*time.Hour + 2*time.Minute)
	name := func(i int) string { return fmt.Sprintf("%064x.rec", i) }
	corrupt := func(dir string, i int, id store.AlertID) string { return name(i) + "." + id.String() + ".corrupt" }

	build := func(t *testing.T) *rig {
		r := newRig(t)
		for i, dir := range []string{store.OutDir, store.InDir, store.CtlDir} {
			id := store.AlertID{byte(i + 1)}
			seed(t, r.mem, store.AlertRecord{ID: id, Kind: "corrupt_record", At: base, Detail: "d", Subject: dir})
			seedRaw(t, r.mem, dir, corrupt(dir, i, id), []byte("not a first line"))
		}
		// No alert yet: the quarantine is unfinished, and its rule runs before any sweep.
		seedRaw(t, r.mem, store.InDir, corrupt(store.InDir, 9, store.AlertID{9}), []byte("not a first line"))
		// A quarantined file whose first line is trusted (it belongs to the name the
		// file had) always had a replacement written before its alert, and the file
		// goes with that replacement. With none, the file is a state no quarantine
		// leaves, and nothing may judge it by a bound that is for files whose
		// record is unknown: it stays for the owner to see. A first line that
		// parses but does not belong to the name is not this case (see the test
		// for it below).
		damaged := inRev(5, 1)
		damagedData, err := store.EncodeRecord(damaged)
		if err != nil {
			t.Fatal(err)
		}
		damagedData[len(damagedData)-1] ^= 1
		seed(t, r.mem, store.AlertRecord{ID: store.AlertID{5}, Kind: "corrupt_record", At: base, Detail: "d", Subject: "in"})
		seedRaw(t, r.mem, store.InDir, damaged.Name()+"."+store.AlertID{5}.String()+".corrupt", damagedData)
		// A replacement and the quarantined file it replaced (here with an unreadable
		// first line): the file goes with the replacement, once.
		replacement := lostInAt(tgtKey(6), base)
		seed(t, r.mem, replacement, store.AlertRecord{ID: store.AlertID{6}, Kind: "corrupt_record", At: base, Detail: "d", Subject: "in"})
		seedRaw(t, r.mem, store.InDir, replacement.Name()+"."+store.AlertID{6}.String()+".corrupt", []byte("not a first line"))
		// A directory that holds no delivery records is the owner's to acknowledge.
		seed(t, r.mem, store.AlertRecord{ID: store.AlertID{7}, Kind: "corrupt_record", At: base, Detail: "d", Subject: "checkpoints"})
		seedRaw(t, r.mem, store.CheckpointsDir, corrupt(store.CheckpointsDir, 7, store.AlertID{7}), []byte("not a first line"))
		return r
	}
	want := []string{
		path.Join(store.CtlDir, corrupt(store.CtlDir, 2, store.AlertID{3})),
		path.Join(store.InDir, corrupt(store.InDir, 1, store.AlertID{2})),
		path.Join(store.OutDir, corrupt(store.OutDir, 0, store.AlertID{1})),
		path.Join(store.InDir, lostInAt(tgtKey(6), base).Name()+"."+store.AlertID{6}.String()+".corrupt"),
	}
	sort.Strings(want)
	for _, c := range []struct {
		rule string
		now  time.Time
		want []string
	}{
		{"not at the discovery", base, nil},
		{"not a nanosecond before the bound", bound.Add(-time.Nanosecond), nil},
		{"at the bound", bound, want},
		{"after the bound", bound.Add(24 * time.Hour), want},
	} {
		t.Run(c.rule, func(t *testing.T) {
			var got []string
			for _, f := range store.PlanSweep(build(t).scan(), c.now).Corrupt {
				got = append(got, path.Join(f.Dir, f.Name))
			}
			sort.Strings(got)
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("quarantined files swept at %v:\n got  %v\n want %v", c.now, got, c.want)
			}
		})
	}

	// What the plan names can be removed: the sweep takes the files and the
	// alerts of the next pass follow.
	r := build(t)
	plan := store.PlanSweep(r.scan(), bound)
	mustDo(t, r.st.ArchiveSweep(plan.Chunk(bound)))
	if left := r.scan().Corrupt; len(left) != 3 {
		t.Errorf("%d quarantined files left after the sweep, want the three that must stay (no alert yet, a readable first line without a replacement, not a delivery directory): %v", len(left), left)
	}
}

// A quarantined file whose first line parses but does not belong to the name the
// file had is a file whose record is unknown, like one with an unreadable first
// line: it goes after the lost record's bound counted from its alert, and the
// acknowledged alert is released only then.
func TestQuarantinedFileWithAFirstLineOfAnotherNameIsSweptAfterTheBoundAndItsAlertReleased(t *testing.T) {
	bound := base.Add(time.Hour + 2*time.Minute).Add(3*time.Hour + 2*time.Minute).Add(24*time.Hour + 2*time.Minute)
	id := store.AlertID{1}
	build := func(t *testing.T, dir string) (*rig, string) {
		r := newRig(t)
		var claimed, under store.Record
		switch dir {
		case store.OutDir:
			claimed, under = srcOut(1, false), srcOut(2, false)
		case store.InDir:
			claimed, under = inRev(1, 1), inRev(2, 1)
		default:
			claimed, under = sessionCtl(1, 5), sessionCtl(2, 6)
		}
		name, data := misnamed(t, claimed, under, id)
		seedRaw(t, r.mem, dir, name, data)
		seed(t, r.mem, store.AlertRecord{ID: id, Kind: "corrupt_record", At: base, Detail: "d", Subject: dir,
			Acked: &store.AlertAcked{At: base, Operation: adminResult(1).Key.ID}})
		return r, path.Join(dir, name)
	}
	for _, dir := range []string{store.OutDir, store.InDir, store.CtlDir} {
		t.Run(path.Base(dir), func(t *testing.T) {
			r, file := build(t, dir)
			swept := func(now time.Time) (corrupt []string, alert bool) {
				plan := store.PlanSweep(r.scan(), now)
				for _, f := range plan.Corrupt {
					corrupt = append(corrupt, path.Join(f.Dir, f.Name))
				}
				for _, f := range plan.Records {
					alert = alert || f.Dir == store.AlertsDir
				}
				return corrupt, alert
			}
			if got, alert := swept(bound.Add(-time.Nanosecond)); len(got) != 0 || alert {
				t.Fatalf("before the bound: swept %v, alert %v; want nothing", got, alert)
			}
			got, alert := swept(bound)
			if len(got) != 1 || got[0] != file || alert {
				t.Fatalf("at the bound: swept %v, alert %v; want the file and not yet its alert", got, alert)
			}
			plan := store.PlanSweep(r.scan(), bound)
			mustDo(t, r.st.ArchiveSweep(plan.Chunk(bound)))
			if left := r.scan().Corrupt; len(left) != 0 {
				t.Fatalf("quarantined files left after the sweep: %v", left)
			}
			if got, alert := swept(base.Add(store.AlertAckRetention + time.Hour)); len(got) != 0 || !alert {
				t.Errorf("after the file went and the acknowledgement was kept for its time: swept %v, alert %v; want the alert released", got, alert)
			}
		})
	}
}
