package envelope

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func reportWire(t testing.TB) statusReportWire {
	t.Helper()
	rec := mustDecodeRecord(t, goldenReportDelivered)
	var w statusReportWire
	requireNoErr(t, record.UnmarshalStrict(rec.Payload, &w, len(rec.Payload)))
	return w
}

func checkReportWire(t testing.TB, w statusReportWire) error {
	t.Helper()
	rec := signRaw(t, StatusReportType, w, keyNetworkA)
	_, err := record.Verify(StatusReportDescriptor(nodeOf(t, keyIdentityA), acceptedFor(t, keyIdentityA, keyNetworkA), t0.Add(time.Hour)), rec)
	return err
}

func ptr[T any](v T) *T { return &v }

func TestStatusReportRules(t *testing.T) {
	cases := []struct {
		rule string
		mut  func(*statusReportWire)
	}{
		{"the message is an id", func(w *statusReportWire) { w.Message = "a b" }},
		{"the message is not empty", func(w *statusReportWire) { w.Message = "" }},
		{"the recipient is an endpoint", func(w *statusReportWire) { w.Recipient = []byte{1, 2} }},
		{"the recipient is not zero", func(w *statusReportWire) { w.Recipient = make([]byte, 16) }},
		{"the sender is a node", func(w *statusReportWire) { w.Sender = []byte{1, 2} }},
		{"the sender is not zero", func(w *statusReportWire) { w.Sender = make([]byte, 32) }},
		{"the sender is given", func(w *statusReportWire) { w.Sender = nil }},
		{"state zero is not a state", func(w *statusReportWire) { w.State = 0 }},
		{"state six is not a state", func(w *statusReportWire) { w.State = 6 }},
		{"a reject reason comes only with rejected", func(w *statusReportWire) { w.Reject = 1 }},
		{"rejected has a reason", func(w *statusReportWire) { w.State, w.DeliveredAt = 3, nil }},
		{"the reject reason is known", func(w *statusReportWire) { w.State, w.Reject, w.DeliveredAt = 3, 11, nil }},
		{"a block cause comes only with accepted", func(w *statusReportWire) { w.BlockedCause = 1 }},
		{"the block cause is known", func(w *statusReportWire) { w.State, w.BlockedCause, w.DeliveredAt = 1, 4, nil }},
		{"delivered has a delivery time", func(w *statusReportWire) { w.DeliveredAt = nil }},
		{"only delivered has a delivery time", func(w *statusReportWire) { w.State = 1 }},
		{"an explicit zero delivery time is refused", func(w *statusReportWire) { w.DeliveredAt = ptr(int64(0)) }},
		{"a negative delivery time is refused", func(w *statusReportWire) { w.DeliveredAt = ptr(int64(-1)) }},
		{"delivery is not after the report", func(w *statusReportWire) { w.DeliveredAt = ptr(w.ReportedAt + 1) }},
		{"the report time is required", func(w *statusReportWire) { w.ReportedAt = 0 }},
		{"the report time is within range", func(w *statusReportWire) { w.ReportedAt = record.MaxUnixSeconds + 1 }},
	}
	for _, c := range cases {
		t.Run(c.rule, func(t *testing.T) {
			w := reportWire(t)
			c.mut(&w)
			err := checkReportWire(t, w)
			requireErr(t, err, record.ErrPayload)
		})
	}
	t.Run("the delivery time may equal the report time", func(t *testing.T) {
		w := reportWire(t)
		w.DeliveredAt = ptr(w.ReportedAt)
		requireNoErr(t, checkReportWire(t, w))
	})
}

