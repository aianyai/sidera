package delivery

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"
)

// Control records in these tests have their delivery deadline ten minutes
// after t0, so report_by is t0+3h12m and the settle deadline five minutes
// before it.
var (
	controlAcceptBefore = at(10 * time.Minute)
	controlReportBy     = ReportBy(controlAcceptBefore)
	controlSettleBy     = controlReportBy.Add(-5 * time.Minute)
)

func controlRecord() Record {
	return NewTargetControlRecord(t0, controlAcceptBefore, controlReportBy)
}

func replyCommitted() Event { return Event{Type: EventReplyCommitted} }

func refusedControlRecord(t *testing.T, reason RejectReason) Record {
	t.Helper()
	rec, err := NewRejectedTargetControlRecord(t0, controlAcceptBefore, controlReportBy, reason)
	if err != nil {
		t.Fatalf("a control signal refused for %s: %v", reason, err)
	}
	return rec
}

func TestNewTargetControlRecord(t *testing.T) {
	rec := controlRecord()
	want := Record{
		Side:         SideTarget,
		State:        StateAccepted,
		Control:      true,
		StateAt:      t0,
		AcceptBefore: controlAcceptBefore,
		ReportBy:     controlReportBy,
	}
	if !reflect.DeepEqual(rec, want) {
		t.Errorf("record = %+v, want %+v", rec, want)
	}
	if !rec.BacklogDeadline.IsZero() {
		t.Errorf("a control record has no backlog deadline, got %v", rec.BacklogDeadline)
	}
	msg := targetRecord(t, t0, time.Hour)
	if msg.Control || NewSourceRecord(t0, controlAcceptBefore).Control {
		t.Error("message records are not control records")
	}
}

func TestControlRecordIsNeverHandedOut(t *testing.T) {
	for _, auth := range []AuthorizationKind{AuthorizationAllow, AuthorizationSuspend, AuthorizationDeny} {
		t.Run(fmt.Sprintf("Handout refuses a control record under authorization %d", auth), func(t *testing.T) {
			rec := controlRecord()
			after, res, err := rec.Handout(at(time.Minute), auth, 1, 1, freshLease())
			if !errors.Is(err, ErrWrongSide) {
				t.Fatalf("err = %v, want ErrWrongSide", err)
			}
			if res.Action != 0 || !reflect.DeepEqual(after, rec) {
				t.Errorf("result %+v, record changed: %+v", res, after)
			}
		})
	}
	t.Run("Ack refuses a control record, even with a matching token", func(t *testing.T) {
		rec := controlRecord()
		after, _, _, err := rec.Ack(at(time.Minute), Ack{Part: 1, Lease: freshLease(), TokenMatches: true})
		if !errors.Is(err, ErrWrongSide) {
			t.Fatalf("err = %v, want ErrWrongSide", err)
		}
		if !reflect.DeepEqual(after, rec) {
			t.Errorf("record changed: %+v", after)
		}
	})
	t.Run("Renew finds no lease on a control record", func(t *testing.T) {
		rec := controlRecord()
		after, res := rec.Renew(at(time.Minute), true)
		if res != RenewNoLease || !reflect.DeepEqual(after, rec) {
			t.Errorf("result %v, record %+v", res, after)
		}
	})
	t.Run("a control record holds no live lease and is not due for hand-out", func(t *testing.T) {
		if controlRecord().HoldsLiveLease(at(time.Minute)) {
			t.Error("a control record holds a lease")
		}
	})
	t.Run("a control record is never blocked, whatever the cause", func(t *testing.T) {
		for _, cause := range []Cause{CauseAdapterOffline, CauseRuntimeAuth, CauseNativeUnavailable} {
			rec := controlRecord()
			after, _, err := rec.Apply(at(time.Minute), Event{Type: EventBlocked, Cause: cause})
			if !errors.Is(err, ErrWrongSide) || !reflect.DeepEqual(after, rec) {
				t.Errorf("cause %v: err = %v, record %+v", cause, err, after)
			}
		}
	})
	t.Run("a control signal is never announced with a prompt", func(t *testing.T) {
		rec := controlRecord()
		after, _, err := rec.Apply(at(time.Minute), Event{Type: EventNotified})
		if !errors.Is(err, ErrWrongSide) || !reflect.DeepEqual(after, rec) {
			t.Errorf("err = %v, record %+v", err, after)
		}
	})
}

