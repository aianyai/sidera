package reply

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// A tightened node: the policy allows nothing, so a message gets in only as a
// reply inside a permission.
func tightenedVerdict(t testing.TB, now time.Time, from ident.Address, replyPermission bool) authz.Verdict {
	t.Helper()
	p, err := authz.NewPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	return authz.Evaluate(p, authz.Request{
		Now: now, Trust: trust.AllowedDirect, Stage: authz.StageIntake,
		Sender: authz.Sender{Node: from.Node, Endpoint: from.Endpoint},
		Action: authz.ActionMessageSend, Target: authz.Target{Kind: authz.TargetEndpoint, Endpoint: senderEndpoint()},
		BodySize: 20, Reply: replyPermission,
	})
}

func TestAfterInboundTighteningAReplyAndASessionCreateReplyStillGetBack(t *testing.T) {
	deliveredAt := t0.Add(2 * time.Minute)
	now := t0.Add(3 * time.Hour)

	t.Run("a reply to a message", func(t *testing.T) {
		o := learn(t, messageOutbound(), deliveredReport(t, runtimeR, t0.Add(3*time.Minute), deliveredAt), t0.Add(4*time.Minute))
		req := request(&o, onA(2), now)
		adm := mustAdmit(t, req)
		if !adm.Decision.Granted() || adm.Decision.Reason != ReasonDeliveredWindow {
			t.Fatalf("decision %+v", adm.Decision)
		}
		if v := tightenedVerdict(t, now, req.Replier, adm.Decision.Granted()); v.Outcome != authz.OutcomeAllow || v.Reason != authz.ReasonReplyPermission {
			t.Errorf("with the permission: %+v", v)
		}
		if v := tightenedVerdict(t, now, req.Replier, false); v.Outcome != authz.OutcomeReject {
			t.Errorf("without it the tightened policy lets it in: %+v", v)
		}
	})
	t.Run("a reply from the session a session.create made", func(t *testing.T) {
		o := createOutbound(true)
		req := request(&o, onA(3), now, createdReply(t, t0.Add(time.Minute)), deliveredReport(t, sessionD, t0.Add(3*time.Minute), deliveredAt))
		adm := mustAdmit(t, req)
		if !adm.Decision.Granted() {
			t.Fatalf("decision %+v (%v)", adm.Decision, adm.EvidenceError)
		}
		if v := tightenedVerdict(t, now, req.Replier, adm.Decision.Granted()); v.Outcome != authz.OutcomeAllow || v.Reason != authz.ReasonReplyPermission {
			t.Errorf("with the permission: %+v", v)
		}
	})
}

func TestTheSourceBehindNATAcceptsAReplyCarryingEvidence(t *testing.T) {
	now := t0.Add(time.Hour)
	o := messageOutbound() // the delivery report was never pulled: the record is still queued
	if r := mustAdmit(t, request(&o, onA(2), now)); r.Decision.Reason != ReasonNoArrivalProof {
		t.Fatalf("without evidence: %+v", r.Decision)
	}
	t.Run("an accepted report", func(t *testing.T) {
		adm := mustAdmit(t, request(&o, onA(2), now, acceptedReport(t, runtimeR, t0.Add(time.Minute))))
		if adm.Decision.Reason != ReasonArrivalWindow || !adm.Decision.Until.Equal(t0.Add(time.Minute+Window)) {
			t.Fatalf("decision %+v (%v)", adm.Decision, adm.EvidenceError)
		}
		if got := adm.Outbound.Recipients[0].Delivery.State; got != delivery.StateAccepted {
			t.Errorf("the record was not brought up to date: %v", got)
		}
		if adm.Report != envelope.ReportApplied {
			t.Errorf("report effect %v", adm.Report)
		}
	})
	t.Run("a delivered report", func(t *testing.T) {
		adm := mustAdmit(t, request(&o, onA(2), now, deliveredReport(t, runtimeR, t0.Add(5*time.Minute), t0.Add(4*time.Minute))))
		if adm.Decision.Reason != ReasonDeliveredWindow || !adm.Decision.Until.Equal(t0.Add(4*time.Minute+Window)) {
			t.Fatalf("decision %+v (%v)", adm.Decision, adm.EvidenceError)
		}
		if got := adm.Outbound.Recipients[0].Delivery.State; got != delivery.StateDelivered {
			t.Errorf("state %v", got)
		}
	})
	if o.Recipients[0].Delivery.State != delivery.StateQueued {
		t.Error("the request's record was changed")
	}
}