func TestStatusReportWireTablesAreIndependentOfGoOrder(t *testing.T) {
	states := map[delivery.State]uint64{
		delivery.StateAccepted: 1, delivery.StateDelivered: 2, delivery.StateRejected: 3,
		delivery.StateDeliveryUnknown: 4, delivery.StateClosedUnconfirmed: 5,
	}
	for s, want := range states {
		if got, ok := toWireCode(stateWire, s); !ok || got != want {
			t.Errorf("state %s: wire %d, want %d", s, got, want)
		}
	}
	for _, s := range []delivery.State{delivery.StateInvalid, delivery.StateQueued, delivery.StateExpiredUnconfirmed} {
		if _, ok := toWireCode(stateWire, s); ok {
			t.Errorf("state %s has a wire value; a target does not report it", s)
		}
	}
	reasons := map[delivery.RejectReason]uint64{
		delivery.RejectUnauthorized: 1, delivery.RejectEndpointMissing: 2, delivery.RejectEndpointClosed: 3,
		delivery.RejectEndpointDeclined: 4, delivery.RejectTooLarge: 5, delivery.RejectExpired: 6,
		delivery.RejectBacklogExpired: 7, delivery.RejectSecondCheckFailed: 8, delivery.RejectMalformed: 9,
		delivery.RejectDeadlineOutOfRange: 10,
	}
	for r := delivery.RejectNone + 1; r.String() != "invalid"; r++ {
		want, ok := reasons[r]
		if !ok {
			t.Errorf("reject reason %s has no pinned wire value: add it to the table of the format", r)
			continue
		}
		if got, ok := toWireCode(rejectWire, r); !ok || got != want {
			t.Errorf("reject reason %s: wire %d, want %d", r, got, want)
		}
	}
	causes := map[delivery.Cause]uint64{
		delivery.CauseAdapterOffline: 1, delivery.CauseRuntimeAuth: 2, delivery.CauseNativeUnavailable: 3,
	}
	for c, want := range causes {
		if got, ok := toWireCode(causeWire, c); !ok || got != want {
			t.Errorf("cause %s: wire %d, want %d", c, got, want)
		}
	}
	for _, c := range []delivery.Cause{delivery.CauseNone, delivery.CauseNetwork, delivery.CauseTrustSuspended} {
		if _, ok := toWireCode(causeWire, c); ok {
			t.Errorf("cause %s has a wire value; it is not a block cause", c)
		}
	}
}

// targetRecords returns a target-side delivery record in each reportable state.
func targetRecords(t testing.TB) map[string]delivery.Record {
	t.Helper()
	accept := t0.Add(10 * time.Minute)
	reportBy := delivery.ReportBy(accept)
	must := func(r delivery.Record, _ delivery.Outcome, err error) delivery.Record {
		t.Helper()
		requireNoErr(t, err)
		return r
	}
	msg, err := delivery.NewTargetRecord(t0, accept, reportBy, delivery.DefaultEndpointMaxQueue)
	requireNoErr(t, err)
	ctl := delivery.NewTargetControlRecord(accept, reportBy)
	closed, _, err := msg.Tick(reportBy.Add(time.Second))
	requireNoErr(t, err)
	return map[string]delivery.Record{
		"accepted":         msg,
		"accepted blocked": must(msg.Apply(t0, delivery.Event{Type: delivery.EventBlocked, Cause: delivery.CauseRuntimeAuth})),
		"delivered":        must(ctl.Apply(t0, delivery.Event{Type: delivery.EventReplyCommitted})),
		"rejected":         must(msg.Apply(t0, delivery.Event{Type: delivery.EventRejected, Reject: delivery.RejectBacklogExpired})),
		"delivery_unknown": must(ctl.Apply(t0, delivery.Event{Type: delivery.EventDeliveryUnknown})),
		"closed":           closed,
	}
}

