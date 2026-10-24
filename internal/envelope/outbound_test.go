package envelope

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

var (
	sentAt       = t0
	acceptBefore = t0.Add(10 * time.Minute)
)

// outboundTo is the sender's record of a message of the given kind to endpoint
// 1 of node A, sent from local endpoint 0x0a, with every delivery still queued.
func outboundTo(t testing.TB, kind Kind, body bool) Outbound {
	t.Helper()
	return Outbound{
		Message: "ctl-0001", From: ep(0x0a), Kind: kind, BodyNonEmpty: body, AcceptBefore: acceptBefore,
		Recipients: []Tracked{{Address: addr(nodeOf(t, keyIdentityA), 2), Delivery: delivery.NewSourceRecord(acceptBefore)}},
	}
}

func createOutbound(t testing.TB) Outbound { return outboundTo(t, KindSessionCreate, true) }

func signedReport(t testing.TB, r StatusReport) SignedReport {
	t.Helper()
	sr, err := VerifyStatusReport(sealReport(t, r, keyNetworkA), nodeOf(t, keyIdentityA), localNode(t), acceptedFor(t, keyIdentityA, keyNetworkA), t0.Add(24*time.Hour))
	requireNoErr(t, err)
	return sr
}

func signedReply(t testing.TB, r ControlReply) SignedControlReply {
	t.Helper()
	sc, err := VerifyControlReply(sealReply(t, r, keyNetworkA), nodeOf(t, keyIdentityA), localNode(t), acceptedFor(t, keyIdentityA, keyNetworkA), t0.Add(24*time.Hour))
	requireNoErr(t, err)
	return sc
}

func reportFor(msg delivery.MessageID, to byte, st delivery.StatusReport, reported time.Duration, delivered time.Duration) StatusReport {
	r := StatusReport{Message: msg, Recipient: ep(to), Status: st, ReportedAt: t0.Add(reported)}
	if delivered != 0 {
		r.DeliveredAt = t0.Add(delivered)
	}
	return r
}

func createdReply(msg delivery.MessageID, runtime, session byte, issued time.Duration) ControlReply {
	return ControlReply{Message: msg, Recipient: ep(runtime), IssuedAt: t0.Add(issued), Result: ControlResult{
		Signal: adapterproto.SignalSessionCreate,
		Create: &CreateResult{Outcome: adapterproto.CreateReplyCreated, SessionEndpoint: ep(session)}}}
}

func failedReply(msg delivery.MessageID, runtime byte, issued time.Duration) ControlReply {
	return ControlReply{Message: msg, Recipient: ep(runtime), IssuedAt: t0.Add(issued), Result: ControlResult{
		Signal: adapterproto.SignalSessionCreate,
		Create: &CreateResult{Outcome: adapterproto.CreateReplyFailed, Reason: adapterproto.CreateReasonNativeError}}}
}

func accepted() delivery.StatusReport { return delivery.StatusReport{State: delivery.StateAccepted} }
func delivered() delivery.StatusReport {
	return delivery.StatusReport{State: delivery.StateDelivered}
}

func TestOutboundValidate(t *testing.T) {
	good := createOutbound(t)
	requireNoErr(t, good.Validate())
	withDerived := good
	withDerived.Derived = &Tracked{Address: addr(nodeOf(t, keyIdentityA), 3), Delivery: delivery.NewSourceRecord(acceptBefore)}
	withDerived.ControlReplyDigest = record.Digest{1}
	requireNoErr(t, withDerived.Validate())
	failedReplyAccepted := good
	failedReplyAccepted.ControlReplyDigest = record.Digest{2}
	requireNoErr(t, failedReplyAccepted.Validate())

	cases := map[string]func(*Outbound){
		"no message":         func(o *Outbound) { o.Message = "" },
		"no sender endpoint": func(o *Outbound) { o.From = ident.EndpointID{} },
		"invalid kind":       func(o *Outbound) { o.Kind = KindInvalid },
		"no deadline":        func(o *Outbound) { o.AcceptBefore = time.Time{} },
		"no recipients":      func(o *Outbound) { o.Recipients = nil },
		"a control signal, two recipients": func(o *Outbound) {
			o.Recipients = append(o.Recipients, Tracked{Address: addr(nodeOf(t, keyIdentityA), 9), Delivery: delivery.NewSourceRecord(acceptBefore)})
		},
		"derived without a body": func(o *Outbound) { o.BodyNonEmpty = false; o.Derived = withDerived.Derived },
		"derived on a message":   func(o *Outbound) { o.Kind = KindMessage; o.Derived = withDerived.Derived },
		"derived without the digest of the reply that bound it": func(o *Outbound) {
			o.Derived = withDerived.Derived
			o.ControlReplyDigest = record.Digest{}
		},
		"a control reply digest on a message": func(o *Outbound) { o.Kind = KindMessage; o.ControlReplyDigest = record.Digest{1} },
		"derived on another node": func(o *Outbound) {
			o.Derived = &Tracked{Address: addr(nodeOf(t, keyIdentityB), 3), Delivery: delivery.NewSourceRecord(acceptBefore)}
			o.ControlReplyDigest = record.Digest{1}
		},
		"derived endpoint tracked twice": func(o *Outbound) {
			o.Derived = &Tracked{Address: o.Recipients[0].Address, Delivery: delivery.NewSourceRecord(acceptBefore)}
			o.ControlReplyDigest = record.Digest{1}
		},
		"a target-side record": func(o *Outbound) {
			o.Recipients = []Tracked{{Address: o.Recipients[0].Address, Delivery: delivery.NewTargetControlRecord(acceptBefore, delivery.ReportBy(acceptBefore))}}
		},
		"a zero record":  func(o *Outbound) { o.Recipients = []Tracked{{Address: o.Recipients[0].Address}} },
		"a zero address": func(o *Outbound) { o.Recipients = []Tracked{{Delivery: delivery.NewSourceRecord(acceptBefore)}} },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			o := good
			mut(&o)
			requireErr(t, o.Validate(), ErrOutbound)
		})
	}
	t.Run("a message may have several recipients", func(t *testing.T) {
		o := outboundTo(t, KindMessage, true)
		o.Recipients = append(o.Recipients, Tracked{Address: addr(nodeOf(t, keyIdentityB), 9), Delivery: delivery.NewSourceRecord(acceptBefore)})
		requireNoErr(t, o.Validate())
	})
}

