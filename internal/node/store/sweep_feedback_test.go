package store_test

import (
	"path"
	"slices"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
)

// A notice is the proof that an item of feedback was told, and the replay of
// feedback reads it to take the item off the record that still holds it. A notice
// swept while the item is still there would let the next start tell it again.

// settledNotice is the notice that covers the feedback items of o, in a final
// state, so that it is finished and only its retention holds it.
func settledNotice(o store.OutRecord) store.InRecord {
	n := noticeFor(o)
	rec, out, err := n.Record.Apply(base, delivery.Event{Type: delivery.EventRejected, Reject: delivery.RejectEndpointClosed})
	if err != nil || out.Result != delivery.ResultApplied {
		panic("settledNotice: the record does not end")
	}
	n.Record, n.Body = rec, nil
	return n
}

// pendingOut is a source record with a feedback item that is told or not: the
// record the sender's node holds while the notice is yet to be cleared.
func pendingOut(i int, finished bool) store.OutRecord {
	o := srcOut(i, false)
	if finished {
		o = srcOutFinished(i)
	}
	o.Feedback = []delivery.Item{feedbackItem(i)}
	return o
}

func plannedRecord(plan store.SweepPlan, dir, name string) bool {
	for _, f := range plan.Records {
		if f.Dir == dir && f.Name == name {
			return true
		}
	}
	return false
}

func TestPlanSweepKeepsANoticeWhileAnItemItCoversIsStillOnItsRecord(t *testing.T) {
	late := base.Add(farAway)
	lostPending := lostNotice(1, 1, base)
	for _, c := range []struct {
		what string
		out  store.OutRecord
	}{
		{what: "a source record that is not finished", out: pendingOut(1, false)},
		{what: "a source record that is finished and whose sender is open", out: pendingOut(1, true)},
	} {
		t.Run(c.what, func(t *testing.T) {
			n := settledNotice(c.out)
			r := newRig(t)
			seed(t, r.mem, session(1, store.EndpointOpen, ctlName(4)), c.out, n)
			plan := store.PlanSweep(r.scan(), late)
			if plannedRecord(plan, store.InDir, n.Name()) {
				t.Errorf("the notice is swept while the item it covers is still on the source record")
			}
			// The item is cleared, as the engine does once the notice is written: the
			// notice is then free to go.
			cleared := c.out
			cleared.Feedback = nil
			seed(t, r.mem, cleared)
			plan = store.PlanSweep(r.scan(), late)
			if !plannedRecord(plan, store.InDir, n.Name()) {
				t.Errorf("the notice is kept although no record holds an item it covers")
			}
		})
	}
	t.Run("an out-lost record that is not finished", func(t *testing.T) {
		n := settledNotice(store.OutRecord{Key: store.OutKey{Sender: lostPending.Key.Sender, Message: msgID(7)}})
		n.Covers = noticeForLost(lostPending).Covers
		r := newRig(t)
		seed(t, r.mem, session(1, store.EndpointOpen, ctlName(4)), lostPending, n)
		if plan := store.PlanSweep(r.scan(), late); plannedRecord(plan, store.InDir, n.Name()) {
			t.Errorf("the notice is swept while the item it covers is still on the out-lost record")
		}
		cleared := lostPending
		cleared.Feedback = nil
		seed(t, r.mem, cleared)
		if plan := store.PlanSweep(r.scan(), late); !plannedRecord(plan, store.InDir, n.Name()) {
			t.Errorf("the notice is kept although the out-lost record holds no item")
		}
	})
}

// An item that is another recipient's or another kind than the one a cover names is
// not covered by it, and does not keep the notice.
func TestPlanSweepKeepsANoticeOnlyForTheItemsItCovers(t *testing.T) {
	covered := pendingOut(1, true)
	n := settledNotice(covered)
	other := covered
	other.Feedback = []delivery.Item{feedbackItem(1)}
	other.Feedback[0].Feedback.Kind = delivery.KindLostUnknownMessage
	other.Feedback[0].Ref.Recipient = addr(3, 3)
	r := newRig(t)
	seed(t, r.mem, session(1, store.EndpointOpen, ctlName(4)), other, n)
	if plan := store.PlanSweep(r.scan(), base.Add(farAway)); !plannedRecord(plan, store.InDir, n.Name()) {
		t.Errorf("the notice is kept for an item it does not cover")
	}
}