func TestStatusReportCarriesTheDeliveryReport(t *testing.T) {
	d := StatusReportDescriptor(nodeOf(t, keyIdentityA), acceptedFor(t, keyIdentityA, keyNetworkA), t0.Add(time.Hour))
	for name, rec := range targetRecords(t) {
		t.Run(name, func(t *testing.T) {
			rep := StatusReport{
				Sender: localNode(t), Message: "m", Recipient: ep(1), Status: rec.StatusReport(),
				ReportedAt: t0.Add(30 * time.Minute),
			}
			if rec.State == delivery.StateDelivered {
				rep.DeliveredAt = t0.Add(20 * time.Minute)
			}
			signed, err := record.Seal(d, rep, signerOf(t, keyNetworkA))
			requireNoErr(t, err)
			v, err := record.Verify(d, signed)
			requireNoErr(t, err)
			if got := v.Payload().Status; got != rec.StatusReport() {
				t.Errorf("status = %+v, want %+v", got, rec.StatusReport())
			}
			// the sender can apply what it read
			src := delivery.NewSourceRecord(t0.Add(10 * time.Minute))
			if _, _, err := src.ApplyStatusReport(t0.Add(time.Hour), v.Payload().Status); err != nil {
				t.Errorf("sender cannot apply the report: %v", err)
			}
		})
	}
}

func TestStatusReportProvesArrival(t *testing.T) {
	want := map[delivery.State]bool{
		delivery.StateAccepted: true, delivery.StateDelivered: true, delivery.StateDeliveryUnknown: true,
		delivery.StateRejected: false, delivery.StateClosedUnconfirmed: false,
	}
	for s, w := range want {
		r := StatusReport{Status: delivery.StatusReport{State: s}}
		if r.ProvesArrival() != w {
			t.Errorf("state %s: ProvesArrival = %v, want %v", s, !w, w)
		}
	}
}

func TestStatusReportRefusesAnUnreportableState(t *testing.T) {
	d := StatusReportDescriptor(nodeOf(t, keyIdentityA), acceptedFor(t, keyIdentityA, keyNetworkA), t0)
	for _, s := range []delivery.State{delivery.StateInvalid, delivery.StateQueued, delivery.StateExpiredUnconfirmed} {
		_, err := record.Seal(d, StatusReport{Sender: localNode(t), Message: "m", Recipient: ep(1), Status: delivery.StatusReport{State: s}, ReportedAt: t0}, signerOf(t, keyNetworkA))
		requireErr(t, err, record.ErrPayload)
	}
	_, err := record.Seal(d, StatusReport{Sender: localNode(t), Message: "m", Recipient: ep(1), Status: delivery.StatusReport{State: delivery.StateRejected, Reject: delivery.RejectReason(77)}, ReportedAt: t0}, signerOf(t, keyNetworkA))
	requireErr(t, err, record.ErrPayload)
	_, err = record.Seal(d, StatusReport{Sender: localNode(t), Message: "m", Recipient: ep(1), Status: delivery.StatusReport{State: delivery.StateAccepted, Blocked: true, BlockedCause: delivery.CauseNetwork}, ReportedAt: t0}, signerOf(t, keyNetworkA))
	requireErr(t, err, record.ErrPayload)
}

// signerCases are the ways a report or reply can be signed by the wrong key or
// verified in the wrong context; both record types share the slot, and both are
// run through the same table.
func signerCases(t *testing.T) []struct {
	rule     string
	signer   int
	node     ident.NodeID
	accepted record.AcceptedNetworkKey
	now      time.Time
	binding  record.Binding
} {
	a, b := nodeOf(t, keyIdentityA), nodeOf(t, keyIdentityB)
	good := acceptedFor(t, keyIdentityA, keyNetworkA)
	revoked, unchecked := good, good
	revoked.Revocation = record.KeyRevocationRevoked
	unchecked.Revocation = record.KeyRevocationUnchecked
	return []struct {
		rule     string
		signer   int
		node     ident.NodeID
		accepted record.AcceptedNetworkKey
		now      time.Time
		binding  record.Binding
	}{
		{"the identity key of the node does not sign", keyIdentityA, a, good, t0, record.BindingKeyMismatch},
		{"the network key of another node does not sign", keyNetworkB, a, good, t0, record.BindingKeyMismatch},
		{"a third party's key does not sign", keyThird, a, good, t0, record.BindingKeyMismatch},
		{"the right key is refused for another node", keyNetworkA, b, good, t0, record.BindingNodeMismatch},
		{"no accepted certificate", keyNetworkA, a, record.AcceptedNetworkKey{}, t0, record.BindingNoCertificate},
		{"an expired certificate", keyNetworkA, a, good, t0.Add(31 * 24 * time.Hour), record.BindingExpired},
		{"a certificate not yet valid", keyNetworkA, a, good, t0.Add(-48 * time.Hour), record.BindingNotYetValid},
		{"a revoked certificate", keyNetworkA, a, revoked, t0, record.BindingRevoked},
		{"an unchecked revocation", keyNetworkA, a, unchecked, t0, record.BindingRevocationUnchecked},
	}
}