func TestAReplyFromTheNewSessionArrivingBeforeTheControlReplyIsAccepted(t *testing.T) {
	now := t0.Add(time.Hour)
	o := createOutbound(true)
	t.Run("with the control reply and the report", func(t *testing.T) {
		adm := mustAdmit(t, request(&o, onA(3), now, createdReply(t, t0.Add(time.Minute)), acceptedReport(t, sessionD, t0.Add(2*time.Minute))))
		if !adm.Decision.Granted() || adm.Decision.Reason != ReasonArrivalWindow {
			t.Fatalf("decision %+v (%v)", adm.Decision, adm.EvidenceError)
		}
		if adm.Handling == nil || !adm.Handling.DerivedCreated || adm.Outbound.Derived == nil || adm.Outbound.Derived.Address != onA(3) {
			t.Fatalf("the derived delivery was not set up by the single handling entry: %+v", adm.Handling)
		}
		if adm.Outbound.Recipients[0].Delivery.State != delivery.StateDelivered {
			t.Errorf("the control signal's delivery: %v", adm.Outbound.Recipients[0].Delivery.State)
		}
	})
	t.Run("the state is the one the pulled reply would have set up", func(t *testing.T) {
		viaEvidence := mustAdmit(t, request(&o, onA(3), now, createdReply(t, t0.Add(time.Minute)), acceptedReport(t, sessionD, t0.Add(2*time.Minute))))
		pulled := learn(t, learnReply(t, o, createdReply(t, t0.Add(time.Minute)), now), acceptedReport(t, sessionD, t0.Add(2*time.Minute)), now)
		if !reflect.DeepEqual(*viaEvidence.Outbound, pulled) {
			t.Errorf("evidence path and pull path differ:\n%+v\n%+v", *viaEvidence.Outbound, pulled)
		}
	})
	t.Run("without the report, the binding is kept but there is no proof of arrival", func(t *testing.T) {
		adm := mustAdmit(t, request(&o, onA(3), now, createdReply(t, t0.Add(time.Minute))))
		if adm.Decision.Reason != ReasonNoArrivalProof || adm.Outbound.Derived == nil {
			t.Fatalf("decision %+v derived %v", adm.Decision, adm.Outbound.Derived)
		}
	})
	t.Run("with the report alone the session is not known", func(t *testing.T) {
		adm := mustAdmit(t, request(&o, onA(3), now, acceptedReport(t, sessionD, t0.Add(2*time.Minute))))
		if adm.Decision.Reason != ReasonNotRecipient || adm.Report != envelope.ReportNotTracked {
			t.Fatalf("decision %+v report %v", adm.Decision, adm.Report)
		}
	})
	t.Run("after the control reply was pulled the reply needs only the report", func(t *testing.T) {
		pulled := learnReply(t, o, createdReply(t, t0.Add(time.Minute)), now)
		adm := mustAdmit(t, request(&pulled, onA(3), now, acceptedReport(t, sessionD, t0.Add(2*time.Minute))))
		if !adm.Decision.Granted() {
			t.Fatalf("decision %+v (%v)", adm.Decision, adm.EvidenceError)
		}
	})
	t.Run("an unconfirmed creation binds as well", func(t *testing.T) {
		unconfirmed := sealCreateReply(t, "m-1", envelope.CreateResult{Outcome: 2, SessionEndpoint: sessionD, Reason: 7}, t0.Add(time.Minute), idA, netA)
		adm := mustAdmit(t, request(&o, onA(3), now, unconfirmed, acceptedReport(t, sessionD, t0.Add(2*time.Minute))))
		if !adm.Decision.Granted() {
			t.Fatalf("decision %+v (%v)", adm.Decision, adm.EvidenceError)
		}
	})
	t.Run("after the control signal was judged expired, the reply corrects it and the session is bound", func(t *testing.T) {
		late := delivery.ExpiryJudgeableAfter(accept).Add(time.Second)
		rec, _, err := o.Recipients[0].Delivery.Apply(late, delivery.Event{Type: delivery.EventExpiryQueried, QueriedAt: late})
		requireNoErr(t, err)
		expired := o
		expired.Recipients = []envelope.Tracked{{Address: onA(2), Delivery: rec}}
		adm := mustAdmit(t, request(&expired, onA(3), late.Add(time.Minute), createdReply(t, t0.Add(time.Minute)), acceptedReport(t, sessionD, t0.Add(2*time.Minute))))
		if !adm.Decision.Granted() {
			t.Fatalf("decision %+v (%v)", adm.Decision, adm.EvidenceError)
		}
		if fb := adm.Handling.Control.Feedback; len(fb) != 1 || fb[0].Kind != delivery.KindExpiredCorrected {
			t.Errorf("feedback %+v: the sender must hear of the correction once", fb)
		}
	})
}