func TestApplyReport(t *testing.T) {
	now := t0.Add(time.Hour)
	t.Run("a delivered report ends the delivery and gives the delivery time", func(t *testing.T) {
		o := outboundTo(t, KindMessage, true)
		n, eff, outs, err := o.ApplyReport(now, signedReport(t, reportFor("ctl-0001", 2, delivered(), 760*time.Second, 700*time.Second)))
		requireNoErr(t, err)
		got := n.Recipients[0]
		if eff != ReportApplied || got.Delivery.State != delivery.StateDelivered || len(outs) == 0 {
			t.Fatalf("effect %v state %v outcomes %v", eff, got.Delivery.State, outs)
		}
		if !got.Arrival.DeliveredAt.Equal(t0.Add(700*time.Second)) || !got.Arrival.ArrivedAt.Equal(t0.Add(760*time.Second)) {
			t.Errorf("arrival %+v", got.Arrival)
		}
	})
	t.Run("an accepted report proves arrival without a delivery time", func(t *testing.T) {
		n, _, _, err := outboundTo(t, KindMessage, true).ApplyReport(now, signedReport(t, reportFor("ctl-0001", 2, accepted(), 100*time.Second, 0)))
		requireNoErr(t, err)
		got := n.Recipients[0]
		if got.Delivery.State != delivery.StateAccepted || !got.Arrival.DeliveredAt.IsZero() || !got.Arrival.ArrivedAt.Equal(t0.Add(100*time.Second)) {
			t.Errorf("state %v arrival %+v", got.Delivery.State, got.Arrival)
		}
	})
	t.Run("a delivery_unknown report proves arrival", func(t *testing.T) {
		n, _, _, err := outboundTo(t, KindMessage, true).ApplyReport(now, signedReport(t, reportFor("ctl-0001", 2, delivery.StatusReport{State: delivery.StateDeliveryUnknown, Unknown: delivery.UnknownHandedOver}, 100*time.Second, 0)))
		requireNoErr(t, err)
		if n.Recipients[0].Arrival.ArrivedAt.IsZero() {
			t.Error("no arrival")
		}
	})
	t.Run("a lost_at_target report carries its cause to the record, gives its own notice and proves no arrival", func(t *testing.T) {
		st := delivery.StatusReport{State: delivery.StateDeliveryUnknown, Unknown: delivery.UnknownLostAtTarget}
		n, _, outs, err := outboundTo(t, KindMessage, true).ApplyReport(now, signedReport(t, reportFor("ctl-0001", 2, st, 100*time.Second, 0)))
		requireNoErr(t, err)
		got := n.Recipients[0]
		if got.Delivery.State != delivery.StateDeliveryUnknown || got.Delivery.Unknown != delivery.UnknownLostAtTarget {
			t.Errorf("state %v cause %v", got.Delivery.State, got.Delivery.Unknown)
		}
		if !got.Arrival.ArrivedAt.IsZero() {
			t.Errorf("a loss found at %v opened the reply window", got.Arrival.ArrivedAt)
		}
		if len(outs) != 1 || len(outs[0].Feedback) != 1 || outs[0].Feedback[0].Kind != delivery.KindLostAtTarget {
			t.Errorf("outcomes %+v, want one lost_at_target notice", outs)
		}
	})
	t.Run("a rejected report proves no arrival", func(t *testing.T) {
		n, _, outs, err := outboundTo(t, KindMessage, true).ApplyReport(now, signedReport(t,
			reportFor("ctl-0001", 2, delivery.StatusReport{State: delivery.StateRejected, Reject: delivery.RejectUnauthorized}, 100*time.Second, 0)))
		requireNoErr(t, err)
		got := n.Recipients[0]
		if got.Delivery.State != delivery.StateRejected || got.Delivery.Reject != delivery.RejectUnauthorized || got.Arrival != (Arrival{}) {
			t.Errorf("state %v reason %v arrival %+v", got.Delivery.State, got.Delivery.Reject, got.Arrival)
		}
		if len(outs) == 0 || len(outs[0].Feedback) != 1 || outs[0].Feedback[0].Kind != delivery.KindRejected {
			t.Errorf("outcomes %+v: the sender must hear of a rejection once", outs)
		}
	})
	t.Run("a report of a full backlog rejects the delivery with that reason and the sender hears of it once", func(t *testing.T) {
		n, _, outs, err := outboundTo(t, KindMessage, true).ApplyReport(now, signedReport(t,
			reportFor("ctl-0001", 2, delivery.StatusReport{State: delivery.StateRejected, Reject: delivery.RejectBacklogFull}, 100*time.Second, 0)))
		requireNoErr(t, err)
		got := n.Recipients[0]
		if got.Delivery.State != delivery.StateRejected || got.Delivery.Reject != delivery.RejectBacklogFull || got.Arrival != (Arrival{}) {
			t.Errorf("state %v reason %v arrival %+v", got.Delivery.State, got.Delivery.Reject, got.Arrival)
		}
		if len(outs) == 0 || len(outs[0].Feedback) != 1 || outs[0].Feedback[0].Kind != delivery.KindRejected || outs[0].Feedback[0].Reject != delivery.RejectBacklogFull {
			t.Errorf("outcomes %+v: the sender must hear of the rejection once, with its reason", outs)
		}
	})
	t.Run("the earliest report that proves arrival is kept", func(t *testing.T) {
		o := outboundTo(t, KindMessage, true)
		for _, at := range []time.Duration{300, 100, 200} {
			var err error
			o, _, _, err = o.ApplyReport(now, signedReport(t, reportFor("ctl-0001", 2, accepted(), at*time.Second, 0)))
			requireNoErr(t, err)
		}
		if got := o.Recipients[0].Arrival.ArrivedAt; !got.Equal(t0.Add(100 * time.Second)) {
			t.Errorf("arrival %v, want t0+100s", got)
		}
	})
	t.Run("a stale accepted report after a delivered one still adds arrival facts and keeps the state", func(t *testing.T) {
		o := outboundTo(t, KindMessage, true)
		o, _, _, err := o.ApplyReport(now, signedReport(t, reportFor("ctl-0001", 2, delivered(), 760*time.Second, 700*time.Second)))
		requireNoErr(t, err)
		o, eff, outs, err := o.ApplyReport(now, signedReport(t, reportFor("ctl-0001", 2, accepted(), 100*time.Second, 0)))
		requireNoErr(t, err)
		if eff != ReportApplied || o.Recipients[0].Delivery.State != delivery.StateDelivered || len(outs) == 0 || outs[0].Result != delivery.ResultIgnored {
			t.Errorf("effect %v state %v outcomes %+v", eff, o.Recipients[0].Delivery.State, outs)
		}
		if !o.Recipients[0].Arrival.ArrivedAt.Equal(t0.Add(100 * time.Second)) {
			t.Errorf("arrival %+v", o.Recipients[0].Arrival)
		}
	})
	t.Run("a blocked report gives the one blocked notice", func(t *testing.T) {
		_, _, outs, err := outboundTo(t, KindMessage, true).ApplyReport(now, signedReport(t,
			reportFor("ctl-0001", 2, delivery.StatusReport{State: delivery.StateAccepted, Blocked: true, BlockedCause: delivery.CauseAdapterOffline}, 100*time.Second, 0)))
		requireNoErr(t, err)
		var kinds []delivery.Kind
		for _, o := range outs {
			for _, f := range o.Feedback {
				kinds = append(kinds, f.Kind)
			}
		}
		if !reflect.DeepEqual(kinds, []delivery.Kind{delivery.KindDeliveryBlocked}) {
			t.Errorf("feedback %v", kinds)
		}
	})
	t.Run("a report for an endpoint with no delivery changes nothing", func(t *testing.T) {
		o := outboundTo(t, KindMessage, true)
		n, eff, outs, err := o.ApplyReport(now, signedReport(t, reportFor("ctl-0001", 9, accepted(), 100*time.Second, 0)))
		requireNoErr(t, err)
		if eff != ReportNotTracked || outs != nil || !reflect.DeepEqual(n, o) {
			t.Errorf("effect %v outcomes %v", eff, outs)
		}
	})
	t.Run("a report about another message is refused", func(t *testing.T) {
		_, _, _, err := outboundTo(t, KindMessage, true).ApplyReport(now, signedReport(t, reportFor("other", 2, accepted(), 100*time.Second, 0)))
		requireErr(t, err, ErrMismatch)
	})
	t.Run("a report signed by another node than the endpoint's is refused", func(t *testing.T) {
		b := nodeOf(t, keyIdentityB)
		d := StatusReportDescriptor(b, acceptedFor(t, keyIdentityB, keyNetworkB), t0)
		rec, err := record.Seal(d, withSender(t, reportFor("ctl-0001", 2, delivered(), 760*time.Second, 700*time.Second)), signerOf(t, keyNetworkB))
		requireNoErr(t, err)
		sr, err := VerifyStatusReport(rec, b, localNode(t), acceptedFor(t, keyIdentityB, keyNetworkB), t0.Add(time.Hour))
		requireNoErr(t, err)
		_, _, _, err = outboundTo(t, KindMessage, true).ApplyReport(now, sr)
		requireErr(t, err, ErrMismatch)
	})
	t.Run("an unverified report is refused", func(t *testing.T) {
		_, _, _, err := outboundTo(t, KindMessage, true).ApplyReport(now, SignedReport{})
		requireErr(t, err, ErrNotVerified)
	})
	t.Run("a report dated beyond the clock tolerance is refused, one within it is believed", func(t *testing.T) {
		now := t0.Add(time.Hour)
		o := outboundTo(t, KindMessage, true)
		_, _, _, err := o.ApplyReport(now, signedReport(t, reportFor("ctl-0001", 2, accepted(), time.Hour+2*time.Minute+time.Second, 0)))
		requireErr(t, err, ErrFromFuture)
		_, eff, _, err := o.ApplyReport(now, signedReport(t, reportFor("ctl-0001", 2, accepted(), time.Hour+2*time.Minute, 0)))
		requireNoErr(t, err)
		if eff != ReportApplied {
			t.Errorf("effect %v", eff)
		}
	})
	t.Run("a report that contradicts the record is an error and changes nothing", func(t *testing.T) {
		o := outboundTo(t, KindMessage, true)
		o, _, _, err := o.ApplyReport(now, signedReport(t, reportFor("ctl-0001", 2, delivered(), 760*time.Second, 700*time.Second)))
		requireNoErr(t, err)
		n, eff, _, err := o.ApplyReport(now, signedReport(t, reportFor("ctl-0001", 2, delivery.StatusReport{State: delivery.StateRejected, Reject: delivery.RejectExpired}, 800*time.Second, 0)))
		requireErr(t, err, ErrPeerContradiction, delivery.ErrIllegalTransition)
		if eff != ReportNone || !reflect.DeepEqual(n, o) {
			t.Errorf("effect %v, outbound changed", eff)
		}
	})
	t.Run("the input is not changed", func(t *testing.T) {
		o := outboundTo(t, KindMessage, true)
		_, _, _, err := o.ApplyReport(now, signedReport(t, reportFor("ctl-0001", 2, delivered(), 760*time.Second, 700*time.Second)))
		requireNoErr(t, err)
		if o.Recipients[0].Delivery.State != delivery.StateQueued || o.Recipients[0].Arrival != (Arrival{}) {
			t.Errorf("input changed: %+v", o.Recipients[0])
		}
	})
	t.Run("a report applies to the derived delivery", func(t *testing.T) {
		o := createOutbound(t)
		o.Derived = &Tracked{Address: addr(nodeOf(t, keyIdentityA), 3), Delivery: delivery.NewSourceRecord(acceptBefore)}
		o.ControlReplyDigest = record.Digest{1}
		n, eff, _, err := o.ApplyReport(now, signedReport(t, reportFor("ctl-0001", 3, delivered(), 760*time.Second, 700*time.Second)))
		requireNoErr(t, err)
		if eff != ReportApplied || n.Derived.Delivery.State != delivery.StateDelivered || n.Recipients[0].Delivery.State != delivery.StateQueued {
			t.Errorf("effect %v derived %v control %v", eff, n.Derived.Delivery.State, n.Recipients[0].Delivery.State)
		}
		if o.Derived.Delivery.State != delivery.StateQueued {
			t.Error("the input's derived delivery was changed")
		}
	})
	t.Run("an invalid outbound record is refused", func(t *testing.T) {
		o := outboundTo(t, KindMessage, true)
		o.Message = ""
		_, _, _, err := o.ApplyReport(now, signedReport(t, reportFor("ctl-0001", 2, accepted(), 100*time.Second, 0)))
		requireErr(t, err, ErrOutbound)
	})
}