func TestReportsAndRepliesAreSignedByTheTargetNodesNetworkKey(t *testing.T) {
	rep := StatusReport{Sender: localNode(t), Message: "m", Recipient: ep(1), Status: delivery.StatusReport{State: delivery.StateAccepted}, ReportedAt: t0}
	reply := ControlReply{Sender: localNode(t), Message: "m", Recipient: ep(1), IssuedAt: t0, Result: ControlResult{
		Signal: adapterproto.SignalSessionClose, Close: &adapterproto.ControlCloseReply{Outcome: adapterproto.CloseReplyClosed}}}
	for _, c := range signerCases(t) {
		t.Run(c.rule, func(t *testing.T) {
			// signing is not refused by the library: the descriptor of the signing
			// side is built for the signer's own node, so build the record raw
			wire, err := statusReportToWire(rep)
			requireNoErr(t, err)
			recR := signRaw(t, StatusReportType, wire, c.signer)
			_, err = VerifyStatusReport(recR, c.node, localNode(t), c.accepted, c.now)
			requireErr(t, err, record.ErrSignerNotAllowed)
			var be *record.BindingError
			if !errors.As(err, &be) || be.Binding != c.binding {
				t.Errorf("report: binding = %v, want %v (%v)", be, c.binding, err)
			}
			cw, err := controlReplyToWire(reply)
			requireNoErr(t, err)
			recC := signRaw(t, ControlReplyType, cw, c.signer)
			_, err = VerifyControlReply(recC, c.node, localNode(t), c.accepted, c.now)
			requireErr(t, err, record.ErrSignerNotAllowed)
			if !errors.As(err, &be) || be.Binding != c.binding {
				t.Errorf("reply: binding = %v, want %v (%v)", be, c.binding, err)
			}
		})
	}
	t.Run("the right key verifies and the signed value remembers its node", func(t *testing.T) {
		a := nodeOf(t, keyIdentityA)
		sr, err := VerifyStatusReport(sealReport(t, rep, keyNetworkA), a, localNode(t), acceptedFor(t, keyIdentityA, keyNetworkA), t0)
		requireNoErr(t, err)
		if sr.Node() != a || sr.Report().Message != "m" || sr.Digest().IsZero() {
			t.Errorf("signed report %+v", sr)
		}
		sc, err := VerifyControlReply(sealReply(t, reply, keyNetworkA), a, localNode(t), acceptedFor(t, keyIdentityA, keyNetworkA), t0)
		requireNoErr(t, err)
		if sc.Node() != a || sc.Reply().Message != "m" || sc.Digest().IsZero() {
			t.Errorf("signed reply %+v", sc)
		}
	})
	t.Run("no node, no verification", func(t *testing.T) {
		_, err := VerifyStatusReport(sealReport(t, rep, keyNetworkA), ident.NodeID{}, localNode(t), acceptedFor(t, keyIdentityA, keyNetworkA), t0)
		requireErr(t, err, ErrReport)
		_, err = VerifyControlReply(sealReply(t, reply, keyNetworkA), ident.NodeID{}, localNode(t), acceptedFor(t, keyIdentityA, keyNetworkA), t0)
		requireErr(t, err, ErrControlReply)
	})
	t.Run("no local node, no verification", func(t *testing.T) {
		a := nodeOf(t, keyIdentityA)
		_, err := VerifyStatusReport(sealReport(t, rep, keyNetworkA), a, ident.NodeID{}, acceptedFor(t, keyIdentityA, keyNetworkA), t0)
		requireErr(t, err, ErrReport)
		_, err = VerifyControlReply(sealReply(t, reply, keyNetworkA), a, ident.NodeID{}, acceptedFor(t, keyIdentityA, keyNetworkA), t0)
		requireErr(t, err, ErrControlReply)
	})
	t.Run("a report or reply naming another sending node is refused", func(t *testing.T) {
		a := nodeOf(t, keyIdentityA)
		elsewhere := nodeOf(t, keyThird) // not the local node: the signal was sent by someone else
		other := rep
		other.Sender = elsewhere
		_, err := VerifyStatusReport(sealReport(t, other, keyNetworkA), a, localNode(t), acceptedFor(t, keyIdentityA, keyNetworkA), t0)
		requireErr(t, err, ErrMismatch)
		otherReply := reply
		otherReply.Sender = elsewhere
		_, err = VerifyControlReply(sealReply(t, otherReply, keyNetworkA), a, localNode(t), acceptedFor(t, keyIdentityA, keyNetworkA), t0)
		requireErr(t, err, ErrMismatch)
	})
	t.Run("a report is not a reply", func(t *testing.T) {
		a := nodeOf(t, keyIdentityA)
		_, err := VerifyControlReply(sealReport(t, rep, keyNetworkA), a, localNode(t), acceptedFor(t, keyIdentityA, keyNetworkA), t0)
		requireErr(t, err, record.ErrType)
		_, err = VerifyStatusReport(sealReply(t, reply, keyNetworkA), a, localNode(t), acceptedFor(t, keyIdentityA, keyNetworkA), t0)
		requireErr(t, err, record.ErrType)
	})
	t.Run("a second signature is refused", func(t *testing.T) {
		a := nodeOf(t, keyIdentityA)
		r := sealReport(t, rep, keyNetworkA)
		r2, err := record.Sign(StatusReportType, r.Payload, signerOf(t, keyNetworkA), signerOf(t, keyThird))
		requireNoErr(t, err)
		_, err = VerifyStatusReport(r2, a, localNode(t), acceptedFor(t, keyIdentityA, keyNetworkA), t0)
		requireErr(t, err, record.ErrSignerNotAllowed)
	})
}