// A source record that still holds an item of feedback is swept only when its
// sender can no longer be told: the endpoint is closed or has no record, which is
// the rule for an out-lost record.
func TestPlanSweepTakesASourceRecordThatHoldsFeedbackOnlyWhenItsSenderCanNoLongerReadIt(t *testing.T) {
	late := base.Add(farAway)
	o := pendingOut(1, true)
	for _, c := range []struct {
		what  string
		eps   []store.Record
		swept bool
	}{
		{"an open sender", []store.Record{session(1, store.EndpointOpen, ctlName(4))}, false},
		{"a closed sender", []store.Record{session(1, store.EndpointClosed, ctlName(4))}, true},
		{"a sender with no record", nil, true},
	} {
		t.Run(c.what, func(t *testing.T) {
			r := newRig(t)
			seed(t, r.mem, append(c.eps, o)...)
			plan := store.PlanSweep(r.scan(), late)
			got := len(plan.Outs) == 1 && plan.Outs[0].Name() == o.Name()
			if got != c.swept {
				t.Errorf("source record planned = %v, want %v (outs %v)", got, c.swept, plan.Outs)
			}
		})
	}
	t.Run("a source record whose item is told goes as before", func(t *testing.T) {
		told := o
		told.Feedback = nil
		r := newRig(t)
		seed(t, r.mem, session(1, store.EndpointOpen, ctlName(4)), told)
		if plan := store.PlanSweep(r.scan(), late); len(plan.Outs) != 1 {
			t.Errorf("outs = %v", plan.Outs)
		}
	})
	// The summary of a record kept back is not in the plan: a plan names the
	// summaries of the records it removes.
	r := newRig(t)
	seed(t, r.mem, session(1, store.EndpointOpen, ctlName(4)), o)
	if plan := store.PlanSweep(r.scan(), late); len(plan.Summaries) != 0 {
		t.Errorf("summaries of records kept back: %v", plan.Summaries)
	}
}

// A notice and the record it covers may go together when the sender can no longer
// be told: the item leaves with its record.
func TestPlanSweepTakesANoticeWithTheClosedSendersRecordItCovers(t *testing.T) {
	o := pendingOut(1, true)
	n := settledNotice(o)
	r := newRig(t)
	seed(t, r.mem, session(1, store.EndpointClosed, ctlName(4)), o, n)
	plan := store.PlanSweep(r.scan(), base.Add(farAway))
	if len(plan.Outs) != 1 || !plannedRecord(plan, store.InDir, n.Name()) {
		t.Errorf("outs %v, notice planned %v; want both", plan.Outs, plannedRecord(plan, store.InDir, n.Name()))
	}
}

// A sender whose endpoint file is damaged, with a key that can be trusted, is not
// known to be closed: the feedback its source and out-lost records hold is kept for
// it. A sender whose file is not there at all, or is intact and closed, is the case
// of the rule above and is swept.
func TestPlanSweepKeepsTheFeedbackOfASenderWhoseEndpointFileIsDamaged(t *testing.T) {
	late := base.Add(farAway)
	out := pendingOut(1, true)
	lost := lostNotice(1, 1, base)
	r := newRig(t)
	seed(t, r.mem, session(1, store.EndpointOpen, ctlName(4)), out, lost)
	damageBody(t, r.mem, path.Join(store.EndpointsDir, session(1, store.EndpointOpen, ctlName(4)).Name()))
	sc := r.scan()
	if !slices.ContainsFunc(sc.Damaged, func(d store.DamagedFile) bool {
		_, trusted := d.Key.(ident.EndpointID)
		return d.Dir == store.EndpointsDir && trusted
	}) {
		t.Fatalf("the scenario has no damaged endpoint file with a key that can be trusted: %+v", sc.Damaged)
	}
	plan := store.PlanSweep(sc, late)
	if len(plan.Outs) != 0 {
		t.Errorf("a source record that holds feedback is swept: %v", plan.Outs)
	}
	if len(plan.OutLosts) != 0 {
		t.Errorf("an out-lost record that holds feedback is swept: %v", plan.OutLosts)
	}
}