func TestAForgedReplyToFromAThirdPartyIsRefused(t *testing.T) {
	now := t0.Add(time.Hour)
	delivered := learn(t, messageOutbound(), deliveredReport(t, runtimeR, t0.Add(3*time.Minute), t0.Add(2*time.Minute)), now)
	genuine := deliveredReport(t, runtimeR, t0.Add(3*time.Minute), t0.Add(2*time.Minute))
	t.Run("a node that is not a recipient's, with no evidence", func(t *testing.T) {
		adm := mustAdmit(t, request(&delivered, onC(2), now))
		if adm.Decision.Granted() || adm.Decision.Reason != ReasonNotRecipient {
			t.Errorf("decision %+v", adm.Decision)
		}
	})
	t.Run("a node that is not a recipient's, replaying the target's genuine report", func(t *testing.T) {
		o := messageOutbound()
		adm := mustAdmit(t, request(&o, onC(2), now, genuine))
		if adm.Decision.Granted() || adm.Decision.Reason != ReasonNotRecipient || adm.Outbound.Recipients[0].Delivery.State != delivery.StateQueued {
			t.Errorf("decision %+v; the evidence of a stranger must not be applied", adm.Decision)
		}
		if adm.EvidenceError != nil {
			t.Errorf("the evidence of a stranger was read and reported as a failure: %v", adm.EvidenceError)
		}
	})
	t.Run("a node that is not a recipient's, with a report it signed itself", func(t *testing.T) {
		own := sealReport(t, "m-1", runtimeR, delivery.StatusReport{State: delivery.StateDelivered}, t0.Add(time.Minute), t0, idC, netC)
		o := messageOutbound()
		adm := mustAdmit(t, request(&o, onC(2), now, own))
		if adm.Decision.Granted() {
			t.Errorf("decision %+v", adm.Decision)
		}
	})
	t.Run("another endpoint of the recipient's node, replaying the genuine report", func(t *testing.T) {
		o := messageOutbound()
		adm := mustAdmit(t, request(&o, onA(8), now, genuine))
		if adm.Decision.Granted() {
			t.Errorf("decision %+v", adm.Decision)
		}
		// the report is about endpoint 2: it is discarded, and endpoint 8 is no recipient
		if adm.Decision.Reason != ReasonNotRecipient || adm.EvidenceError == nil {
			t.Errorf("reason %v, evidence error %v", adm.Decision.Reason, adm.EvidenceError)
		}
	})
	t.Run("another endpoint of the recipient's node with genuine evidence about itself", func(t *testing.T) {
		o := messageOutbound()
		adm := mustAdmit(t, request(&o, onA(8), now, deliveredReport(t, ep(8), t0.Add(3*time.Minute), t0.Add(2*time.Minute))))
		if adm.Decision.Granted() || adm.Decision.Reason != ReasonNotRecipient {
			t.Errorf("decision %+v", adm.Decision)
		}
	})
	t.Run("a stranger claiming to be the session a created session bound", func(t *testing.T) {
		o := createOutbound(true)
		adm := mustAdmit(t, request(&o, onA(9), now, createdReply(t, t0.Add(time.Minute)), acceptedReport(t, ep(9), t0.Add(2*time.Minute))))
		if adm.Decision.Granted() {
			t.Errorf("decision %+v", adm.Decision)
		}
	})
	t.Run("a stranger replaying the control reply that binds the session", func(t *testing.T) {
		o := createOutbound(true)
		adm := mustAdmit(t, request(&o, onC(3), now, createdReply(t, t0.Add(time.Minute)), acceptedReport(t, sessionD, t0.Add(2*time.Minute))))
		if adm.Decision.Granted() || adm.Decision.Reason != ReasonNotRecipient {
			t.Errorf("decision %+v", adm.Decision)
		}
	})
	t.Run("a reply_to that names no message of ours", func(t *testing.T) {
		adm := mustAdmit(t, request(nil, onA(2), now, genuine))
		if adm.Decision.Reason != ReasonNoOutbound {
			t.Errorf("decision %+v", adm.Decision)
		}
	})
	t.Run("a reply addressed to an endpoint that did not send the message", func(t *testing.T) {
		req := request(&delivered, onA(2), now)
		req.Target = ep(0x0b)
		if adm := mustAdmit(t, req); adm.Decision.Reason != ReasonNotSender {
			t.Errorf("decision %+v", adm.Decision)
		}
	})
}