func replyWire(t testing.TB, golden []byte) controlReplyWire {
	t.Helper()
	rec := mustDecodeRecord(t, golden)
	var w controlReplyWire
	requireNoErr(t, record.UnmarshalStrict(rec.Payload, &w, len(rec.Payload)))
	return w
}

func checkReplyWire(t testing.TB, w controlReplyWire) error {
	t.Helper()
	rec := signRaw(t, ControlReplyType, w, keyNetworkA)
	_, err := record.Verify(ControlReplyDescriptor(nodeOf(t, keyIdentityA), acceptedFor(t, keyIdentityA, keyNetworkA), t0.Add(time.Hour)), rec)
	return err
}

func TestControlReplyRules(t *testing.T) {
	created := func(t testing.TB) controlReplyWire { return replyWire(t, goldenReplyCreated) }
	failed := func(t testing.TB) controlReplyWire { return replyWire(t, goldenReplyFailed) }
	closed := func(t testing.TB) controlReplyWire { return replyWire(t, goldenReplyClose) }
	interrupted := func(t testing.TB) controlReplyWire { return replyWire(t, goldenReplyInterrupt) }
	status := func(t testing.TB) controlReplyWire { return replyWire(t, goldenReplyStatus) }
	cases := []struct {
		rule  string
		build func(testing.TB) controlReplyWire
		mut   func(*controlReplyWire)
	}{
		{"the message is an id", created, func(w *controlReplyWire) { w.Message = "a b" }},
		{"the recipient is an endpoint", created, func(w *controlReplyWire) { w.Recipient = []byte{1} }},
		{"the sender is a node", created, func(w *controlReplyWire) { w.Sender = []byte{1, 2} }},
		{"the sender is not zero", created, func(w *controlReplyWire) { w.Sender = make([]byte, 32) }},
		{"the sender is given", created, func(w *controlReplyWire) { w.Sender = nil }},
		{"the issue time is required", created, func(w *controlReplyWire) { w.IssuedAt = 0 }},
		{"exactly one result: none", created, func(w *controlReplyWire) { w.Create = nil }},
		{"exactly one result: two", created, func(w *controlReplyWire) { w.Close = &outcomeReasonWire{Outcome: 1} }},
		{"a created session has an endpoint", created, func(w *controlReplyWire) { w.Create.SessionEndpoint = nil }},
		{"a session endpoint is 16 bytes", created, func(w *controlReplyWire) { w.Create.SessionEndpoint = []byte{1, 2, 3} }},
		{"a created session has no reason", created, func(w *controlReplyWire) { w.Create.Reason = 7 }},
		{"an unconfirmed session has an endpoint", created, func(w *controlReplyWire) { w.Create.Outcome, w.Create.SessionEndpoint, w.Create.Reason = 2, nil, 7 }},
		{"an unconfirmed session has an unconfirmed reason", created, func(w *controlReplyWire) { w.Create.Outcome, w.Create.Reason = 2, 2 }},
		{"a failed creation has no endpoint", failed, func(w *controlReplyWire) { w.Create.SessionEndpoint = ep(3).Bytes() }},
		{"a failed creation has a failure reason", failed, func(w *controlReplyWire) { w.Create.Reason = 7 }},
		{"a failed creation has a reason", failed, func(w *controlReplyWire) { w.Create.Reason = 0 }},
		{"a create outcome is known", created, func(w *controlReplyWire) { w.Create.Outcome = 4 }},
		{"a create outcome fits a byte", created, func(w *controlReplyWire) { w.Create.Outcome = 257 }},
		{"a create reason is known", failed, func(w *controlReplyWire) { w.Create.Reason = 10 }},
		{"a close outcome is known", closed, func(w *controlReplyWire) { w.Close.Outcome = 4 }},
		{"a close reason comes with close_unconfirmed", closed, func(w *controlReplyWire) { w.Close.Outcome = 1 }},
		{"close_unconfirmed has a reason", closed, func(w *controlReplyWire) { w.Close.Reason = 0 }},
		{"a close reason is known", closed, func(w *controlReplyWire) { w.Close.Reason = 4 }},
		{"an interrupt outcome is known", interrupted, func(w *controlReplyWire) { w.Interrupt.Outcome = 6 }},
		{"an interrupt reason comes with failed", interrupted, func(w *controlReplyWire) { w.Interrupt.Outcome = 1 }},
		{"a failed interrupt has a reason", interrupted, func(w *controlReplyWire) { w.Interrupt.Reason = 0 }},
		{"an activity is known", status, func(w *controlReplyWire) { w.Status.Activity = 4 }},
		{"conditions have no repeats", status, func(w *controlReplyWire) { w.Status.Conditions = []uint64{1, 1} }},
		{"conditions ascend", status, func(w *controlReplyWire) { w.Status.Conditions = []uint64{4, 1} }},
		{"a condition is known", status, func(w *controlReplyWire) { w.Status.Conditions = []uint64{9} }},
		{"presets are sorted by name", status, func(w *controlReplyWire) {
			w.Status.Presets[0], w.Status.Presets[1] = w.Status.Presets[1], w.Status.Presets[0]
		}},
		{"a preset name is valid", status, func(w *controlReplyWire) { w.Status.Presets[0].Name = "Default" }},
		{"the as-of time is required", status, func(w *controlReplyWire) { w.Status.AsOf = 0 }},
		{"the queue length fits", status, func(w *controlReplyWire) { w.Status.Queued = 1 << 40 }},
	}
	for _, c := range cases {
		t.Run(c.rule, func(t *testing.T) {
			w := c.build(t)
			c.mut(&w)
			requireErr(t, checkReplyWire(t, w), record.ErrPayload)
		})
	}
	t.Run("an unconfirmed creation with an endpoint and a reason is valid", func(t *testing.T) {
		w := created(t)
		w.Create.Outcome, w.Create.Reason = 2, 8
		requireNoErr(t, checkReplyWire(t, w))
	})
	t.Run("a failed creation because control is unknown is valid", func(t *testing.T) {
		w := failed(t)
		w.Create.Reason = uint64(adapterproto.CreateReasonControlUnknown)
		requireNoErr(t, checkReplyWire(t, w))
	})
	t.Run("a status reply with nothing to report is valid", func(t *testing.T) {
		w := status(t)
		w.Status.Conditions, w.Status.Presets, w.Status.Queued = nil, nil, 0
		requireNoErr(t, checkReplyWire(t, w))
	})
}