func TestReplyCommitted(t *testing.T) {
	t.Run("the committed reply delivers an accepted control record without feedback", func(t *testing.T) {
		rec, out := mustApply(t, controlRecord(), at(time.Minute), replyCommitted())
		if rec.State != StateDelivered || out.Result != ResultApplied || out.From != StateAccepted || out.To != StateDelivered || len(out.Feedback) != 0 || rec.Reported != 0 {
			t.Errorf("state %s outcome %+v", rec.State, out)
		}
		if rec.Lease.Handouts != 0 || rec.Lease.Chunks != 0 {
			t.Errorf("the reply must not invent a hand-out: %+v", rec.Lease)
		}
	})
	t.Run("a repeated committed reply is a duplicate", func(t *testing.T) {
		rec, _ := mustApply(t, controlRecord(), at(time.Minute), replyCommitted())
		_, out := mustApply(t, rec, at(2*time.Minute), replyCommitted())
		if out.Result != ResultIgnored || out.Ignored != IgnoreDuplicate {
			t.Errorf("outcome %+v", out)
		}
	})
	t.Run("a reply committed after delivery_unknown corrects it", func(t *testing.T) {
		rec, _, err := controlRecord().Tick(controlSettleBy)
		if err != nil || rec.State != StateDeliveryUnknown {
			t.Fatalf("state %s err %v", rec.State, err)
		}
		rec, out := mustApply(t, rec, controlSettleBy.Add(time.Minute), replyCommitted())
		if rec.State != StateDelivered || out.From != StateDeliveryUnknown || out.To != StateDelivered {
			t.Errorf("state %s outcome %+v", rec.State, out)
		}
	})
	t.Run("a reply committed after the record closed is ignored as final", func(t *testing.T) {
		rec, _, err := controlRecord().Tick(controlReportBy.Add(time.Second))
		if err != nil || rec.State != StateClosedUnconfirmed {
			t.Fatalf("state %s err %v", rec.State, err)
		}
		rec, out := mustApply(t, rec, controlReportBy.Add(time.Minute), replyCommitted())
		if rec.State != StateClosedUnconfirmed || out.Ignored != IgnoreAlreadyFinal {
			t.Errorf("state %s outcome %+v", rec.State, out)
		}
	})
	t.Run("a reply committed for a rejected control record contradicts it", func(t *testing.T) {
		rec := refusedControlRecord(t, RejectMalformed)
		after, _, err := rec.Apply(at(time.Minute), replyCommitted())
		if !errors.Is(err, ErrIllegalTransition) || !reflect.DeepEqual(after, rec) {
			t.Errorf("err = %v, record %+v", err, after)
		}
	})
}