func TestTheOwnerCanTurnReplyPermissionOff(t *testing.T) {
	now := t0.Add(time.Hour)
	o := learn(t, messageOutbound(), deliveredReport(t, runtimeR, t0.Add(3*time.Minute), t0.Add(2*time.Minute)), now)
	req := request(&o, onA(2), now)
	if !mustAdmit(t, req).Decision.Granted() {
		t.Fatal("setup: not granted when on")
	}
	req.Enabled = false
	adm := mustAdmit(t, req)
	if adm.Decision.Granted() || adm.Decision.Reason != ReasonDisabled {
		t.Errorf("decision %+v", adm.Decision)
	}
	// the zero Request is off too
	if got := mustAdmit(t, Request{Now: now, Local: nodeOf(idB), Replier: onA(2), Target: senderEndpoint(), ReplyTo: "m-1", Outbound: &o}); got.Decision.Reason != ReasonDisabled {
		t.Errorf("a request that never said it is on: %+v", got.Decision)
	}
	t.Run("off, the evidence is not applied", func(t *testing.T) {
		fresh := messageOutbound()
		r := request(&fresh, onA(2), now, acceptedReport(t, runtimeR, t0))
		r.Enabled = false
		adm := mustAdmit(t, r)
		if adm.Outbound.Recipients[0].Delivery.State != delivery.StateQueued {
			t.Error("evidence applied while the switch was off")
		}
	})
}

func TestWindow(t *testing.T) {
	deliveredAt := t0.Add(2 * time.Minute)
	o := learn(t, messageOutbound(), deliveredReport(t, runtimeR, t0.Add(3*time.Minute), deliveredAt), t0.Add(4*time.Minute))
	arrived := learn(t, messageOutbound(), acceptedReport(t, runtimeR, t0.Add(3*time.Minute)), t0.Add(4*time.Minute))
	cases := []struct {
		rule string
		o    envelope.Outbound
		now  time.Time
		want Reason
	}{
		{"granted at once after delivery", o, deliveredAt, ReasonDeliveredWindow},
		{"granted a second before 24 hours from the delivery time", o, deliveredAt.Add(Window - time.Second), ReasonDeliveredWindow},
		{"no permission at exactly 24 hours from the delivery time", o, deliveredAt.Add(Window), ReasonWindowClosed},
		{"no permission after 24 hours", o, deliveredAt.Add(Window + time.Hour), ReasonWindowClosed},
		{"the window does not run from the report time", o, t0.Add(3*time.Minute + Window - time.Second), ReasonWindowClosed},
		{"granted a second before 24 hours from the arrival report", arrived, t0.Add(3*time.Minute + Window - time.Second), ReasonArrivalWindow},
		{"no permission at exactly 24 hours from the arrival report", arrived, t0.Add(3*time.Minute + Window), ReasonWindowClosed},
		{"no permission without any proof", func() envelope.Outbound { return messageOutbound() }(), t0.Add(time.Minute), ReasonNoArrivalProof},
	}
	for _, c := range cases {
		t.Run(c.rule, func(t *testing.T) {
			o := c.o
			adm := mustAdmit(t, request(&o, onA(2), c.now))
			if adm.Decision.Reason != c.want {
				t.Fatalf("reason %v, want %v", adm.Decision.Reason, c.want)
			}
			if adm.Decision.Granted() != (c.want == ReasonDeliveredWindow || c.want == ReasonArrivalWindow) {
				t.Error("Granted disagrees with the reason")
			}
			if !adm.Decision.Granted() && !adm.Decision.Until.IsZero() {
				t.Error("a decision without permission has a window end")
			}
		})
	}
	t.Run("a window of 24 hours is not extended by a later report", func(t *testing.T) {
		later := learn(t, arrived, acceptedReport(t, runtimeR, t0.Add(2*time.Hour)), t0.Add(11*time.Hour))
		adm := mustAdmit(t, request(&later, onA(2), t0.Add(3*time.Minute+Window)))
		if adm.Decision.Reason != ReasonWindowClosed {
			t.Errorf("decision %+v", adm.Decision)
		}
	})
	t.Run("a rejected delivery proves no arrival", func(t *testing.T) {
		rejected := learn(t, messageOutbound(), sealReport(t, "m-1", runtimeR, delivery.StatusReport{State: delivery.StateRejected, Reject: delivery.RejectUnauthorized}, t0.Add(time.Minute), time.Time{}, idA, netA), t0.Add(2*time.Minute))
		adm := mustAdmit(t, request(&rejected, onA(2), t0.Add(3*time.Minute)))
		if adm.Decision.Reason != ReasonNoArrivalProof {
			t.Errorf("decision %+v", adm.Decision)
		}
	})
	t.Run("a handed-out delivery with an unknown result proves arrival", func(t *testing.T) {
		unknown := learn(t, messageOutbound(), sealReport(t, "m-1", runtimeR, delivery.StatusReport{State: delivery.StateDeliveryUnknown}, t0.Add(time.Minute), time.Time{}, idA, netA), t0.Add(2*time.Minute))
		adm := mustAdmit(t, request(&unknown, onA(2), t0.Add(3*time.Minute)))
		if adm.Decision.Reason != ReasonArrivalWindow {
			t.Errorf("decision %+v", adm.Decision)
		}
	})
}

