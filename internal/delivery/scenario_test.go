package delivery

import (
	"errors"
	"testing"
	"time"
)

// pull gives the source what the target's status report says right now, the way
// a source learns anything: from the report the target gives when asked.
func pull(t *testing.T, src, tgt Record, now time.Time) (Record, []Feedback) {
	t.Helper()
	src, outs, err := src.ApplyStatusReport(now, tgt.StatusReport())
	if err != nil {
		t.Fatalf("ApplyStatusReport(%+v): %v", tgt.StatusReport(), err)
	}
	var fbs []Feedback
	for _, o := range outs {
		fbs = append(fbs, o.Feedback...)
	}
	return src, fbs
}

func TestScenarioLostReceiptThenRetry(t *testing.T) {
	key := DedupKey{SenderNode: node(1), Message: "m1", Recipient: endpoint(9)}
	digest := BodyDigest{7}
	ab := at(10 * time.Minute)

	var stored *DedupRecord
	if CheckDedup(stored, digest) != DedupNew {
		t.Fatal("first request is not new")
	}
	rec := NewDedupRecord(key, digest, ReportBy(ab))
	stored = &rec // the receipt is lost on the way back
	if got := CheckDedup(stored, digest); got != DedupReplay {
		t.Errorf("retry with the same id and body: %v, want the original record back", got)
	}
	if got := CheckDedup(stored, BodyDigest{8}); got != DedupConflict {
		t.Errorf("same id with a different body: %v, want conflict", got)
	}
}

func TestScenarioNormalDeliveryIsSilent(t *testing.T) {
	ab := at(10 * time.Minute)
	src := NewSourceRecord(ab)
	tgt, err := NewTargetRecord(at(time.Second), ab, src.ReportBy, DefaultEndpointMaxQueue)
	if err != nil {
		t.Fatal(err)
	}
	var all []Feedback
	src, fbs := pull(t, src, tgt, at(2*time.Second))
	all = append(all, fbs...)
	tgt, res, _ := tgt.Handout(at(time.Minute), AuthorizationAllow, 1, 1, testRand)
	if res.Action != HandoutGranted {
		t.Fatal(res)
	}
	tgt, _, _, _ = ackUnder(tgt, at(2*time.Minute), Ack{Part: 1, TokenMatches: true})
	src, fbs = pull(t, src, tgt, at(3*time.Minute))
	all = append(all, fbs...)
	if src.State != StateDelivered || len(all) != 0 {
		t.Errorf("state %s feedback %+v", src.State, all)
	}
}

func TestScenarioEachAnomalyOnce(t *testing.T) {
	scripts := []struct {
		rule  string
		steps []step
		want  Kind
	}{
		{"unreachable network", []step{failing(3*time.Minute, 0, CauseNetwork)}, KindUnreachable},
		{"unreachable trust suspended", []step{failing(3*time.Minute, 0, CauseTrustSuspended)}, KindUnreachable},
		{"rejected", []step{rejectedBy(time.Minute, RejectEndpointMissing)}, KindRejected},
		{"expired", []step{queried(afterExpiry)}, KindExpiredUnconfirmed},
		{"delivery unknown", []step{receipt(time.Second), unknown(time.Minute)}, KindDeliveryUnknown},
		{"delivery blocked", []step{receipt(time.Second), blocked(time.Hour, CauseRuntimeAuth)}, KindDeliveryBlocked},
	}
	for _, s := range scripts {
		t.Run(s.rule+" is reported exactly once however often the facts repeat", func(t *testing.T) {
			rec := NewSourceRecord(at(10 * time.Minute))
			var q FeedbackQueue
			count := 0
			for round := 0; round < 3; round++ {
				for _, st := range s.steps {
					var out Outcome
					var err error
					rec, out, err = rec.Apply(t0.Add(st.at+time.Duration(round)*time.Second), st.ev)
					if err != nil {
						t.Fatal(err)
					}
					for _, fb := range out.Feedback {
						if fb.Kind != s.want {
							t.Errorf("unexpected %s", fb.Kind)
						}
						count++
						_ = q.Add(Item{Ref: DeliveryRef{Sender: addr(1, 1), Message: "m1", Recipient: addr(2, 2)}, Feedback: fb, At: t0})
					}
				}
			}
			if count != 1 {
				t.Errorf("%d notices decided, want 1", count)
			}
			if got, _ := q.Flush(at(24*time.Hour), testParams); len(got) != 1 {
				t.Errorf("%d notices sent", len(got))
			}
		})
	}
}