func TestReplyCommittedAppliesToControlRecordsOnly(t *testing.T) {
	rows := []struct {
		rule string
		rec  func(*testing.T) Record
	}{
		{"a source record refuses a committed reply", func(*testing.T) Record { return NewSourceRecord(t0, controlAcceptBefore) }},
		{"a source record flagged as control still refuses a committed reply", func(*testing.T) Record {
			rec := NewSourceRecord(t0, controlAcceptBefore)
			rec.Control = true
			return rec
		}},
		{"an accepted source record refuses a committed reply", func(t *testing.T) Record {
			rec, _ := mustApply(t, NewSourceRecord(t0, controlAcceptBefore), at(time.Second), Event{Type: EventReceipt})
			return rec
		}},
		{"a queued target message record refuses a committed reply", func(t *testing.T) Record { return targetRecord(t, t0, time.Hour) }},
		{"a handed-out target message record refuses a committed reply", func(t *testing.T) Record {
			return handOut(t, targetRecord(t, t0, time.Hour), at(time.Minute), 1, 1)
		}},
		{"a delivered target message record refuses a committed reply", func(t *testing.T) Record {
			return deliverBody(t, targetRecord(t, t0, time.Hour), at(time.Minute))
		}},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			rec := r.rec(t)
			after, _, err := rec.Apply(at(time.Hour), replyCommitted())
			if !errors.Is(err, ErrWrongSide) {
				t.Fatalf("err = %v, want ErrWrongSide", err)
			}
			if !reflect.DeepEqual(after, rec) {
				t.Errorf("record changed: %+v", after)
			}
		})
	}
}

func TestControlRecordSettlement(t *testing.T) {
	rows := []struct {
		rule       string
		now        time.Time
		wantState  State
		wantReject RejectReason
		wantSteps  [][2]State // from, to of every outcome that changed the state
	}{
		{"a control record is not rejected as backlog at the longest queue time of a message", at(10*time.Minute + MaxQueueCap), StateAccepted, RejectNone, nil},
		{"a control record is not rejected as backlog when the queue cap of a message has passed", at(2*time.Hour + 20*time.Minute), StateAccepted, RejectNone, nil},
		{"a control record stays accepted just before the settle deadline", controlSettleBy.Add(-time.Nanosecond), StateAccepted, RejectNone, nil},
		{"a control record without a committed reply is delivery_unknown at the settle deadline", controlSettleBy, StateDeliveryUnknown, RejectNone, [][2]State{{StateAccepted, StateDeliveryUnknown}}},
		{"delivery_unknown stays until report_by", controlReportBy, StateDeliveryUnknown, RejectNone, [][2]State{{StateAccepted, StateDeliveryUnknown}}},
		{"a control record closes silently after report_by", controlReportBy.Add(time.Second), StateClosedUnconfirmed, RejectNone, [][2]State{{StateAccepted, StateDeliveryUnknown}, {StateDeliveryUnknown, StateClosedUnconfirmed}}},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			rec, outs, err := controlRecord().Tick(r.now)
			if err != nil {
				t.Fatal(err)
			}
			if rec.State != r.wantState || rec.Reject != r.wantReject {
				t.Errorf("state %s reject %s, want %s", rec.State, rec.Reject, r.wantState)
			}
			var steps [][2]State
			for _, o := range outs {
				if o.From != o.To {
					steps = append(steps, [2]State{o.From, o.To})
				}
				if len(o.Feedback) != 0 {
					t.Errorf("a target record produced feedback: %+v", o.Feedback)
				}
			}
			if !reflect.DeepEqual(steps, r.wantSteps) {
				t.Errorf("steps %v, want %v", steps, r.wantSteps)
			}
		})
	}
	t.Run("a delivered control record is not settled", func(t *testing.T) {
		rec, _ := mustApply(t, controlRecord(), at(time.Minute), replyCommitted())
		after, _, err := rec.Tick(controlReportBy.Add(time.Hour))
		if err != nil || after.State != StateDelivered {
			t.Errorf("state %s err %v", after.State, err)
		}
	})
	t.Run("a forced closure of an accepted control record is delivery_unknown, never rejected", func(t *testing.T) {
		rec, out := mustApply(t, controlRecord(), at(time.Minute), Event{Type: EventRejected, Reject: RejectEndpointClosed})
		if rec.State != StateDeliveryUnknown || out.To != StateDeliveryUnknown || rec.Reject != RejectNone {
			t.Errorf("state %s reject %s", rec.State, rec.Reject)
		}
	})
	t.Run("delivery_unknown may be recorded for a control record that was never handed out", func(t *testing.T) {
		rec, _ := mustApply(t, controlRecord(), at(time.Minute), Event{Type: EventDeliveryUnknown})
		if rec.State != StateDeliveryUnknown {
			t.Errorf("state %s", rec.State)
		}
	})
	t.Run("a message record without a hand-out is still refused delivery_unknown", func(t *testing.T) {
		_, _, err := targetRecord(t, t0, time.Hour).Apply(at(time.Minute), Event{Type: EventDeliveryUnknown})
		if !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestControlStatusReport(t *testing.T) {
	rows := []struct {
		rule string
		rec  func(*testing.T) Record
		want StatusReport
	}{
		{"an executing control record is reported accepted", func(*testing.T) Record { return controlRecord() }, StatusReport{State: StateAccepted}},
		{"a control record whose reply is committed is reported delivered", func(t *testing.T) Record {
			rec, _ := mustApply(t, controlRecord(), at(time.Minute), replyCommitted())
			return rec
		}, StatusReport{State: StateDelivered}},
		{"a settled control record is reported delivery_unknown", func(t *testing.T) Record {
			rec, _, err := controlRecord().Tick(controlSettleBy)
			if err != nil {
				t.Fatal(err)
			}
			return rec
		}, StatusReport{State: StateDeliveryUnknown}},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := r.rec(t).StatusReport(); got != r.want {
				t.Errorf("report %+v, want %+v", got, r.want)
			}
		})
	}
}

