package envelope

import (
	"reflect"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
)

// acceptedOutbound is outboundTo with the delivery to every tracked endpoint
// accepted: the target has given a receipt.
func acceptedOutbound(t testing.TB, kind Kind, body bool) Outbound {
	t.Helper()
	o := outboundTo(t, kind, body)
	for i := range o.Recipients {
		d, _, err := o.Recipients[i].Delivery.Apply(t0.Add(time.Second), delivery.Event{Type: delivery.EventReceipt})
		requireNoErr(t, err)
		o.Recipients[i].Delivery = d
	}
	return o
}

// TestApplyNoRecordFindsTheTrackedDeliveryOnItsNode: a target's answer that it
// holds no record of a delivery is applied to the delivery the sender tracks for
// that endpoint, original or derived, if the delivery is on the answering node;
// the rule that judges it is delivery's alone.
func TestApplyNoRecordFindsTheTrackedDeliveryOnItsNode(t *testing.T) {
	now := t0.Add(time.Minute)
	nodeA := nodeOf(t, keyIdentityA)

	t.Run("an accepted delivery is lost at the target and the sender hears of it once", func(t *testing.T) {
		o := acceptedOutbound(t, KindMessage, true)
		n, eff, out, err := o.ApplyNoRecord(now, nodeA, ep(2))
		requireNoErr(t, err)
		got := n.Recipients[0].Delivery
		if eff != ReportApplied || got.State != delivery.StateDeliveryUnknown || got.Unknown != delivery.UnknownLostAtTarget {
			t.Fatalf("effect %v state %v cause %v", eff, got.State, got.Unknown)
		}
		if len(out.Feedback) != 1 || out.Feedback[0].Kind != delivery.KindLostAtTarget {
			t.Errorf("outcome %+v, want one lost_at_target notice", out)
		}
		if !n.Recipients[0].Arrival.ArrivedAt.IsZero() {
			t.Errorf("a loss opened the reply window: %+v", n.Recipients[0].Arrival)
		}
		if o.Recipients[0].Delivery.State != delivery.StateAccepted {
			t.Error("the input was changed")
		}
		_, eff, out, err = n.ApplyNoRecord(now.Add(time.Second), nodeA, ep(2))
		requireNoErr(t, err)
		if eff != ReportApplied || out.Result != delivery.ResultIgnored || len(out.Feedback) != 0 {
			t.Errorf("the same answer again: effect %v outcome %+v", eff, out)
		}
	})
	t.Run("a queued delivery has no receipt and stays as it is", func(t *testing.T) {
		o := outboundTo(t, KindMessage, true)
		n, eff, out, err := o.ApplyNoRecord(now, nodeA, ep(2))
		requireNoErr(t, err)
		if eff != ReportApplied || out.Result != delivery.ResultIgnored || out.Ignored != delivery.IgnoreNotApplicable || !reflect.DeepEqual(n, o) {
			t.Errorf("effect %v outcome %+v", eff, out)
		}
	})
	t.Run("the derived delivery is found like an original one", func(t *testing.T) {
		o := acceptedOutbound(t, KindSessionCreate, true)
		d, _, err := delivery.NewSourceRecord(t0, acceptBefore).Apply(t0.Add(time.Second), delivery.Event{Type: delivery.EventReceipt})
		requireNoErr(t, err)
		o.Derived = &Tracked{Address: addr(nodeA, 3), Delivery: d}
		o.ControlReplyDigest[0] = 1
		n, eff, out, err := o.ApplyNoRecord(now, nodeA, ep(3))
		requireNoErr(t, err)
		if eff != ReportApplied || n.Derived.Delivery.State != delivery.StateDeliveryUnknown || n.Recipients[0].Delivery.State != delivery.StateAccepted {
			t.Errorf("effect %v derived %v control %v", eff, n.Derived.Delivery.State, n.Recipients[0].Delivery.State)
		}
		if len(out.Feedback) != 1 || out.Feedback[0].Kind != delivery.KindLostAtTarget {
			t.Errorf("outcome %+v", out)
		}
		if o.Derived.Delivery.State != delivery.StateAccepted {
			t.Error("the input's derived delivery was changed")
		}
	})
	t.Run("an endpoint with no delivery changes nothing", func(t *testing.T) {
		o := acceptedOutbound(t, KindMessage, true)
		n, eff, out, err := o.ApplyNoRecord(now, nodeA, ep(9))
		requireNoErr(t, err)
		if eff != ReportNotTracked || !reflect.DeepEqual(out, delivery.Outcome{}) || !reflect.DeepEqual(n, o) {
			t.Errorf("effect %v outcome %+v", eff, out)
		}
	})
	t.Run("an answer from another node than the delivery's is refused", func(t *testing.T) {
		o := acceptedOutbound(t, KindMessage, true)
		n, eff, out, err := o.ApplyNoRecord(now, nodeOf(t, keyIdentityB), ep(2))
		requireErr(t, err, ErrMismatch)
		if eff != ReportNone || !reflect.DeepEqual(out, delivery.Outcome{}) || !reflect.DeepEqual(n, o) {
			t.Errorf("effect %v outcome %+v: a refused answer changed something", eff, out)
		}
	})
	t.Run("an answer after the pull window is ignored", func(t *testing.T) {
		o := acceptedOutbound(t, KindMessage, true)
		late := o.Recipients[0].Delivery.ReportBy.Add(time.Second)
		n, eff, out, err := o.ApplyNoRecord(late, nodeA, ep(2))
		requireNoErr(t, err)
		if eff != ReportApplied || out.Ignored != delivery.IgnoreRetryEnded || !reflect.DeepEqual(n, o) {
			t.Errorf("effect %v outcome %+v", eff, out)
		}
	})
	t.Run("the signal of a control delivery is lost at the target too", func(t *testing.T) {
		o := acceptedOutbound(t, KindSessionCreate, false)
		n, _, out, err := o.ApplyNoRecord(now, nodeA, ep(2))
		requireNoErr(t, err)
		if n.Recipients[0].Delivery.Unknown != delivery.UnknownLostAtTarget || len(out.Feedback) != 1 {
			t.Errorf("record %+v outcome %+v", n.Recipients[0].Delivery, out)
		}
	})
	t.Run("an invalid outbound record is refused", func(t *testing.T) {
		o := acceptedOutbound(t, KindMessage, true)
		o.Message = ""
		_, _, _, err := o.ApplyNoRecord(now, nodeA, ep(2))
		requireErr(t, err, ErrOutbound)
	})
	t.Run("a zero recipient is not tracked", func(t *testing.T) {
		o := acceptedOutbound(t, KindMessage, true)
		_, eff, _, err := o.ApplyNoRecord(now, nodeA, ident.EndpointID{})
		requireNoErr(t, err)
		if eff != ReportNotTracked {
			t.Errorf("effect %v", eff)
		}
	})
}