func TestScenarioExpiredThenCorrectedOnce(t *testing.T) {
	ab := at(10 * time.Minute)
	src := NewSourceRecord(ab)
	var all []Kind
	src, out := mustApply(t, src, at(5*time.Minute), Event{Type: EventContactFailing, Cause: CauseNetwork, Since: t0})
	all = append(all, kindsOf(out.Feedback)...)
	src, out = mustApply(t, src, at(afterExpiry), Event{Type: EventExpiryQueried, QueriedAt: at(afterExpiry)})
	all = append(all, kindsOf(out.Feedback)...)
	// The target had the message all along and delivers it.
	tgt, _ := NewTargetRecord(at(9*time.Minute), ab, src.ReportBy, DefaultEndpointMaxQueue)
	tgt, _, _ = tgt.Handout(at(20*time.Minute), AuthorizationAllow, 1, 1, testRand)
	tgt, _, _, _ = ackUnder(tgt, at(21*time.Minute), Ack{Part: 1, TokenMatches: true})
	for i := 0; i < 3; i++ {
		var fbs []Feedback
		src, fbs = pull(t, src, tgt, at(time.Hour+time.Duration(i)*time.Minute))
		all = append(all, kindsOf(fbs)...)
	}
	want := []Kind{KindUnreachable, KindExpiredUnconfirmed, KindExpiredCorrected}
	if !sameKinds(all, want) || src.State != StateDelivered {
		t.Errorf("feedback %v state %s, want %v", all, src.State, want)
	}
}

func TestScenarioBacklogRejectionReachesASourceBehindNAT(t *testing.T) {
	ab := at(10 * time.Minute)
	src := NewSourceRecord(ab)
	tgt, _ := NewTargetRecord(at(time.Minute), ab, src.ReportBy, DefaultEndpointMaxQueue)

	tgt, outs, _ := tgt.Tick(at(time.Minute + DefaultEndpointMaxQueue))
	if tgt.State != StateRejected || tgt.Reject != RejectBacklogExpired || len(outs) != 1 {
		t.Fatalf("target %s/%s", tgt.State, tgt.Reject)
	}
	if !at(time.Minute + DefaultEndpointMaxQueue).Before(SettleBy(src.ReportBy)) {
		t.Fatal("the rejection came after the settle deadline")
	}
	// The target cannot reach the source; the source pulls later, before report_by.
	var all []Kind
	for i := 0; i < 3; i++ {
		var fbs []Feedback
		src, fbs = pull(t, src, tgt, at(2*time.Hour+time.Duration(i)*time.Minute))
		all = append(all, kindsOf(fbs)...)
	}
	if !sameKinds(all, []Kind{KindRejected}) || src.State != StateRejected || src.Reject != RejectBacklogExpired {
		t.Errorf("feedback %v state %s reason %s", all, src.State, src.Reject)
	}
}