// TestScenarioControlSignalLifecycle follows one control signal on both nodes:
// the source applies the target's reports with the events it already has.
func TestScenarioControlSignalLifecycle(t *testing.T) {
	t.Run("a committed reply reaches the sender as a silent delivery", func(t *testing.T) {
		src := NewSourceRecord(t0, controlAcceptBefore)
		tgt := controlRecord()
		src, _, err := src.ApplyStatusReport(at(time.Minute), tgt.StatusReport())
		if err != nil || src.State != StateAccepted {
			t.Fatalf("state %s err %v", src.State, err)
		}
		tgt, _ = mustApply(t, tgt, at(2*time.Minute), replyCommitted())
		src, outs, err := src.ApplyStatusReport(at(3*time.Minute), tgt.StatusReport())
		if err != nil || src.State != StateDelivered {
			t.Fatalf("state %s err %v", src.State, err)
		}
		for _, o := range outs {
			if len(o.Feedback) != 0 {
				t.Errorf("feedback %+v for a normal control signal", o.Feedback)
			}
		}
	})
	t.Run("the sender applies the reply itself with the delivered event", func(t *testing.T) {
		src, _ := mustApply(t, NewSourceRecord(t0, controlAcceptBefore), at(time.Second), Event{Type: EventReceipt})
		src, out := mustApply(t, src, at(time.Minute), Event{Type: EventDelivered})
		if src.State != StateDelivered || len(out.Feedback) != 0 {
			t.Errorf("state %s feedback %+v", src.State, out.Feedback)
		}
	})
	t.Run("an unsettled control signal is told as unknown and corrected by a late reply", func(t *testing.T) {
		src := NewSourceRecord(t0, controlAcceptBefore)
		tgt := controlRecord()
		src, _, _ = src.ApplyStatusReport(at(time.Minute), tgt.StatusReport())
		tgt, _, err := tgt.Tick(controlSettleBy)
		if err != nil {
			t.Fatal(err)
		}
		src, outs, err := src.ApplyStatusReport(controlSettleBy, tgt.StatusReport())
		if err != nil {
			t.Fatal(err)
		}
		if got := kindsOf(outs[0].Feedback); !sameKinds(got, []Kind{KindDeliveryUnknown}) {
			t.Fatalf("feedback %v", got)
		}
		tgt, _ = mustApply(t, tgt, controlSettleBy.Add(time.Minute), replyCommitted())
		src, outs, err = src.ApplyStatusReport(controlSettleBy.Add(2*time.Minute), tgt.StatusReport())
		if err != nil || src.State != StateDelivered {
			t.Fatalf("state %s err %v", src.State, err)
		}
		if got := kindsOf(outs[0].Feedback); !sameKinds(got, []Kind{KindUnknownCorrected}) {
			t.Errorf("feedback %v", got)
		}
	})
}