func TestEvidenceThatDoesNotVerifyIsDiscardedAndTheReplyIsJudgedWithoutIt(t *testing.T) {
	now := t0.Add(time.Hour)
	good := acceptedReport(t, runtimeR, t0.Add(time.Minute))
	expiredKey := acceptedOn(onA(2))
	expiredKey.Cert.NotAfter = t0.Add(30 * time.Minute)
	revoked := acceptedOn(onA(2))
	revoked.Revocation = record.KeyRevocationRevoked
	deliveredRecord := func() envelope.Outbound {
		return learn(t, messageOutbound(), deliveredReport(t, runtimeR, t0.Add(3*time.Minute), t0.Add(2*time.Minute)), now)
	}
	boundRecord := func() envelope.Outbound {
		return learn(t, learnReply(t, createOutbound(true), createdReply(t, t0.Add(time.Minute)), now), acceptedReport(t, sessionD, t0.Add(2*time.Minute)), now)
	}
	cases := []struct {
		rule     string
		o        func() envelope.Outbound
		replier  ident.Address
		evidence []record.Record
		mut      func(*Request)
		// wantErr, if set, is the sentinel the evidence error must wrap.
		wantErr error
	}{
		{"a report signed by another node's network key", messageOutbound2, onA(2),
			[]record.Record{sealReport(t, "m-1", runtimeR, delivery.StatusReport{State: delivery.StateAccepted}, t0.Add(time.Minute), time.Time{}, idC, netC)}, nil, nil},
		{"a report about another message", messageOutbound2, onA(2),
			[]record.Record{sealReport(t, "m-2", runtimeR, delivery.StatusReport{State: delivery.StateAccepted}, t0.Add(time.Minute), time.Time{}, idA, netA)}, nil, envelope.ErrMismatch},
		{"a report about another endpoint than the replier", messageOutbound2, onA(2),
			[]record.Record{acceptedReport(t, ep(5), t0.Add(time.Minute))}, nil, envelope.ErrMismatch},
		{"a report naming another sending node", messageOutbound2, onA(2),
			[]record.Record{sealReportFrom(t, nodeOf(idC), "m-1", runtimeR, delivery.StatusReport{State: delivery.StateAccepted}, t0.Add(time.Minute))}, nil, envelope.ErrMismatch},
		{"a report dated in the future", messageOutbound2, onA(2),
			[]record.Record{acceptedReport(t, runtimeR, now.Add(3*time.Minute))}, nil, envelope.ErrFromFuture},
		{"a report under an expired certificate", messageOutbound2, onA(2), []record.Record{good}, func(r *Request) { r.Accepted = expiredKey }, nil},
		{"a report under a revoked certificate", messageOutbound2, onA(2), []record.Record{good}, func(r *Request) { r.Accepted = revoked }, nil},
		{"a report with no certificate to check it", messageOutbound2, onA(2), []record.Record{good}, func(r *Request) { r.Accepted = record.AcceptedNetworkKey{} }, nil},
		{"a report that contradicts the record", deliveredRecord, onA(2),
			[]record.Record{sealReport(t, "m-1", runtimeR, delivery.StatusReport{State: delivery.StateRejected, Reject: delivery.RejectExpired}, t0.Add(time.Minute), time.Time{}, idA, netA)}, nil, envelope.ErrPeerContradiction},
		{"a control reply for a message that is not a control signal", messageOutbound2, onA(2), []record.Record{createdReply(t, t0.Add(time.Minute))}, nil, envelope.ErrMismatch},
		{"a control reply about another message", func() envelope.Outbound { return createOutbound(true) }, onA(3),
			[]record.Record{sealCreateReply(t, "m-2", envelope.CreateResult{Outcome: 1, SessionEndpoint: sessionD}, t0.Add(time.Minute), idA, netA)}, nil, envelope.ErrMismatch},
		{"a control reply signed by another node", func() envelope.Outbound { return createOutbound(true) }, onA(3),
			[]record.Record{sealCreateReply(t, "m-1", envelope.CreateResult{Outcome: 1, SessionEndpoint: sessionD}, t0.Add(time.Minute), idC, netC)}, nil, nil},
		{"a control reply naming another sending node", func() envelope.Outbound { return createOutbound(true) }, onA(3),
			[]record.Record{sealCreateReplyFrom(t, nodeOf(idC), envelope.CreateResult{Outcome: 1, SessionEndpoint: sessionD}, t0.Add(time.Minute))}, nil, envelope.ErrMismatch},
		{"a control reply dated in the future", func() envelope.Outbound { return createOutbound(true) }, onA(3), []record.Record{createdReply(t, now.Add(3*time.Minute))}, nil, envelope.ErrFromFuture},
		{"a control reply that names another session than the bound one", boundRecord, onA(3),
			[]record.Record{sealCreateReply(t, "m-1", envelope.CreateResult{Outcome: 1, SessionEndpoint: ep(4)}, t0.Add(time.Minute), idA, netA)}, nil, envelope.ErrPeerContradiction},
		{"a control reply after a failure was recorded", func() envelope.Outbound {
			return learnReply(t, createOutbound(true), failedReply(t, t0.Add(30*time.Second)), now)
		}, onA(3), []record.Record{createdReply(t, t0.Add(time.Minute))}, nil, envelope.ErrPeerContradiction},
		{"evidence in the wrong order", func() envelope.Outbound { return createOutbound(true) }, onA(3),
			[]record.Record{acceptedReport(t, sessionD, t0.Add(time.Minute)), createdReply(t, t0.Add(time.Minute))}, nil, envelope.ErrEvidence},
		{"two reports", messageOutbound2, onA(2), []record.Record{good, good}, nil, envelope.ErrEvidence},
		{"a record of another type", messageOutbound2, onA(2), []record.Record{{Type: "sidera/node-declaration/1", Payload: []byte{0xa0}, Signatures: good.Signatures}}, nil, envelope.ErrEvidence},
		{"a report next to a control reply that fails is discarded with it", messageOutbound2, onA(2),
			[]record.Record{createdReply(t, t0.Add(time.Minute)), good}, nil, envelope.ErrMismatch},
	}
	for _, c := range cases {
		t.Run(c.rule, func(t *testing.T) {
			o := c.o()
			before := o
			req := request(&o, c.replier, now, c.evidence...)
			if c.mut != nil {
				c.mut(&req)
			}
			adm := mustAdmit(t, req)
			if adm.EvidenceError == nil {
				t.Fatalf("the evidence was not reported as failed; decision %+v", adm.Decision)
			}
			if c.wantErr != nil && !errors.Is(adm.EvidenceError, c.wantErr) {
				t.Errorf("evidence error %v does not wrap %v", adm.EvidenceError, c.wantErr)
			}
			if !reflect.DeepEqual(*adm.Outbound, before) || adm.Handling != nil || adm.Report != envelope.ReportNone || adm.Reported != nil {
				t.Error("discarded evidence left a trace")
			}
			// the verdict is the one the same reply gets without any evidence
			bare := req
			bare.Evidence = nil
			want := mustAdmit(t, bare).Decision
			if adm.Decision != want {
				t.Errorf("decision %+v, want %+v (the reply without evidence)", adm.Decision, want)
			}
		})
	}
}