func TestArrivalLearn(t *testing.T) {
	r := func(state delivery.State, reported, deliveredAt int) StatusReport {
		rep := StatusReport{Status: delivery.StatusReport{State: state}, ReportedAt: t0.Add(time.Duration(reported) * time.Second)}
		if deliveredAt != 0 {
			rep.DeliveredAt = t0.Add(time.Duration(deliveredAt) * time.Second)
		}
		return rep
	}
	a := Arrival{}.Learn(r(delivery.StateAccepted, 50, 0))
	a = a.Learn(r(delivery.StateDelivered, 90, 80))
	a = a.Learn(r(delivery.StateDelivered, 95, 85))
	a = a.Learn(r(delivery.StateRejected, 10, 0))
	if !a.ArrivedAt.Equal(t0.Add(50*time.Second)) || !a.DeliveredAt.Equal(t0.Add(80*time.Second)) {
		t.Errorf("%+v", a)
	}
}

func TestHandleControlReply(t *testing.T) {
	now := t0.Add(20 * time.Minute)
	nodeA := nodeOf(t, keyIdentityA)

	t.Run("a created session ends the signal and binds the initial message to the new endpoint", func(t *testing.T) {
		o := createOutbound(t)
		h, err := HandleControlReply(now, o, signedReply(t, createdReply("ctl-0001", 2, 3, 650*time.Second)), nil)
		requireNoErr(t, err)
		if h.Outbound.Recipients[0].Delivery.State != delivery.StateDelivered {
			t.Errorf("control delivery %v", h.Outbound.Recipients[0].Delivery.State)
		}
		d := h.Outbound.Derived
		if d == nil || !h.DerivedCreated || d.Address != addr(nodeA, 3) || d.Delivery.State != delivery.StateAccepted || d.Delivery.Side != delivery.SideSource {
			t.Fatalf("derived %+v created %v", d, h.DerivedCreated)
		}
		if !d.Delivery.AcceptBefore.Equal(acceptBefore) || !d.Delivery.ReportBy.Equal(delivery.ReportBy(acceptBefore)) {
			t.Errorf("the derived delivery has deadlines %v and %v: it shares the signal's", d.Delivery.AcceptBefore, d.Delivery.ReportBy)
		}
		if h.Report != ReportNone || h.Reported != nil {
			t.Errorf("report effect %v", h.Report)
		}
		if err := h.Outbound.Validate(); err != nil {
			t.Error(err)
		}
	})
	t.Run("an unconfirmed session binds as well", func(t *testing.T) {
		r := createdReply("ctl-0001", 2, 3, 650*time.Second)
		r.Result.Create.Outcome, r.Result.Create.Reason = adapterproto.CreateReplyUnconfirmed, adapterproto.CreateReasonReadyTimeout
		h, err := HandleControlReply(now, createOutbound(t), signedReply(t, r), nil)
		requireNoErr(t, err)
		if h.Outbound.Derived == nil {
			t.Error("no derived delivery")
		}
	})
	t.Run("a failed creation ends the signal and binds nothing", func(t *testing.T) {
		h, err := HandleControlReply(now, createOutbound(t), signedReply(t, failedReply("ctl-0001", 2, 30*time.Second)), nil)
		requireNoErr(t, err)
		if h.Outbound.Derived != nil || h.DerivedCreated || h.Outbound.Recipients[0].Delivery.State != delivery.StateDelivered {
			t.Errorf("derived %v state %v", h.Outbound.Derived, h.Outbound.Recipients[0].Delivery.State)
		}
	})
	t.Run("without an initial message nothing is bound", func(t *testing.T) {
		o := outboundTo(t, KindSessionCreate, false)
		h, err := HandleControlReply(now, o, signedReply(t, createdReply("ctl-0001", 2, 3, 650*time.Second)), nil)
		requireNoErr(t, err)
		if h.Outbound.Derived != nil || h.Outbound.Recipients[0].Delivery.State != delivery.StateDelivered {
			t.Errorf("derived %v", h.Outbound.Derived)
		}
	})
	t.Run("the reply to another control signal ends it and binds nothing", func(t *testing.T) {
		cases := map[Kind]ControlResult{
			KindSessionClose:     {Signal: adapterproto.SignalSessionClose, Close: &adapterproto.ControlCloseReply{Outcome: adapterproto.CloseReplyClosed}},
			KindSessionInterrupt: {Signal: adapterproto.SignalSessionInterrupt, Interrupt: &adapterproto.ControlInterruptReply{Outcome: adapterproto.InterruptReplyStopped}},
			KindEndpointStatus: {Signal: adapterproto.SignalEndpointStatus, Status: &adapterproto.ControlStatusReply{
				Activity: adapterproto.ActivityIdle, AsOf: t0.Unix() + 60}},
		}
		for kind, res := range cases {
			o := outboundTo(t, kind, false)
			h, err := HandleControlReply(now, o, signedReply(t, ControlReply{Message: "ctl-0001", Recipient: ep(2), IssuedAt: t0.Add(time.Minute), Result: res}), nil)
			requireNoErr(t, err)
			if h.Outbound.Derived != nil || h.Outbound.Recipients[0].Delivery.State != delivery.StateDelivered {
				t.Errorf("%v: derived %v state %v", kind, h.Outbound.Derived, h.Outbound.Recipients[0].Delivery.State)
			}
		}
	})
	t.Run("the accompanying report is applied to the derived delivery just created", func(t *testing.T) {
		rep := signedReport(t, reportFor("ctl-0001", 3, delivered(), 760*time.Second, 700*time.Second))
		h, err := HandleControlReply(now, createOutbound(t), signedReply(t, createdReply("ctl-0001", 2, 3, 650*time.Second)), &rep)
		requireNoErr(t, err)
		d := h.Outbound.Derived
		if h.Report != ReportApplied || d.Delivery.State != delivery.StateDelivered || !d.Arrival.DeliveredAt.Equal(t0.Add(700*time.Second)) {
			t.Errorf("report %v derived %+v", h.Report, d)
		}
	})
	t.Run("a report with no delivery to apply to is not applied", func(t *testing.T) {
		rep := signedReport(t, reportFor("ctl-0001", 3, delivered(), 760*time.Second, 700*time.Second))
		h, err := HandleControlReply(now, createOutbound(t), signedReply(t, failedReply("ctl-0001", 2, 30*time.Second)), &rep)
		requireNoErr(t, err)
		if h.Report != ReportNotTracked || h.Outbound.Derived != nil {
			t.Errorf("report %v derived %v", h.Report, h.Outbound.Derived)
		}
	})
	t.Run("handling the same reply twice changes nothing the second time", func(t *testing.T) {
		rep := signedReport(t, reportFor("ctl-0001", 3, delivered(), 760*time.Second, 700*time.Second))
		reply := signedReply(t, createdReply("ctl-0001", 2, 3, 650*time.Second))
		first, err := HandleControlReply(now, createOutbound(t), reply, &rep)
		requireNoErr(t, err)
		second, err := HandleControlReply(now, first.Outbound, reply, &rep)
		requireNoErr(t, err)
		if !reflect.DeepEqual(first.Outbound, second.Outbound) || second.DerivedCreated || second.Control.Result != delivery.ResultIgnored {
			t.Errorf("second handling changed the record or created again: %+v", second)
		}
	})
	t.Run("the pull path and the evidence path reach the same state in either order", func(t *testing.T) {
		reply := signedReply(t, createdReply("ctl-0001", 2, 3, 650*time.Second))
		rep := signedReport(t, reportFor("ctl-0001", 3, accepted(), 700*time.Second, 0))
		// evidence path first: the reply and the report together
		a, err := HandleControlReply(now, createOutbound(t), reply, &rep)
		requireNoErr(t, err)
		// pull path: the reply alone, then the report on its own
		b, err := HandleControlReply(now, createOutbound(t), reply, nil)
		requireNoErr(t, err)
		bo, _, _, err := b.Outbound.ApplyReport(now, rep)
		requireNoErr(t, err)
		if !reflect.DeepEqual(a.Outbound, bo) {
			t.Errorf("paths differ:\n%+v\n%+v", a.Outbound, bo)
		}
		// pull path of the report before the reply: not tracked yet, and then
		// the reply and the report again
		c, eff, _, err := createOutbound(t).ApplyReport(now, rep)
		requireNoErr(t, err)
		if eff != ReportNotTracked {
			t.Fatalf("effect %v", eff)
		}
		cc, err := HandleControlReply(now, c, reply, &rep)
		requireNoErr(t, err)
		if !reflect.DeepEqual(a.Outbound, cc.Outbound) {
			t.Errorf("late reply gives another state:\n%+v\n%+v", a.Outbound, cc.Outbound)
		}
	})
	t.Run("a reply after the delivery was judged expired corrects it and still binds", func(t *testing.T) {
		o := createOutbound(t)
		late := delivery.ExpiryJudgeableAfter(acceptBefore).Add(time.Second)
		rec, _, err := o.Recipients[0].Delivery.Apply(late, delivery.Event{Type: delivery.EventExpiryQueried, QueriedAt: late})
		requireNoErr(t, err)
		if rec.State != delivery.StateExpiredUnconfirmed {
			t.Fatalf("setup: state %v", rec.State)
		}
		o.Recipients[0].Delivery = rec
		h, err := HandleControlReply(late.Add(time.Minute), o, signedReply(t, createdReply("ctl-0001", 2, 3, 650*time.Second)), nil)
		requireNoErr(t, err)
		if len(h.Control.Feedback) != 1 || h.Control.Feedback[0].Kind != delivery.KindExpiredCorrected {
			t.Errorf("control feedback %+v, want one expired_corrected", h.Control.Feedback)
		}
		if h.Outbound.Derived == nil || h.Outbound.Recipients[0].Delivery.State != delivery.StateDelivered {
			t.Errorf("derived %v state %v", h.Outbound.Derived, h.Outbound.Recipients[0].Delivery.State)
		}
	})
	t.Run("a reply after report_by leaves the closed delivery alone and still binds", func(t *testing.T) {
		o := createOutbound(t)
		after := delivery.ReportBy(acceptBefore).Add(time.Minute)
		rec, _, err := o.Recipients[0].Delivery.Tick(after)
		requireNoErr(t, err)
		if rec.State != delivery.StateClosedUnconfirmed {
			t.Fatalf("setup: state %v", rec.State)
		}
		o.Recipients[0].Delivery = rec
		h, err := HandleControlReply(after, o, signedReply(t, createdReply("ctl-0001", 2, 3, 650*time.Second)), nil)
		requireNoErr(t, err)
		if h.Control.Result != delivery.ResultIgnored || h.Control.Ignored != delivery.IgnoreAlreadyFinal || h.Outbound.Derived == nil {
			t.Errorf("control %+v derived %v", h.Control, h.Outbound.Derived)
		}
		// The derived delivery shares the deadlines that have passed, so it closes
		// at its next tick without any notice: the sender is told nothing about a
		// delivery whose report window ended before the reply arrived.
		closed, outs, err := h.Outbound.Derived.Delivery.Tick(after)
		requireNoErr(t, err)
		if closed.State != delivery.StateClosedUnconfirmed {
			t.Errorf("derived state after the tick: %v", closed.State)
		}
		for _, o := range outs {
			if len(o.Feedback) != 0 {
				t.Errorf("the late close gave feedback %+v", o.Feedback)
			}
		}
	})
	t.Run("a reply for a signal the target rejected is an error", func(t *testing.T) {
		o := createOutbound(t)
		rec, _, err := o.Recipients[0].Delivery.Apply(now, delivery.Event{Type: delivery.EventRejected, Reject: delivery.RejectUnauthorized})
		requireNoErr(t, err)
		o.Recipients[0].Delivery = rec
		_, err = HandleControlReply(now, o, signedReply(t, createdReply("ctl-0001", 2, 3, 650*time.Second)), nil)
		requireErr(t, err, ErrPeerContradiction, delivery.ErrIllegalTransition)
	})
	t.Run("the digest of the accepted reply is kept", func(t *testing.T) {
		reply := signedReply(t, createdReply("ctl-0001", 2, 3, 650*time.Second))
		h, err := HandleControlReply(now, createOutbound(t), reply, nil)
		requireNoErr(t, err)
		if h.Outbound.ControlReplyDigest != reply.Digest() || reply.Digest().IsZero() {
			t.Errorf("digest %v, want %v", h.Outbound.ControlReplyDigest, reply.Digest())
		}
	})
	t.Run("the same reply under a fresh signature is the same reply", func(t *testing.T) {
		// The target keeps the payload and signs it again with its current network
		// key each time it sends it: the digest excludes the signature.
		first, err := HandleControlReply(now, createOutbound(t), signedReply(t, createdReply("ctl-0001", 2, 3, 650*time.Second)), nil)
		requireNoErr(t, err)
		rotated := acceptedFor(t, keyIdentityA, keyThird)
		payload := createdReply("ctl-0001", 2, 3, 650*time.Second)
		payload.Sender = localNode(t)
		rec, err := record.Seal(ControlReplyDescriptor(nodeOf(t, keyIdentityA), rotated, t0), payload, signerOf(t, keyThird))
		requireNoErr(t, err)
		again, err := VerifyControlReply(rec, nodeOf(t, keyIdentityA), localNode(t), rotated, t0.Add(time.Hour))
		requireNoErr(t, err)
		if again.Digest() != first.Outbound.ControlReplyDigest {
			t.Fatalf("a new signature changed the digest")
		}
		second, err := HandleControlReply(now, first.Outbound, again, nil)
		requireNoErr(t, err)
		if !reflect.DeepEqual(first.Outbound, second.Outbound) || second.DerivedCreated {
			t.Error("the same reply again changed the record")
		}
	})
	t.Run("another reply after the first is a contradiction by the peer", func(t *testing.T) {
		first, err := HandleControlReply(now, createOutbound(t), signedReply(t, createdReply("ctl-0001", 2, 3, 650*time.Second)), nil)
		requireNoErr(t, err)
		for name, other := range map[string]ControlReply{
			"another session endpoint":      createdReply("ctl-0001", 2, 4, 650*time.Second),
			"a failure after a creation":    failedReply("ctl-0001", 2, 650*time.Second),
			"the same result, issued later": createdReply("ctl-0001", 2, 3, 651*time.Second),
		} {
			_, err = HandleControlReply(now, first.Outbound, signedReply(t, other), nil)
			if !errors.Is(err, ErrPeerContradiction) {
				t.Errorf("%s: error %v, want ErrPeerContradiction", name, err)
			}
		}
	})
	t.Run("a creation after a recorded failure builds no delivery of the initial message", func(t *testing.T) {
		failed, err := HandleControlReply(now, createOutbound(t), signedReply(t, failedReply("ctl-0001", 2, 30*time.Second)), nil)
		requireNoErr(t, err)
		h, err := HandleControlReply(now, failed.Outbound, signedReply(t, createdReply("ctl-0001", 2, 3, 650*time.Second)), nil)
		requireErr(t, err, ErrPeerContradiction)
		if !reflect.DeepEqual(h, Handling{}) || failed.Outbound.Derived != nil {
			t.Error("a contradicting reply returned or changed something")
		}
	})
	t.Run("a reply that names the signal's own recipient as the session is a contradiction by the peer, judged before the record is built", func(t *testing.T) {
		for name, o := range map[string]Outbound{
			"with an initial message":    createOutbound(t),
			"without an initial message": outboundTo(t, KindSessionCreate, false),
		} {
			o := o
			h, err := HandleControlReply(now, o, signedReply(t, createdReply("ctl-0001", 2, 2, 650*time.Second)), nil)
			requireErr(t, err, ErrPeerContradiction)
			if errors.Is(err, ErrOutbound) {
				t.Errorf("%s: the collision reached Validate: %v", name, err)
			}
			if !reflect.DeepEqual(h, Handling{}) || o.Recipients[0].Delivery.State != delivery.StateQueued {
				t.Errorf("%s: a refused reply returned or changed something", name)
			}
		}
	})
	mismatches := []struct {
		rule  string
		o     func() Outbound
		reply func() ControlReply
	}{
		{"another message", createOutbound2(t), func() ControlReply { return createdReply("other", 2, 3, 650*time.Second) }},
		{"another recipient endpoint", createOutbound2(t), func() ControlReply { return createdReply("ctl-0001", 5, 3, 650*time.Second) }},
		{"another signal", createOutbound2(t), func() ControlReply {
			return ControlReply{Message: "ctl-0001", Recipient: ep(2), IssuedAt: t0.Add(time.Minute), Result: ControlResult{
				Signal: adapterproto.SignalSessionClose, Close: &adapterproto.ControlCloseReply{Outcome: adapterproto.CloseReplyClosed}}}
		}},
		{"a message that is not a control signal", func() Outbound { return outboundTo(t, KindMessage, true) }, func() ControlReply { return createdReply("ctl-0001", 2, 3, 650*time.Second) }},
	}
	for _, c := range mismatches {
		t.Run("a reply for "+c.rule+" is refused", func(t *testing.T) {
			_, err := HandleControlReply(now, c.o(), signedReply(t, c.reply()), nil)
			requireErr(t, err, ErrMismatch)
		})
	}
	t.Run("a reply signed by another node is refused", func(t *testing.T) {
		b := nodeOf(t, keyIdentityB)
		reply := createdReply("ctl-0001", 2, 3, 650*time.Second)
		reply.Sender = localNode(t)
		rec, err := record.Seal(ControlReplyDescriptor(b, acceptedFor(t, keyIdentityB, keyNetworkB), t0), reply, signerOf(t, keyNetworkB))
		requireNoErr(t, err)
		sc, err := VerifyControlReply(rec, b, localNode(t), acceptedFor(t, keyIdentityB, keyNetworkB), t0.Add(time.Hour))
		requireNoErr(t, err)
		_, err = HandleControlReply(now, createOutbound(t), sc, nil)
		requireErr(t, err, ErrMismatch)
	})
	t.Run("a report about another message, from another node, or unverified is refused", func(t *testing.T) {
		reply := signedReply(t, createdReply("ctl-0001", 2, 3, 650*time.Second))
		other := signedReport(t, reportFor("other", 3, accepted(), 700*time.Second, 0))
		_, err := HandleControlReply(now, createOutbound(t), reply, &other)
		requireErr(t, err, ErrMismatch)
		b := nodeOf(t, keyIdentityB)
		rec, err := record.Seal(StatusReportDescriptor(b, acceptedFor(t, keyIdentityB, keyNetworkB), t0), withSender(t, reportFor("ctl-0001", 3, accepted(), 700*time.Second, 0)), signerOf(t, keyNetworkB))
		requireNoErr(t, err)
		fromB, err := VerifyStatusReport(rec, b, localNode(t), acceptedFor(t, keyIdentityB, keyNetworkB), t0.Add(time.Hour))
		requireNoErr(t, err)
		_, err = HandleControlReply(now, createOutbound(t), reply, &fromB)
		requireErr(t, err, ErrMismatch)
		zero := SignedReport{}
		_, err = HandleControlReply(now, createOutbound(t), reply, &zero)
		requireErr(t, err, ErrNotVerified)
		_, err = HandleControlReply(now, createOutbound(t), SignedControlReply{}, nil)
		requireErr(t, err, ErrNotVerified)
	})
	t.Run("a reply dated beyond the clock tolerance is refused", func(t *testing.T) {
		_, err := HandleControlReply(now, createOutbound(t), signedReply(t, createdReply("ctl-0001", 2, 3, 20*time.Minute+2*time.Minute+time.Second)), nil)
		requireErr(t, err, ErrFromFuture)
	})
	t.Run("a failure leaves the input and the result empty", func(t *testing.T) {
		o := createOutbound(t)
		h, err := HandleControlReply(now, o, signedReply(t, createdReply("other", 2, 3, 650*time.Second)), nil)
		requireErr(t, err, ErrMismatch)
		if !reflect.DeepEqual(h, Handling{}) || o.Recipients[0].Delivery.State != delivery.StateQueued {
			t.Error("a failed handling returned or changed something")
		}
	})
	t.Run("the target's own view: the delivered report of the committed reply does not end the signal, the reply does", func(t *testing.T) {
		target := delivery.NewTargetControlRecord(acceptBefore, delivery.ReportBy(acceptBefore))
		target, _, err := target.Apply(now, delivery.Event{Type: delivery.EventReplyCommitted})
		requireNoErr(t, err)
		rep := reportFor("ctl-0001", 2, target.StatusReport(), 700*time.Second, 690*time.Second)
		o := createOutbound(t)
		n, eff, _, err := o.ApplyReport(now, signedReport(t, rep))
		requireNoErr(t, err)
		if eff != ReportAwaitsControlReply || !reflect.DeepEqual(n, o) {
			t.Errorf("effect %v, or the record changed: the control signal's delivery is %v", eff, n.Recipients[0].Delivery.State)
		}
	})
}