// controlPaths are the paths of the control rules the random control signals
// must reach often enough to mean something.
var controlPaths = []string{
	"reply committed from accepted", "reply committed after delivery_unknown", "reply committed after close",
	"reply committed again", "settled to delivery_unknown", "closed after report_by",
	"forced closure is delivery_unknown", "hand-out refused", "ack refused", "prompt refused",
	"source told delivery_unknown", "source told unknown corrected", "source delivered", "refused on a message record",
	"block refused",
}

// TestPropertyControlSignalsAreNeverQueuedAndAlwaysEnd drives a control record
// and its sender's record with random, time-ordered operations. The expected
// state of the control record comes from a separate model written here that
// knows only the rules in words: delivered only by a committed reply, unknown
// at the settle deadline otherwise, closed after report_by, never rejected,
// never handed out.
func TestPropertyControlSignalsAreNeverQueuedAndAlwaysEnd(t *testing.T) {
	paths := map[string]int{}
	for seed := int64(1); seed <= propertySeeds; seed++ {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) { runRandomControl(t, seed, paths, func(*testing.T, Record) {}) })
	}
	if t.Failed() {
		return
	}
	for _, p := range controlPaths {
		if paths[p] < minPathCount {
			t.Errorf("path %q reached %d times, want at least %d: the generator no longer exercises it", p, paths[p], minPathCount)
		}
	}
	names := make([]string, 0, len(paths))
	for p := range paths {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		t.Logf("%-45s %d", p, paths[p])
	}
}