func TestStaleEvidenceDoesNotTakePermissionFromAReplyWhoseFactsAreKnown(t *testing.T) {
	// The target rotated its network key after it signed the evidence: the
	// evidence no longer verifies, but the sender already learned that the
	// message arrived and which session the control signal created.
	now := t0.Add(time.Hour)
	evidence := []record.Record{createdReply(t, t0.Add(time.Minute)), acceptedReport(t, sessionD, t0.Add(2*time.Minute))}
	known := learn(t, learnReply(t, createOutbound(true), createdReply(t, t0.Add(time.Minute)), now), acceptedReport(t, sessionD, t0.Add(2*time.Minute)), now)
	rotated := acceptedFor(idA, 0x99)

	t.Run("a reply from the new session", func(t *testing.T) {
		req := request(&known, onA(3), now, evidence...)
		req.Accepted = rotated
		adm := mustAdmit(t, req)
		if adm.Decision.Reason != ReasonArrivalWindow || !adm.Decision.Until.Equal(t0.Add(2*time.Minute+Window)) {
			t.Fatalf("decision %+v", adm.Decision)
		}
		if adm.EvidenceError == nil {
			t.Error("the stale evidence was not reported")
		}
		if !reflect.DeepEqual(*adm.Outbound, known) {
			t.Error("the stale evidence changed the record")
		}
	})
	t.Run("a reply to a plain message", func(t *testing.T) {
		o := learn(t, messageOutbound(), deliveredReport(t, runtimeR, t0.Add(3*time.Minute), t0.Add(2*time.Minute)), now)
		req := request(&o, onA(2), now, deliveredReport(t, runtimeR, t0.Add(3*time.Minute), t0.Add(2*time.Minute)))
		req.Accepted = rotated
		adm := mustAdmit(t, req)
		if adm.Decision.Reason != ReasonDeliveredWindow || adm.EvidenceError == nil {
			t.Fatalf("decision %+v evidence error %v", adm.Decision, adm.EvidenceError)
		}
	})
	t.Run("the same stale evidence gives nothing to a reply whose facts are not known", func(t *testing.T) {
		o := createOutbound(true)
		req := request(&o, onA(3), now, evidence...)
		req.Accepted = rotated
		adm := mustAdmit(t, req)
		if adm.Decision.Granted() || adm.Decision.Reason != ReasonNotRecipient || adm.EvidenceError == nil {
			t.Fatalf("decision %+v evidence error %v", adm.Decision, adm.EvidenceError)
		}
	})
}