// A control signal's own delivery ends only through its control reply: the
// reply carries the result and the session endpoint, and a sender that stopped
// pulling at the delivered report would never get either.
func TestAControlSignalsDeliveryEndsOnlyThroughItsControlReply(t *testing.T) {
	now := t0.Add(20 * time.Minute)
	deliveredFromControl := func() SignedReport {
		return signedReport(t, reportFor("ctl-0001", 2, delivered(), 700*time.Second, 690*time.Second))
	}
	reply := func() SignedControlReply { return signedReply(t, createdReply("ctl-0001", 2, 3, 650*time.Second)) }

	t.Run("a delivered report alone leaves the signal pending, and the reply then ends it and builds the initial message's delivery", func(t *testing.T) {
		o := createOutbound(t)
		n, eff, outs, err := o.ApplyReport(now, deliveredFromControl())
		requireNoErr(t, err)
		if eff != ReportAwaitsControlReply || outs != nil || !reflect.DeepEqual(n, o) {
			t.Fatalf("effect %v outcomes %v: the report must change nothing", eff, outs)
		}
		if n.Recipients[0].Delivery.State.IsTerminal() {
			t.Fatalf("the signal's delivery is final (%v): the sender would stop pulling and never get the control reply", n.Recipients[0].Delivery.State)
		}
		h, err := HandleControlReply(now, n, reply(), nil)
		requireNoErr(t, err)
		d := h.Outbound.Derived
		if h.Outbound.Recipients[0].Delivery.State != delivery.StateDelivered || d == nil || !h.DerivedCreated || d.Address.Endpoint != ep(3) {
			t.Errorf("after the reply: signal %v, derived %+v", h.Outbound.Recipients[0].Delivery.State, d)
		}
	})
	t.Run("the other states of the signal's own report are applied as before", func(t *testing.T) {
		for name, st := range map[string]delivery.StatusReport{
			"accepted":          accepted(),
			"accepted, blocked": {State: delivery.StateAccepted, Blocked: true, BlockedCause: delivery.CauseAdapterOffline},
			"delivery_unknown":  {State: delivery.StateDeliveryUnknown, Unknown: delivery.UnknownHandedOver},
			"rejected":          {State: delivery.StateRejected, Reject: delivery.RejectUnauthorized},
		} {
			n, eff, _, err := createOutbound(t).ApplyReport(now, signedReport(t, reportFor("ctl-0001", 2, st, 100*time.Second, 0)))
			requireNoErr(t, err)
			if eff != ReportApplied || n.Recipients[0].Delivery.State != st.State {
				t.Errorf("%s: effect %v state %v", name, eff, n.Recipients[0].Delivery.State)
			}
		}
	})
	t.Run("every control signal waits for its reply", func(t *testing.T) {
		for _, kind := range []Kind{KindSessionCreate, KindSessionClose, KindSessionInterrupt, KindEndpointStatus} {
			o := outboundTo(t, kind, false)
			n, eff, _, err := o.ApplyReport(now, deliveredFromControl())
			requireNoErr(t, err)
			if eff != ReportAwaitsControlReply || !reflect.DeepEqual(n, o) {
				t.Errorf("%v: effect %v", kind, eff)
			}
		}
	})
	t.Run("once a reply was accepted, a delivered report is an ordinary one, whatever the reply's outcome", func(t *testing.T) {
		for name, r := range map[string]ControlReply{
			"created": createdReply("ctl-0001", 2, 3, 650*time.Second),
			"failed":  failedReply("ctl-0001", 2, 30*time.Second),
		} {
			h, err := HandleControlReply(now, createOutbound(t), signedReply(t, r), nil)
			requireNoErr(t, err)
			_, eff, _, err := h.Outbound.ApplyReport(now, deliveredFromControl())
			requireNoErr(t, err)
			if eff != ReportApplied {
				t.Errorf("%s: effect %v", name, eff)
			}
		}
	})
	t.Run("a message's delivered report ends the delivery as before", func(t *testing.T) {
		n, eff, _, err := outboundTo(t, KindMessage, true).ApplyReport(now, deliveredFromControl())
		requireNoErr(t, err)
		if eff != ReportApplied || n.Recipients[0].Delivery.State != delivery.StateDelivered {
			t.Errorf("effect %v state %v", eff, n.Recipients[0].Delivery.State)
		}
	})
	t.Run("the initial message's delivered report is applied once the reply bound the session", func(t *testing.T) {
		h, err := HandleControlReply(now, createOutbound(t), reply(), nil)
		requireNoErr(t, err)
		n, eff, _, err := h.Outbound.ApplyReport(now, signedReport(t, reportFor("ctl-0001", 3, delivered(), 760*time.Second, 700*time.Second)))
		requireNoErr(t, err)
		if eff != ReportApplied || n.Derived.Delivery.State != delivery.StateDelivered {
			t.Errorf("effect %v derived %v", eff, n.Derived.Delivery.State)
		}
	})
}

