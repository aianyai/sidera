package reply

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// FuzzAdmitEvidence feeds arbitrary bytes, read as a record, to Admit as the
// evidence of a reply. Nothing may panic, and permission is never earned by
// evidence that does not verify: whenever evidence turns a reply that has no
// permission without it into one that has, every record of that evidence
// verifies, for the node that was the target, as the sender's own node's
// message. Evidence checked against another node's key contributes nothing, and
// a replier on a node that is not a recipient's never gets permission.
func FuzzAdmitEvidence(f *testing.F) {
	now := t0.Add(time.Hour)
	seeds := []record.Record{
		acceptedReport(f, runtimeR, t0.Add(time.Minute)),
		deliveredReport(f, runtimeR, t0.Add(3*time.Minute), t0.Add(2*time.Minute)),
		acceptedReport(f, sessionD, t0.Add(time.Minute)),
		createdReply(f, t0.Add(time.Minute)),
		failedReply(f, t0.Add(time.Minute)),
		// signed by the target's key but made for another sending node
		sealReportFrom(f, nodeOf(idC), "m-1", runtimeR, delivery.StatusReport{State: delivery.StateAccepted}, t0.Add(time.Minute)),
		sealCreateReplyFrom(f, nodeOf(idC), envelope.CreateResult{Outcome: 1, SessionEndpoint: sessionD}, t0.Add(time.Minute)),
		// signed by another node's key
		sealReport(f, "m-1", runtimeR, delivery.StatusReport{State: delivery.StateAccepted}, t0.Add(time.Minute), time.Time{}, idC, netC),
		// about another message
		sealReport(f, "m-2", runtimeR, delivery.StatusReport{State: delivery.StateAccepted}, t0.Add(time.Minute), time.Time{}, idA, netA),
	}
	for _, s := range seeds {
		b, err := s.Encode()
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	// The oracle states the rule itself and uses only the record package's own
	// verification: signed by the network key of the target node A, about this
	// node B's message m-1.
	verifies := func(r record.Record) bool {
		switch r.Type {
		case envelope.StatusReportType:
			v, err := record.Verify(envelope.StatusReportDescriptor(nodeOf(idA), acceptedFor(idA, netA), now), r)
			return err == nil && v.Payload().Sender == nodeOf(idB) && v.Payload().Message == "m-1"
		case envelope.ControlReplyType:
			v, err := record.Verify(envelope.ControlReplyDescriptor(nodeOf(idA), acceptedFor(idA, netA), now), r)
			return err == nil && v.Payload().Sender == nodeOf(idB) && v.Payload().Message == "m-1"
		}
		return false
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := record.Decode(data, record.DefaultMaxInput)
		if err != nil {
			return
		}
		type shape struct {
			name     string
			outbound func() envelope.Outbound
			replier  ident.Address
			evidence []record.Record
			accepted *record.AcceptedNetworkKey // overrides the replier's own
			never    bool                       // permission is impossible
		}
		wrongKey := acceptedOn(onC(2))
		shapes := []shape{
			{"a message", messageOutbound, onA(2), []record.Record{r}, nil, false},
			{"a session.create", func() envelope.Outbound { return createOutbound(true) }, onA(3), []record.Record{r}, nil, false},
			{"a session.create with its genuine control reply", func() envelope.Outbound { return createOutbound(true) }, onA(3), []record.Record{createdReply(t, t0), r}, nil, false},
			{"a session.create, the record as its control reply", func() envelope.Outbound { return createOutbound(true) }, onA(3), []record.Record{r, acceptedReport(t, sessionD, t0.Add(time.Minute))}, nil, false},
			{"the runtime endpoint of a session.create", func() envelope.Outbound { return createOutbound(true) }, onA(2), []record.Record{r}, nil, true},
			{"a replier on a node that is no recipient's", messageOutbound, onC(2), []record.Record{r}, nil, true},
			{"evidence checked against another node's key", messageOutbound, onA(2), []record.Record{r}, &wrongKey, false},
		}
		for _, sh := range shapes {
			o := sh.outbound()
			req := request(&o, sh.replier, now, sh.evidence...)
			if sh.accepted != nil {
				req.Accepted = *sh.accepted
			}
			adm, err := Admit(req)
			if err != nil {
				t.Fatalf("%s: a well-formed request errored: %v", sh.name, err)
			}
			if adm.Decision.Granted() && adm.Outbound == nil {
				t.Fatalf("%s: granted without a record", sh.name)
			}
			bare := req
			bare.Evidence = nil
			bareAdm, err := Admit(bare)
			if err != nil {
				t.Fatalf("%s: %v", sh.name, err)
			}
			switch {
			case sh.never && adm.Decision.Granted():
				t.Fatalf("%s: permission granted", sh.name)
			case adm.Decision.Granted() && !bareAdm.Decision.Granted():
				if adm.EvidenceError != nil {
					t.Fatalf("%s: permission earned by evidence that was reported as failed: %v", sh.name, adm.EvidenceError)
				}
				if sh.accepted != nil {
					t.Fatalf("%s: permission earned by evidence checked against another node's key", sh.name)
				}
				for _, ev := range sh.evidence {
					if !verifies(ev) {
						t.Fatalf("%s: permission earned by evidence that does not verify for the target node: %s", sh.name, ev.Type)
					}
				}
			}
		}
	})
}