func TestControlReplySealRefusesWhatReceiversRefuse(t *testing.T) {
	d := ControlReplyDescriptor(nodeOf(t, keyIdentityA), acceptedFor(t, keyIdentityA, keyNetworkA), t0)
	good := ControlReply{Sender: localNode(t), Message: "m", Recipient: ep(2), IssuedAt: t0, Result: ControlResult{
		Signal: adapterproto.SignalSessionCreate, Create: &CreateResult{Outcome: adapterproto.CreateReplyCreated, SessionEndpoint: ep(3)}}}
	cases := map[string]func(*ControlReply){
		"no result": func(r *ControlReply) { r.Result = ControlResult{} },
		"two results": func(r *ControlReply) {
			r.Result.Close = &adapterproto.ControlCloseReply{Outcome: adapterproto.CloseReplyClosed}
		},
		"created without endpoint": func(r *ControlReply) { r.Result.Create.SessionEndpoint = ident.EndpointID{} },
		"zero issue time":          func(r *ControlReply) { r.IssuedAt = time.Time{} },
		"long message id":          func(r *ControlReply) { r.Message = delivery.MessageID(strings.Repeat("a", 200)) },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			r := good
			c := *good.Result.Create
			r.Result.Create = &c
			mut(&r)
			if _, err := record.Seal(d, r, signerOf(t, keyNetworkA)); err == nil {
				t.Fatal("sealed")
			}
		})
	}
	if _, err := record.Seal(d, good, signerOf(t, keyNetworkA)); err != nil {
		t.Fatal(err)
	}
}

func TestControlReplyBinds(t *testing.T) {
	mk := func(o adapterproto.ControlCreateOutcome) ControlReply {
		return ControlReply{Result: ControlResult{Signal: adapterproto.SignalSessionCreate, Create: &CreateResult{Outcome: o, SessionEndpoint: ep(3)}}}
	}
	if e, ok := mk(adapterproto.CreateReplyCreated).Binds(); !ok || e != ep(3) {
		t.Error("created binds the endpoint")
	}
	if e, ok := mk(adapterproto.CreateReplyUnconfirmed).Binds(); !ok || e != ep(3) {
		t.Error("unconfirmed binds the endpoint")
	}
	if _, ok := mk(adapterproto.CreateReplyFailed).Binds(); ok {
		t.Error("failed binds nothing")
	}
	other := ControlReply{Result: ControlResult{Signal: adapterproto.SignalSessionClose, Close: &adapterproto.ControlCloseReply{Outcome: adapterproto.CloseReplyClosed}}}
	if _, ok := other.Binds(); ok {
		t.Error("a close reply binds nothing")
	}
}