// observe is called with the control record and the sender's record after every
// step.
func runRandomControl(t *testing.T, seed int64, paths map[string]int, observe func(*testing.T, Record)) {
	rng := rand.New(rand.NewSource(seed))
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("seed %d: "+format, append([]any{seed}, args...)...)
	}
	ab := t0.Add(time.Duration(1+rng.Intn(60)) * time.Minute)
	reportBy := ab.Add(2*time.Hour + 4*15*time.Minute + 2*time.Minute) // the decided report_by offset, spelled out
	settle := reportBy.Add(-5 * time.Minute)
	tgt := NewTargetControlRecord(t0, ab, reportBy)
	src := NewSourceRecord(t0, ab)

	// The model: the externally visible state the rules in words call for.
	model := StateAccepted
	modelFinal := func() bool {
		return model == StateDelivered || model == StateClosedUnconfirmed || model == StateRejected
	}
	committed := false // a reply was committed while it could still count
	told := map[Kind]int{}
	// What the sender must have been told, by the rules in words: delivery_unknown
	// only if it pulled while the target was delivery_unknown, and the correction
	// only if it pulled delivered after having been told unknown. Nothing else:
	// no rejection and no block, because a control record has neither.
	modelToldUnknown, modelToldCorrected := false, false

	// Some control signals are executed promptly, some never get their reply
	// committed before the deadlines.
	commitWeight := 0
	if rng.Intn(2) == 0 {
		commitWeight = 3
	}
	type opKind int
	const (
		opCommit opKind = iota
		opTick
		opHandout
		opAck
		opPrompt
		opForce
		opPull
		opMessageRecord
		opBlock
		opCount
	)
	weights := map[opKind]int{opCommit: commitWeight, opTick: 4, opHandout: 1, opAck: 1, opPrompt: 1, opForce: 1, opPull: 4, opMessageRecord: 1, opBlock: 1}
	var wheel []opKind
	for o := opKind(0); o < opCount; o++ {
		for i := 0; i < weights[o]; i++ {
			wheel = append(wheel, o)
		}
	}
	step := func() time.Duration {
		switch p := rng.Intn(100); {
		case p < 70:
			return time.Duration(rng.Intn(5*60)) * time.Second
		case p < 95:
			return time.Duration(rng.Intn(60)) * time.Minute
		default:
			return time.Duration(rng.Intn(180)) * time.Minute
		}
	}
	advanceModel := func(now time.Time) {
		if model == StateAccepted && !now.Before(settle) {
			model = StateDeliveryUnknown
			paths["settled to delivery_unknown"]++
		}
		if !modelFinal() && now.After(reportBy) {
			model = StateClosedUnconfirmed
			paths["closed after report_by"]++
		}
	}
	check := func(now time.Time) {
		observe(t, tgt)
		observe(t, src)
		if tgt.State != model {
			fail("target %s, model %s at %v", tgt.State, model, now.Sub(t0))
		}
		if tgt.Lease.Handouts != 0 || tgt.Lease.Active || tgt.Lease.Chunks != 0 || len(tgt.Lease.IDs) != 0 || !tgt.BacklogDeadline.IsZero() || tgt.Reject != RejectNone {
			fail("a control record acquired queue state: %+v", tgt)
		}
		if tgt.State == StateDelivered && !committed {
			fail("delivered without a committed reply")
		}
		if committed && tgt.State != StateDelivered && tgt.State != StateClosedUnconfirmed {
			fail("reply committed but target is %s", tgt.State)
		}
		if !now.Before(settle) && tgt.State == StateAccepted {
			fail("still accepted at the settle deadline")
		}
	}

	now := t0
	end := reportBy.Add(10 * time.Minute)
	for ops := 0; now.Before(end) && ops < 300; ops++ {
		now = now.Add(step())
		advanceModel(now)
		n, _, err := tgt.Tick(now)
		if err != nil {
			fail("Tick: %v", err)
		}
		tgt = n
		check(now)

		switch wheel[rng.Intn(len(wheel))] {
		case opCommit:
			before := model
			n, out, err := tgt.Apply(now, replyCommitted())
			if err != nil {
				fail("reply committed in %s: %v", tgt.State, err)
			}
			tgt = n
			switch before {
			case StateAccepted:
				model, committed = StateDelivered, true
				paths["reply committed from accepted"]++
			case StateDeliveryUnknown:
				model, committed = StateDelivered, true
				paths["reply committed after delivery_unknown"]++
			case StateDelivered:
				paths["reply committed again"]++
			case StateClosedUnconfirmed:
				paths["reply committed after close"]++
				if out.Ignored != IgnoreAlreadyFinal {
					fail("a reply after close: %+v", out)
				}
			}
		case opHandout:
			if _, _, err := tgt.Handout(now, AuthorizationAllow, 1, 1, freshLease()); !errors.Is(err, ErrWrongSide) {
				fail("Handout: %v", err)
			}
			paths["hand-out refused"]++
		case opAck:
			if _, _, _, err := tgt.Ack(now, Ack{Part: 1, Lease: freshLease(), TokenMatches: true}); !errors.Is(err, ErrWrongSide) {
				fail("Ack: %v", err)
			}
			paths["ack refused"]++
		case opPrompt:
			if _, _, err := tgt.Apply(now, Event{Type: EventNotified}); !errors.Is(err, ErrWrongSide) {
				fail("prompt: %v", err)
			}
			paths["prompt refused"]++
		case opForce:
			if model == StateAccepted {
				n, _, err := tgt.Apply(now, Event{Type: EventRejected, Reject: RejectEndpointClosed})
				if err != nil {
					fail("forced closure: %v", err)
				}
				tgt, model = n, StateDeliveryUnknown
				paths["forced closure is delivery_unknown"]++
			}
		case opMessageRecord:
			msg := targetRecord(t, now, time.Hour)
			if _, _, err := msg.Apply(now, replyCommitted()); !errors.Is(err, ErrWrongSide) {
				fail("a message record took a committed reply: %v", err)
			}
			paths["refused on a message record"]++
		case opBlock:
			if _, _, err := tgt.Apply(now, Event{Type: EventBlocked, Cause: CauseAdapterOffline}); !errors.Is(err, ErrWrongSide) {
				fail("block: %v", err)
			}
			paths["block refused"]++
		case opPull:
			if tgt.State == StateClosedUnconfirmed {
				break
			}
			var wantKinds []Kind
			switch {
			case model == StateDeliveryUnknown && !modelToldUnknown:
				wantKinds, modelToldUnknown = []Kind{KindDeliveryUnknown}, true
			case model == StateDelivered && modelToldUnknown && !modelToldCorrected:
				wantKinds, modelToldCorrected = []Kind{KindUnknownCorrected}, true
			}
			n, outs, err := src.ApplyStatusReport(now, tgt.StatusReport())
			if err != nil {
				fail("ApplyStatusReport(%+v) in %s: %v", tgt.StatusReport(), src.State, err)
			}
			src = n
			var gotKinds []Kind
			for _, o := range outs {
				gotKinds = append(gotKinds, kindsOf(o.Feedback)...)
				for _, fb := range o.Feedback {
					told[fb.Kind]++
					if told[fb.Kind] > 1 {
						fail("feedback %s twice", fb.Kind)
					}
					switch fb.Kind {
					case KindDeliveryUnknown:
						paths["source told delivery_unknown"]++
					case KindUnknownCorrected:
						paths["source told unknown corrected"]++
					}
				}
			}
			if !sameKinds(gotKinds, wantKinds) {
				fail("pulled %s from a target in %s: feedback %v, want %v", tgt.StatusReport().State, model, gotKinds, wantKinds)
			}
			if src.State == StateDelivered {
				paths["source delivered"]++
				if tgt.State != StateDelivered {
					fail("source delivered, target %s", tgt.State)
				}
			}
		}
		advanceModel(now)
		check(now)
	}
	wantTold := map[Kind]int{}
	if modelToldUnknown {
		wantTold[KindDeliveryUnknown] = 1
	}
	if modelToldCorrected {
		wantTold[KindUnknownCorrected] = 1
	}
	if !reflect.DeepEqual(told, wantTold) {
		fail("the sender was told %v, the model expects %v", told, wantTold)
	}
	now = end
	advanceModel(now)
	tgt, _, _ = tgt.Tick(now)
	check(now)
	if !tgt.State.IsTerminal() {
		fail("target not final at the end: %s", tgt.State)
	}
}