func messageOutbound2() envelope.Outbound { return messageOutbound() }

func TestOnlyASessionCreateWithABodyGivesTheSessionAPermission(t *testing.T) {
	now := t0.Add(time.Hour)
	evidence := func() []record.Record {
		return []record.Record{createdReply(t, t0.Add(time.Minute)), acceptedReport(t, sessionD, t0.Add(2*time.Minute))}
	}
	t.Run("without an initial message", func(t *testing.T) {
		o := createOutbound(false)
		adm := mustAdmit(t, request(&o, onA(3), now, evidence()...))
		if adm.Decision.Granted() || adm.Decision.Reason != ReasonNotRecipient || adm.Outbound.Derived != nil {
			t.Errorf("decision %+v derived %v", adm.Decision, adm.Outbound.Derived)
		}
		if adm.Outbound.Recipients[0].Delivery.State != delivery.StateDelivered {
			t.Error("the reply must still end the control signal")
		}
	})
	t.Run("when the creation failed", func(t *testing.T) {
		o := createOutbound(true)
		adm := mustAdmit(t, request(&o, onA(3), now, failedReply(t, t0.Add(time.Minute)), acceptedReport(t, sessionD, t0.Add(2*time.Minute))))
		if adm.Decision.Granted() || adm.Outbound.Derived != nil {
			t.Errorf("decision %+v derived %v", adm.Decision, adm.Outbound.Derived)
		}
	})
	t.Run("the runtime endpoint itself never gets permission to answer a session.create", func(t *testing.T) {
		// Its result returns as the control reply. The evidence is still a true
		// fact and is applied.
		o := createOutbound(true)
		adm := mustAdmit(t, request(&o, onA(2), now, acceptedReport(t, runtimeR, t0.Add(time.Minute))))
		if adm.Decision.Granted() || adm.Decision.Reason != ReasonControlSignal || adm.EvidenceError != nil {
			t.Errorf("decision %+v (%v)", adm.Decision, adm.EvidenceError)
		}
		if adm.Outbound.Recipients[0].Delivery.State != delivery.StateAccepted {
			t.Error("the report was not applied")
		}
	})
	t.Run("an endpoint identifier of one recipient's node is not a recipient on another node", func(t *testing.T) {
		o := messageOutbound()
		o.Recipients = append(o.Recipients, envelope.Tracked{Address: onC(6), Delivery: queued()})
		o = learn(t, o, deliveredReport(t, runtimeR, t0.Add(3*time.Minute), t0.Add(2*time.Minute)), now)
		// node C is a recipient's node, and endpoint 2 is a recipient, but of node A
		adm := mustAdmit(t, request(&o, onC(2), now))
		if adm.Decision.Granted() || adm.Decision.Reason != ReasonNotRecipient {
			t.Errorf("decision %+v", adm.Decision)
		}
	})
	t.Run("each recipient of a multi-recipient message answers for itself", func(t *testing.T) {
		o := messageOutbound()
		o.Recipients = append(o.Recipients, envelope.Tracked{Address: onC(6), Delivery: queued()})
		fromC := sealReport(t, "m-1", ep(6), delivery.StatusReport{State: delivery.StateAccepted}, t0.Add(time.Minute), time.Time{}, idC, netC)
		adm := mustAdmit(t, request(&o, onC(6), now, fromC))
		if !adm.Decision.Granted() {
			t.Fatalf("decision %+v (%v)", adm.Decision, adm.EvidenceError)
		}
		if adm.Outbound.Recipients[0].Delivery.State != delivery.StateQueued || adm.Outbound.Recipients[1].Delivery.State != delivery.StateAccepted {
			t.Error("a report from one recipient moved the other's delivery")
		}
		// A's report for endpoint 2 does not give C's endpoint a permission
		if got := mustAdmit(t, request(&o, onC(6), now)); got.Decision.Granted() {
			t.Error("granted to C without proof")
		}
	})
}