// A target that slept past report_by settles the delivery when it wakes, so
// its honest report is dated after report_by. Such a report is not a fault of
// the peer, but it must not move the arrival facts or the reply window: it is
// ignored.
func TestAReportDatedAfterTheDeliverysReportByIsIgnored(t *testing.T) {
	now := t0.Add(5 * time.Hour)
	// accept_before is 10 minutes after t0; report_by follows 2 h (queue cap),
	// four 15 min leases and the 2 min clock tolerance later; the sender then
	// allows the clock tolerance again.
	lastBelieved := 10*time.Minute + 2*time.Hour + 4*15*time.Minute + 2*time.Minute + 2*time.Minute
	for name, o := range map[string]Outbound{
		"a message": outboundTo(t, KindMessage, true),
		"a control": outboundTo(t, KindSessionClose, false),
		"a derived": func() Outbound {
			o := createOutbound(t)
			o.Derived = &Tracked{Address: addr(nodeOf(t, keyIdentityA), 3), Delivery: delivery.NewSourceRecord(acceptBefore)}
			o.ControlReplyDigest = record.Digest{1}
			return o
		}(),
	} {
		to := byte(2)
		if o.Derived != nil {
			to = 3
		}
		t.Run(name+": the last second believed is applied", func(t *testing.T) {
			_, eff, _, err := o.ApplyReport(now, signedReport(t, reportFor("ctl-0001", to, accepted(), lastBelieved, 0)))
			requireNoErr(t, err)
			if eff != ReportApplied {
				t.Errorf("effect %v", eff)
			}
		})
		t.Run(name+": one second later is ignored and changes nothing", func(t *testing.T) {
			n, eff, outs, err := o.ApplyReport(now, signedReport(t, reportFor("ctl-0001", to, accepted(), lastBelieved+time.Second, 0)))
			requireNoErr(t, err)
			if eff != ReportTooLate || outs != nil || !reflect.DeepEqual(n, o) {
				t.Errorf("effect %v: the record changed", eff)
			}
		})
	}
	t.Run("a late delivered report of a control signal is too late, not awaiting the reply", func(t *testing.T) {
		o := outboundTo(t, KindSessionClose, false)
		n, eff, _, err := o.ApplyReport(now, signedReport(t, reportFor("ctl-0001", 2, delivered(), lastBelieved+time.Second, lastBelieved)))
		requireNoErr(t, err)
		if eff != ReportTooLate || !reflect.DeepEqual(n, o) {
			t.Errorf("effect %v", eff)
		}
	})
	t.Run("a late report attached to a control reply leaves the reply handled and the report ignored", func(t *testing.T) {
		o := createOutbound(t)
		late := signedReport(t, reportFor("ctl-0001", 2, delivered(), lastBelieved+time.Second, lastBelieved))
		h, err := HandleControlReply(now, o, signedReply(t, createdReply("ctl-0001", 2, 3, 650*time.Second)), &late)
		requireNoErr(t, err)
		if h.Report != ReportTooLate || h.Reported != nil || h.Outbound.Derived == nil || h.Outbound.ControlReplyDigest.IsZero() {
			t.Errorf("report effect %v, outbound %+v", h.Report, h.Outbound)
		}
	})
	t.Run("a report for an endpoint the sender holds nothing for is still only not tracked", func(t *testing.T) {
		_, eff, _, err := outboundTo(t, KindMessage, true).ApplyReport(now, signedReport(t, reportFor("ctl-0001", 9, accepted(), lastBelieved+time.Hour, 0)))
		requireNoErr(t, err)
		if eff != ReportNotTracked {
			t.Errorf("effect %v", eff)
		}
	})
}

func withSender(t testing.TB, r StatusReport) StatusReport {
	r.Sender = localNode(t)
	return r
}

func createOutbound2(t testing.TB) func() Outbound {
	return func() Outbound { return createOutbound(t) }
}