// A control signal the target refuses when it arrives is rejected from the start:
// it was never handed over, so the sender may be told it was not delivered. Every
// other way a control record can end stays as it was: an accepted one is handed
// over and is never rejected afterwards.
func TestNewRejectedTargetControlRecord(t *testing.T) {
	t.Run("the record is born rejected with the reason and the deadlines of the envelope", func(t *testing.T) {
		rec := refusedControlRecord(t, RejectEndpointClosed)
		want := Record{
			Side:         SideTarget,
			State:        StateRejected,
			Reject:       RejectEndpointClosed,
			StateAt:      t0,
			Control:      true,
			AcceptBefore: controlAcceptBefore,
			ReportBy:     controlReportBy,
		}
		if !reflect.DeepEqual(rec, want) {
			t.Errorf("record = %+v, want %+v", rec, want)
		}
		if err := rec.Valid(); err != nil {
			t.Errorf("a refused control record is not valid: %v", err)
		}
		if got, want := rec.StatusReport(), (StatusReport{State: StateRejected, Reject: RejectEndpointClosed}); got != want {
			t.Errorf("report %+v, want %+v", got, want)
		}
	})
	t.Run("the reasons of the intake are accepted and no other", func(t *testing.T) {
		intake := map[RejectReason]bool{
			RejectUnauthorized: true, RejectEndpointMissing: true, RejectEndpointClosed: true, RejectEndpointDeclined: true,
			RejectTooLarge: true, RejectExpired: true, RejectMalformed: true, RejectDeadlineOutOfRange: true,
		}
		seen := 0
		for reason := RejectNone; reason.String() != "invalid"; reason++ {
			seen++
			rec, err := NewRejectedTargetControlRecord(t0, controlAcceptBefore, controlReportBy, reason)
			switch {
			case intake[reason] && err != nil:
				t.Errorf("%s: refused: %v", reason, err)
			case intake[reason] && rec.Valid() != nil:
				t.Errorf("%s: the record is not valid: %v", reason, rec.Valid())
			case !intake[reason] && !errors.Is(err, ErrInvalidInput):
				t.Errorf("%s: err = %v, want ErrInvalidInput", reason, err)
			}
		}
		if seen != len(intake)+4 { // none, backlog_expired, second_check_failed, backlog_full
			t.Errorf("the reasons changed (%d known): decide whether the new one is a reason of a control intake", seen)
		}
		if _, err := NewRejectedTargetControlRecord(t0, controlAcceptBefore, controlReportBy, RejectReason(200)); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("an unknown reason: err = %v, want ErrInvalidInput", err)
		}
	})
	t.Run("the time of the refusal is required", func(t *testing.T) {
		if _, err := NewRejectedTargetControlRecord(time.Time{}, controlAcceptBefore, controlReportBy, RejectExpired); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("err = %v, want ErrInvalidInput", err)
		}
	})
}