func TestScenarioLongerEndpointQueueTimeIsCutBeforeReportBy(t *testing.T) {
	// The sender's report_by leaves less time than the endpoint's queue time.
	ab := at(10 * time.Minute)
	reportBy := ab.Add(time.Hour)
	tgt, err := NewTargetRecord(at(time.Minute), ab, reportBy, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	settle := SettleBy(reportBy)
	tgt, _, _ = tgt.Tick(settle.Add(-time.Second))
	if tgt.State != StateAccepted {
		t.Fatalf("rejected too early: %s", tgt.State)
	}
	tgt, _, _ = tgt.Tick(settle)
	if tgt.State != StateRejected || tgt.Reject != RejectBacklogExpired {
		t.Fatalf("at the settle deadline: %s/%s", tgt.State, tgt.Reject)
	}
	src := NewSourceRecord(ab)
	src.ReportBy = reportBy
	src, fbs := pull(t, src, tgt, settle.Add(time.Minute))
	if src.State != StateRejected || !sameKinds(kindsOf(fbs), []Kind{KindRejected}) || !settle.Add(time.Minute).Before(reportBy) {
		t.Errorf("source %s feedback %v", src.State, fbs)
	}
}

func TestScenarioLeaseRenewalBeyondCapEndsUnknownOnce(t *testing.T) {
	ab := at(10 * time.Minute)
	src := NewSourceRecord(ab)
	tgt, _ := NewTargetRecord(at(time.Minute), ab, src.ReportBy, 2*time.Hour)
	now := at(time.Minute)
	for lease := 0; lease < MaxLeases; lease++ {
		tgt, _, _ = tgt.Handout(now, AuthorizationAllow, 1, 1, testRand)
		// The agent keeps working, so the lease is renewed up to its cap and never acknowledged.
		for i := 0; i < 10; i++ {
			now = now.Add(2 * time.Minute)
			tgt, _ = tgt.Renew(now, true)
		}
		now = tgt.Lease.ExpiresAt
		tgt, _, _ = tgt.Tick(now)
	}
	if tgt.State != StateDeliveryUnknown {
		t.Fatalf("target state %s", tgt.State)
	}
	var all []Kind
	for i := 0; i < 3; i++ {
		var fbs []Feedback
		src, fbs = pull(t, src, tgt, now.Add(time.Duration(i)*time.Minute))
		all = append(all, kindsOf(fbs)...)
	}
	if !sameKinds(all, []Kind{KindDeliveryUnknown}) {
		t.Errorf("feedback %v", all)
	}
}

func TestScenarioAuthorizationSuspended(t *testing.T) {
	ab := at(10 * time.Minute)
	t.Run("at intake the sender keeps retrying and hears an unreachable hint with the trust cause", func(t *testing.T) {
		v, _ := Intake(at(time.Minute), ab, suspend)
		if v.Outcome != IntakeSuspended {
			t.Fatalf("verdict %+v", v)
		}
		src := NewSourceRecord(ab)
		src, out := mustApply(t, src, at(3*time.Minute), Event{Type: EventContactFailing, Cause: CauseTrustSuspended, Since: at(time.Minute)})
		if len(out.Feedback) != 1 || out.Feedback[0].Cause != CauseTrustSuspended || src.State != StateQueued {
			t.Errorf("outcome %+v state %s", out, src.State)
		}
		v, _ = Intake(at(8*time.Minute), ab, allow)
		if v.Outcome != IntakeAccept {
			t.Errorf("after recovery: %+v", v)
		}
	})
	t.Run("at recv the message is skipped, then delivered after recovery, and the sender hears nothing", func(t *testing.T) {
		src := NewSourceRecord(ab)
		tgt, _ := NewTargetRecord(at(time.Minute), ab, src.ReportBy, DefaultEndpointMaxQueue)
		var all []Feedback
		for i := 2; i < 6; i++ {
			var res HandoutResult
			tgt, res, _ = tgt.Handout(at(time.Duration(i)*time.Minute), AuthorizationSuspend, 1, 1, testRand)
			if res.Action != HandoutSkipped || tgt.Lease.Active || tgt.Sub != SubNone {
				t.Fatalf("round %d: %+v", i, res)
			}
			var fbs []Feedback
			src, fbs = pull(t, src, tgt, at(time.Duration(i)*time.Minute))
			all = append(all, fbs...)
		}
		tgt, res, _ := tgt.Handout(at(10*time.Minute), AuthorizationAllow, 1, 1, testRand)
		tgt, _, _, _ = ackUnder(tgt, at(11*time.Minute), Ack{Part: 1, TokenMatches: true})
		src, fbs := pull(t, src, tgt, at(12*time.Minute))
		all = append(all, fbs...)
		if res.Action != HandoutGranted || src.State != StateDelivered || len(all) != 0 {
			t.Errorf("res %+v state %s feedback %+v", res, src.State, all)
		}
	})
	t.Run("a suspension lasting to the queue time ends as backlog expiry", func(t *testing.T) {
		src := NewSourceRecord(ab)
		tgt, _ := NewTargetRecord(at(time.Minute), ab, src.ReportBy, DefaultEndpointMaxQueue)
		tgt, _, _ = tgt.Handout(at(10*time.Minute), AuthorizationSuspend, 1, 1, testRand)
		tgt, _, _ = tgt.Tick(at(time.Minute + DefaultEndpointMaxQueue))
		if tgt.State != StateRejected || tgt.Reject != RejectBacklogExpired {
			t.Errorf("target %s/%s", tgt.State, tgt.Reject)
		}
	})
	t.Run("revocation, local revoke or tightening reject at recv and the sender is told", func(t *testing.T) {
		src := NewSourceRecord(ab)
		tgt, _ := NewTargetRecord(at(time.Minute), ab, src.ReportBy, DefaultEndpointMaxQueue)
		tgt, res, _ := tgt.Handout(at(2*time.Minute), AuthorizationDeny, 1, 1, testRand)
		src, fbs := pull(t, src, tgt, at(3*time.Minute))
		if res.Action != HandoutDenied || src.State != StateRejected || src.Reject != RejectSecondCheckFailed || !sameKinds(kindsOf(fbs), []Kind{KindRejected}) {
			t.Errorf("res %+v source %s/%s feedback %v", res, src.State, src.Reject, fbs)
		}
	})
}

func TestScenarioPull(t *testing.T) {
	ab := at(10 * time.Minute)
	t.Run("a recv cut off mid-call is answered at once by the same endpoint, flagged as possible duplicate", func(t *testing.T) {
		tgt, _ := NewTargetRecord(t0, ab, ReportBy(ab), DefaultEndpointMaxQueue)
		tgt, first, _ := tgt.Handout(at(time.Minute), AuthorizationAllow, 1, 1, testRand)
		_, retry, _ := tgt.Handout(at(time.Minute+5*time.Second), AuthorizationAllow, 1, 1, testRand)
		if first.PossibleDuplicate || retry.Action != HandoutResent || !retry.PossibleDuplicate {
			t.Errorf("first %+v retry %+v", first, retry)
		}
	})
	t.Run("a model that never acknowledges stops being redelivered after the third redelivery, with one notice", func(t *testing.T) {
		src := NewSourceRecord(ab)
		tgt, _ := NewTargetRecord(t0, ab, src.ReportBy, 2*time.Hour)
		now := t0
		redeliveries := 0
		for i := 0; i < 10 && tgt.State == StateAccepted; i++ {
			var res HandoutResult
			tgt, res, _ = tgt.Handout(now, AuthorizationAllow, 1, 1, testRand)
			if res.Action != HandoutGranted {
				t.Fatalf("round %d: %+v", i, res)
			}
			if res.PossibleDuplicate {
				redeliveries++
			}
			now = now.Add(LeaseDuration)
			tgt, _, _ = tgt.Tick(now)
		}
		var all []Kind
		for i := 0; i < 2; i++ {
			var fbs []Feedback
			src, fbs = pull(t, src, tgt, now.Add(time.Duration(i)*time.Minute))
			all = append(all, kindsOf(fbs)...)
		}
		if redeliveries != MaxRedeliveries || tgt.State != StateDeliveryUnknown || !sameKinds(all, []Kind{KindDeliveryUnknown}) {
			t.Errorf("redeliveries %d state %s feedback %v", redeliveries, tgt.State, all)
		}
	})
	t.Run("another endpoint's token cannot acknowledge the lease", func(t *testing.T) {
		tgt, _ := NewTargetRecord(t0, ab, ReportBy(ab), DefaultEndpointMaxQueue)
		tgt, _, _ = tgt.Handout(at(time.Minute), AuthorizationAllow, 1, 1, testRand)
		tgt, v, _, _ := ackUnder(tgt, at(2*time.Minute), Ack{Part: 1, TokenMatches: false})
		if v != AckRefusedToken || tgt.State != StateAccepted || !tgt.Lease.Active {
			t.Errorf("verdict %v state %s", v, tgt.State)
		}
	})
}

func TestScenarioOfflineNodeGetsOneMergedNotice(t *testing.T) {
	ref := func(i int) DeliveryRef {
		return DeliveryRef{Sender: addr(1, 1), Message: MessageID(string(rune('A' + i))), Recipient: addr(2, 5)}
	}
	var q FeedbackQueue
	srcs := make([]Record, 20)
	add := func(i int, fbs []Feedback, when time.Duration) {
		for _, fb := range fbs {
			if err := q.Add(Item{Ref: ref(i), Feedback: fb, At: at(when)}); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Phase one: contact keeps failing until the deliveries are about to lapse.
	for i := range srcs {
		when := 3*time.Minute + time.Duration(i)*time.Second
		var out Outcome
		srcs[i], out = mustApply(t, NewSourceRecord(at(10*time.Minute)), at(when), Event{Type: EventContactFailing, Cause: CauseNetwork, Since: t0})
		add(i, out.Feedback, when)
	}
	got, err := q.Flush(at(5*time.Minute), testParams)
	if err != nil || len(got) != 1 || len(got[0].Items) != 20 || got[0].Items[0].Feedback.Kind != KindUnreachable {
		t.Fatalf("unreachable phase: notices %d err %v", len(got), err)
	}

	// Phase two: the node is still offline after every deadline, so each
	// delivery is judged expired; the sender again hears of it once.
	for i := range srcs {
		when := afterExpiry + time.Duration(i)*time.Second
		var out Outcome
		srcs[i], out = mustApply(t, srcs[i], at(when), Event{Type: EventExpiryQueried, QueriedAt: at(when)})
		add(i, out.Feedback, when)
	}
	got, err = q.Flush(at(afterExpiry+5*time.Minute), testParams)
	if err != nil || len(got) != 1 || len(got[0].Items) != 20 || got[0].Items[0].Feedback.Kind != KindExpiredUnconfirmed {
		t.Fatalf("expiry phase: notices %+v err %v", got, err)
	}
}

func TestScenarioForcedClosure(t *testing.T) {
	ab := at(10 * time.Minute)
	fresh := func(t *testing.T) Record {
		r, err := NewTargetRecord(t0, ab, ReportBy(ab), DefaultEndpointMaxQueue)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	requeued := func(t *testing.T) Record {
		r := handOut(t, fresh(t), at(time.Minute), 1, 1)
		r, outs, _ := r.Tick(at(6 * time.Minute))
		if len(outs) != 1 || !outs[0].Requeued || r.Lease.Active || r.Lease.Handouts != 1 {
			t.Fatalf("not requeued: %+v", outs)
		}
		return r
	}
	denied := func(t *testing.T, r Record, now time.Duration) Record {
		r, res, err := r.Handout(at(now), AuthorizationDeny, 1, 1, testRand)
		if err != nil || res.Action != HandoutDenied {
			t.Fatalf("%+v %v", res, err)
		}
		return r
	}
	endpointClosed := func(t *testing.T, r Record, now time.Duration) Record {
		r, _ = mustApply(t, r, at(now), Event{Type: EventRejected, Reject: RejectEndpointClosed})
		return r
	}
	rows := []struct {
		rule       string
		close      func(*testing.T) Record
		wantState  State
		wantReject RejectReason
	}{
		{"a queued message never handed out is rejected when its backlog deadline passes", func(t *testing.T) Record {
			r, _, _ := fresh(t).Tick(at(DefaultEndpointMaxQueue))
			return r
		}, StateRejected, RejectBacklogExpired},
		{"a message handed out, requeued and past its backlog deadline is delivery_unknown, not rejected", func(t *testing.T) Record {
			r, _, _ := requeued(t).Tick(at(DefaultEndpointMaxQueue))
			return r
		}, StateDeliveryUnknown, RejectNone},
		{"a second check denied at the first hand-out rejects", func(t *testing.T) Record {
			return denied(t, fresh(t), 2*time.Minute)
		}, StateRejected, RejectSecondCheckFailed},
		{"a second check denied under a live lease is delivery_unknown", func(t *testing.T) Record {
			return denied(t, handOut(t, fresh(t), at(time.Minute), 1, 1), 2*time.Minute)
		}, StateDeliveryUnknown, RejectNone},
		{"a second check denied after a requeue is delivery_unknown", func(t *testing.T) Record {
			return denied(t, requeued(t), 7*time.Minute)
		}, StateDeliveryUnknown, RejectNone},
		{"only a prompt was delivered, the body never handed out: a denied second check still rejects", func(t *testing.T) Record {
			r, _ := mustApply(t, fresh(t), at(time.Minute), Event{Type: EventNotified})
			return denied(t, r, 2*time.Minute)
		}, StateRejected, RejectSecondCheckFailed},
		{"an endpoint closed before any hand-out rejects", func(t *testing.T) Record {
			return endpointClosed(t, fresh(t), 2*time.Minute)
		}, StateRejected, RejectEndpointClosed},
		{"an endpoint closed after a hand-out is delivery_unknown", func(t *testing.T) Record {
			return endpointClosed(t, handOut(t, fresh(t), at(time.Minute), 1, 1), 2*time.Minute)
		}, StateDeliveryUnknown, RejectNone},
		{"an endpoint closed after a requeue is delivery_unknown", func(t *testing.T) Record {
			return endpointClosed(t, requeued(t), 7*time.Minute)
		}, StateDeliveryUnknown, RejectNone},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			tgt := r.close(t)
			if tgt.State != r.wantState || tgt.Reject != r.wantReject {
				t.Fatalf("target %s/%s, want %s/%s", tgt.State, tgt.Reject, r.wantState, r.wantReject)
			}
			// The sender learns the closure from its pulls, however often it asks.
			src := NewSourceRecord(ab)
			var all []Feedback
			for i := 0; i < 3; i++ {
				var fbs []Feedback
				src, fbs = pull(t, src, tgt, at(time.Hour+time.Duration(i)*time.Minute))
				all = append(all, fbs...)
			}
			if tgt.State == StateRejected {
				if !sameKinds(kindsOf(all), []Kind{KindRejected}) || all[0].Reject != r.wantReject || src.Reject != r.wantReject {
					t.Errorf("feedback %+v source %s/%s", all, src.State, src.Reject)
				}
				return
			}
			if !sameKinds(kindsOf(all), []Kind{KindDeliveryUnknown}) || src.State != StateDeliveryUnknown {
				t.Fatalf("feedback %v source %s: delivery_unknown must be told once and rejected never", kindsOf(all), src.State)
			}
			// The agent did see the body: its late acknowledgement corrects the unknown.
			tgt, verdict, _ := ack(t, tgt, at(2*time.Hour), 1)
			if verdict != AckCompleted || tgt.State != StateDelivered {
				t.Fatalf("late acknowledgement: verdict %v state %s", verdict, tgt.State)
			}
			for i := 0; i < 2; i++ {
				var fbs []Feedback
				src, fbs = pull(t, src, tgt, at(2*time.Hour+time.Duration(i+1)*time.Minute))
				all = append(all, fbs...)
			}
			if !sameKinds(kindsOf(all), []Kind{KindDeliveryUnknown, KindUnknownCorrected}) || src.State != StateDelivered {
				t.Errorf("feedback %v source %s", kindsOf(all), src.State)
			}
		})
	}
}

func TestScenarioStatusReportApplication(t *testing.T) {
	ab := at(10 * time.Minute)
	blockedTarget := func(t *testing.T) Record {
		r, _ := NewTargetRecord(at(time.Minute), ab, ReportBy(ab), DefaultEndpointMaxQueue)
		r, _ = mustApply(t, r, at(2*time.Minute), Event{Type: EventBlocked, Cause: CauseRuntimeAuth})
		return r
	}
	t.Run("a blocked report pulled before any receipt is still reported once", func(t *testing.T) {
		src := NewSourceRecord(ab)
		var all []Kind
		for i := 0; i < 3; i++ {
			var fbs []Feedback
			src, fbs = pull(t, src, blockedTarget(t), at(3*time.Minute+time.Duration(i)*time.Minute))
			all = append(all, kindsOf(fbs)...)
		}
		if !sameKinds(all, []Kind{KindDeliveryBlocked}) || src.State != StateAccepted || !src.Blocked {
			t.Errorf("feedback %v state %s blocked %v", all, src.State, src.Blocked)
		}
	})
	t.Run("from expired, pulling accepted and blocked yields the correction and the blocked notice", func(t *testing.T) {
		src, _ := mustApply(t, NewSourceRecord(ab), at(afterExpiry), Event{Type: EventExpiryQueried, QueriedAt: at(afterExpiry)})
		if src.State != StateExpiredUnconfirmed {
			t.Fatalf("state %s", src.State)
		}
		src, fbs := pull(t, src, blockedTarget(t), at(20*time.Minute))
		if !sameKinds(kindsOf(fbs), []Kind{KindExpiredCorrected, KindDeliveryBlocked}) || src.State != StateAccepted {
			t.Errorf("feedback %v state %s", kindsOf(fbs), src.State)
		}
	})
	t.Run("a block that ended is recovered by the next pull", func(t *testing.T) {
		tgt := blockedTarget(t)
		src, _ := pull(t, NewSourceRecord(ab), tgt, at(3*time.Minute))
		tgt, _ = mustApply(t, tgt, at(4*time.Minute), Event{Type: EventUnblocked})
		src, fbs := pull(t, src, tgt, at(5*time.Minute))
		if !sameKinds(kindsOf(fbs), []Kind{KindBlockedRecovered}) || src.Blocked {
			t.Errorf("feedback %v blocked %v", kindsOf(fbs), src.Blocked)
		}
	})
	t.Run("a block reported for a state that is not accepted is not applied", func(t *testing.T) {
		src := NewSourceRecord(ab)
		src, outs, err := src.ApplyStatusReport(at(time.Minute), StatusReport{State: StateDeliveryUnknown, Blocked: true, BlockedCause: CauseRuntimeAuth})
		if err != nil || len(outs) != 1 || src.Blocked || src.Reported.Has(KindDeliveryBlocked) {
			t.Errorf("outs %+v blocked %v reported %b err %v", outs, src.Blocked, src.Reported, err)
		}
	})
	t.Run("only the sender applies a report", func(t *testing.T) {
		tgt := blockedTarget(t)
		if _, _, err := tgt.ApplyStatusReport(at(time.Minute), tgt.StatusReport()); !errors.Is(err, ErrWrongSide) {
			t.Errorf("err %v", err)
		}
	})
	t.Run("a report of a state a target does not have is invalid and changes nothing", func(t *testing.T) {
		src := NewSourceRecord(ab)
		for _, st := range []State{StateInvalid, StateQueued, StateExpiredUnconfirmed} {
			n, outs, err := src.ApplyStatusReport(at(time.Minute), StatusReport{State: st})
			if !errors.Is(err, ErrInvalidInput) || len(outs) != 0 || n.State != StateQueued {
				t.Errorf("state %s: outs %+v err %v", st, outs, err)
			}
		}
	})
	t.Run("a report with a block cause that is not a block cause is invalid and changes nothing", func(t *testing.T) {
		src := NewSourceRecord(ab)
		n, outs, err := src.ApplyStatusReport(at(time.Minute), StatusReport{State: StateAccepted, Blocked: true, BlockedCause: CauseNetwork})
		if !errors.Is(err, ErrInvalidEvent) || len(outs) != 0 || n.State != StateQueued {
			t.Errorf("state %s outs %+v err %v", n.State, outs, err)
		}
	})
	t.Run("a report of closed_unconfirmed gives the sender nothing to apply", func(t *testing.T) {
		src := NewSourceRecord(ab)
		n, outs, err := src.ApplyStatusReport(at(time.Minute), StatusReport{State: StateClosedUnconfirmed})
		if err != nil || len(outs) != 0 || n.State != StateQueued {
			t.Errorf("state %s outs %+v err %v", n.State, outs, err)
		}
	})
}