func TestTheOriginalRecipientOfAControlSignalNeverGetsReplyPermission(t *testing.T) {
	now := t0.Add(time.Hour)
	delivered := deliveredReport(t, runtimeR, t0.Add(3*time.Minute), t0.Add(2*time.Minute))
	for _, kind := range []envelope.Kind{envelope.KindSessionCreate, envelope.KindSessionClose, envelope.KindSessionInterrupt, envelope.KindEndpointStatus} {
		for _, body := range []bool{false, true} {
			if body && kind != envelope.KindSessionCreate {
				continue
			}
			t.Run(kind.String()+bodyName(body), func(t *testing.T) {
				o := createOutbound(body)
				o.Kind = kind
				// every fact a plain message needs for permission is known
				known := learn(t, o, delivered, now)
				adm := mustAdmit(t, request(&known, onA(2), now))
				if adm.Decision.Granted() || adm.Decision.Reason != ReasonControlSignal || !adm.Decision.Until.IsZero() {
					t.Errorf("with the facts held: %+v", adm.Decision)
				}
				fresh := mustAdmit(t, request(&o, onA(2), now, delivered))
				if fresh.Decision.Granted() || fresh.Decision.Reason != ReasonControlSignal {
					t.Errorf("with the facts as evidence: %+v", fresh.Decision)
				}
			})
		}
	}
	t.Run("a plain message to the same endpoint does get it", func(t *testing.T) {
		o := learn(t, messageOutbound(), delivered, now)
		if adm := mustAdmit(t, request(&o, onA(2), now)); !adm.Decision.Granted() {
			t.Errorf("decision %+v", adm.Decision)
		}
	})
	t.Run("the session the control signal created does, though the control signal itself is known to have arrived", func(t *testing.T) {
		o := learn(t, learn(t, learnReply(t, createOutbound(true), createdReply(t, t0.Add(time.Minute)), now), delivered, now),
			acceptedReport(t, sessionD, t0.Add(2*time.Minute)), now)
		if adm := mustAdmit(t, request(&o, onA(3), now)); !adm.Decision.Granted() {
			t.Errorf("decision %+v", adm.Decision)
		}
		if adm := mustAdmit(t, request(&o, onA(2), now)); adm.Decision.Reason != ReasonControlSignal {
			t.Errorf("the runtime endpoint of the same record: %+v", adm.Decision)
		}
	})
}

func bodyName(body bool) string {
	if body {
		return " with a body"
	}
	return ""
}

func TestInvalidRequests(t *testing.T) {
	o := messageOutbound()
	now := t0.Add(time.Hour)
	base := request(&o, onA(2), now)
	cases := map[string]func(*Request){
		"no time":                        func(r *Request) { r.Now = time.Time{} },
		"no local node":                  func(r *Request) { r.Local = ident.NodeID{} },
		"no replier":                     func(r *Request) { r.Replier.Node = ident.NodeID{} },
		"no target":                      func(r *Request) { r.Target = ident.EndpointID{} },
		"no reply_to":                    func(r *Request) { r.ReplyTo = "" },
		"an outbound of another message": func(r *Request) { r.ReplyTo = "m-2" },
		"an invalid outbound":            func(r *Request) { bad := o; bad.From = ident.EndpointID{}; r.Outbound = &bad },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			r := base
			mut(&r)
			_, err := Admit(r)
			requireErr(t, err, ErrInvalidRequest)
		})
	}
}

func TestReasonNames(t *testing.T) {
	want := map[Reason]string{
		ReasonDeliveredWindow: "delivered_window", ReasonArrivalWindow: "arrival_window", ReasonDisabled: "disabled",
		ReasonNoOutbound: "no_outbound", ReasonNotSender: "not_sender", ReasonNotRecipient: "not_recipient",
		ReasonControlSignal: "control_signal", ReasonNoArrivalProof: "no_arrival_proof", ReasonWindowClosed: "window_closed",
		ReasonInvalid: "invalid", Reason(200): "invalid",
	}
	for r, w := range want {
		if r.String() != w {
			t.Errorf("%d = %q, want %q", r, r.String(), w)
		}
	}
	if (Decision{Reason: ReasonInvalid}).Granted() {
		t.Error("the zero decision grants")
	}
}
