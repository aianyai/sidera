package envelope

import (
	"reflect"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/timing"
)

// TestApplyObservationAppliesWhatTheSenderSeesItself: the facts the sender
// observes (a query that found no receipt, contact that keeps failing, the passing
// of report_by) are applied to the delivery the sender tracks for the endpoint,
// original or derived, on the node they are about; the rule that judges each is
// delivery's alone.
func TestApplyObservationAppliesWhatTheSenderSeesItself(t *testing.T) {
	nodeA := nodeOf(t, keyIdentityA)
	queried := func(at time.Time) delivery.Event {
		return delivery.Event{Type: delivery.EventExpiryQueried, QueriedAt: at}
	}

	t.Run("a query after the deadline and the tolerance that found no receipt expires a queued delivery once", func(t *testing.T) {
		o := outboundTo(t, KindMessage, true)
		late := acceptBefore.Add(timing.ClockTolerance + time.Second)
		n, eff, out, err := o.ApplyObservation(late, nodeA, ep(2), queried(late))
		requireNoErr(t, err)
		if eff != ReportApplied || n.Recipients[0].Delivery.State != delivery.StateExpiredUnconfirmed {
			t.Fatalf("effect %v state %v, want expired_unconfirmed", eff, n.Recipients[0].Delivery.State)
		}
		if len(out.Feedback) != 1 || out.Feedback[0].Kind != delivery.KindExpiredUnconfirmed {
			t.Errorf("outcome %+v, want one expired_unconfirmed notice", out)
		}
		if o.Recipients[0].Delivery.State != delivery.StateQueued {
			t.Error("the input was changed")
		}
		_, _, again, err := n.ApplyObservation(late.Add(time.Second), nodeA, ep(2), queried(late.Add(time.Second)))
		requireNoErr(t, err)
		if again.Result != delivery.ResultIgnored || len(again.Feedback) != 0 {
			t.Errorf("the same query again: %+v", again)
		}
	})
	t.Run("a query made before the tolerance is over changes nothing", func(t *testing.T) {
		o := outboundTo(t, KindMessage, true)
		early := acceptBefore.Add(time.Second)
		n, eff, out, err := o.ApplyObservation(early, nodeA, ep(2), queried(early))
		requireNoErr(t, err)
		if eff != ReportApplied || out.Ignored != delivery.IgnoreNotDueYet || !reflect.DeepEqual(n, o) {
			t.Errorf("effect %v outcome %+v", eff, out)
		}
	})
	t.Run("contact that has failed for two minutes before the deadline gives the unreachable hint once", func(t *testing.T) {
		o := outboundTo(t, KindMessage, true)
		now := t0.Add(3 * time.Minute)
		ev := delivery.Event{Type: delivery.EventContactFailing, Cause: delivery.CauseNetwork, Since: t0}
		n, eff, out, err := o.ApplyObservation(now, nodeA, ep(2), ev)
		requireNoErr(t, err)
		if eff != ReportApplied || len(out.Feedback) != 1 || out.Feedback[0].Kind != delivery.KindUnreachable || out.Feedback[0].Cause != delivery.CauseNetwork {
			t.Fatalf("effect %v outcome %+v, want one unreachable notice with the network cause", eff, out)
		}
		_, _, again, err := n.ApplyObservation(now.Add(time.Second), nodeA, ep(2), ev)
		requireNoErr(t, err)
		if again.Ignored != delivery.IgnoreAlreadyReported || len(again.Feedback) != 0 {
			t.Errorf("the same failure again: %+v", again)
		}
	})
	t.Run("contact that fails for an accepted delivery is no news the delivery rules know of", func(t *testing.T) {
		o := acceptedOutbound(t, KindMessage, true)
		now := t0.Add(3 * time.Minute)
		n, eff, out, err := o.ApplyObservation(now, nodeA, ep(2), delivery.Event{Type: delivery.EventContactFailing, Cause: delivery.CauseNetwork, Since: t0})
		requireNoErr(t, err)
		if eff != ReportApplied || out.Ignored != delivery.IgnoreNotApplicable || !reflect.DeepEqual(n, o) {
			t.Errorf("effect %v outcome %+v", eff, out)
		}
	})
	t.Run("report_by passing closes the original delivery and the derived one", func(t *testing.T) {
		o := acceptedOutbound(t, KindSessionCreate, true)
		d, _, err := delivery.NewSourceRecord(t0, acceptBefore).Apply(t0.Add(time.Second), delivery.Event{Type: delivery.EventReceipt})
		requireNoErr(t, err)
		o.Derived = &Tracked{Address: addr(nodeA, 3), Delivery: d}
		o.ControlReplyDigest[0] = 1
		after := o.Recipients[0].Delivery.ReportBy.Add(time.Second)
		ev := delivery.Event{Type: delivery.EventReportByPassed}
		n, eff, out, err := o.ApplyObservation(after, nodeA, ep(2), ev)
		requireNoErr(t, err)
		if eff != ReportApplied || n.Recipients[0].Delivery.State != delivery.StateClosedUnconfirmed ||
			len(out.Feedback) != 1 || out.Feedback[0].Kind != delivery.KindAcceptedUnconfirmed {
			t.Fatalf("effect %v state %v outcome %+v, want closed_unconfirmed and the notice that the result could not be learned", eff, n.Recipients[0].Delivery.State, out)
		}
		n, _, _, err = n.ApplyObservation(after, nodeA, ep(3), ev)
		requireNoErr(t, err)
		if n.Derived.Delivery.State != delivery.StateClosedUnconfirmed {
			t.Errorf("the derived delivery is %v", n.Derived.Delivery.State)
		}
	})
	t.Run("what a target reports is not an observation", func(t *testing.T) {
		o := acceptedOutbound(t, KindMessage, true)
		for _, ev := range []delivery.EventType{delivery.EventReceipt, delivery.EventDelivered, delivery.EventRejected, delivery.EventDeliveryUnknown, delivery.EventNoRecordAtTarget} {
			n, eff, _, err := o.ApplyObservation(t0.Add(time.Minute), nodeA, ep(2), delivery.Event{Type: ev})
			requireErr(t, err, ErrObservation)
			if eff != ReportNone || !reflect.DeepEqual(n, o) {
				t.Errorf("event %v: effect %v, record changed %v", ev, eff, !reflect.DeepEqual(n, o))
			}
		}
	})
	t.Run("an endpoint with no delivery changes nothing", func(t *testing.T) {
		o := outboundTo(t, KindMessage, true)
		n, eff, _, err := o.ApplyObservation(t0, nodeA, ep(9), delivery.Event{Type: delivery.EventReportByPassed})
		requireNoErr(t, err)
		if eff != ReportNotTracked || !reflect.DeepEqual(n, o) {
			t.Errorf("effect %v", eff)
		}
	})
	t.Run("an observation about another node than the delivery's is refused", func(t *testing.T) {
		o := outboundTo(t, KindMessage, true)
		n, eff, _, err := o.ApplyObservation(t0, nodeOf(t, keyIdentityB), ep(2), delivery.Event{Type: delivery.EventReportByPassed})
		requireErr(t, err, ErrMismatch)
		if eff != ReportNone || !reflect.DeepEqual(n, o) {
			t.Errorf("effect %v: a refused observation changed something", eff)
		}
	})
}