func TestARefusedControlRecordIsFinalAndNeverHandedOver(t *testing.T) {
	rec := refusedControlRecord(t, RejectUnauthorized)
	t.Run("time changes nothing, and no time-driven change is pending", func(t *testing.T) {
		if !rec.NextTick().IsZero() {
			t.Errorf("NextTick = %v, want none", rec.NextTick())
		}
		for _, now := range []time.Time{controlSettleBy, controlReportBy.Add(time.Hour)} {
			after, outs, err := rec.Tick(now)
			if err != nil || len(outs) != 0 || !reflect.DeepEqual(after, rec) {
				t.Errorf("Tick(%v): err %v, outcomes %+v, record %+v", now, err, outs, after)
			}
		}
	})
	t.Run("what can still be said about it changes nothing", func(t *testing.T) {
		for _, ev := range []Event{
			{Type: EventRejected, Reject: RejectEndpointClosed},
			{Type: EventDeliveryUnknown},
			{Type: EventReportByPassed},
		} {
			after, out, err := rec.Apply(at(time.Minute), ev)
			if err != nil || out.Result != ResultIgnored || !reflect.DeepEqual(after, rec) {
				t.Errorf("event %d: err %v, outcome %+v, record %+v", ev.Type, err, out, after)
			}
		}
	})
	t.Run("a committed reply contradicts it", func(t *testing.T) {
		after, _, err := rec.Apply(at(time.Minute), replyCommitted())
		if !errors.Is(err, ErrIllegalTransition) || !reflect.DeepEqual(after, rec) {
			t.Errorf("err = %v, record %+v", err, after)
		}
	})
	t.Run("a refusal reported to an accepted control record still ends as delivery_unknown", func(t *testing.T) {
		for _, reason := range []RejectReason{RejectEndpointClosed, RejectUnauthorized, RejectExpired} {
			accepted, out := mustApply(t, controlRecord(), at(time.Minute), Event{Type: EventRejected, Reject: reason})
			if accepted.State != StateDeliveryUnknown || out.To != StateDeliveryUnknown || accepted.Reject != RejectNone {
				t.Errorf("%s: state %s reject %s", reason, accepted.State, accepted.Reject)
			}
		}
	})
}
