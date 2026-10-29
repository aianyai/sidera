package recipes

import (
	"fmt"
	"strings"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

type deliveryObligation struct {
	message         adapterproto.MessageID
	recipient, body string
}
type feedbackRef struct {
	message   adapterproto.MessageID
	recipient string
	kind      delivery.Kind
}

// DeliveryEvidence relates successful calls to externally visible bodies and
// actual system notices. The zero value belongs to one backend run only.
type DeliveryEvidence struct {
	accepted []deliveryObligation
	notices  map[feedbackRef]adapterproto.MessageID
	required []feedbackRef
}

// Accepted records only a call that returned success, never a guessed identifier
// of a call cut by a crash. Each resolved recipient carries its own obligation.
func (d *DeliveryEvidence) Accepted(r fakeagent.SendResult, body string) {
	for _, recipient := range r.To {
		d.accepted = append(d.accepted, deliveryObligation{r.MessageID, recipient, body})
	}
}

func (d *DeliveryEvidence) RequireFeedback(message adapterproto.MessageID, recipient string, kind delivery.Kind) {
	d.required = append(d.required, feedbackRef{message, recipient, kind})
}

// RecvAndAck drains at most 64 pages. Every acknowledged part comes from the
// page just returned by the real agent, including partial oversized messages.
func (d *DeliveryEvidence) RecvAndAck(e scenario.Env, a fakeagent.Agent) error {
	me, err := a.Me(e.Ctx())
	if err != nil {
		return err
	}
	local, err := ident.ParseAddress(me.Address)
	if err != nil {
		return err
	}
	for n := 0; n < 64; n++ {
		r, err := a.Recv(e.Ctx(), fakeagent.RecvInput{Frontend: adapterproto.FrontendCLI})
		if err != nil {
			return err
		}
		if len(r.Page.Messages) == 0 {
			return nil
		}
		items := make([]adapterproto.PartRef, 0, len(r.Page.Messages))
		for _, m := range r.Page.Messages {
			if err := d.observeNotice(m, local.Node); err != nil {
				return err
			}
			part := 1
			if m.Part != nil {
				part = m.Part.Index
			}
			items = append(items, adapterproto.PartRef{MessageID: m.ID, Part: part})
		}
		ack, err := a.Ack(e.Ctx(), fakeagent.AckInput{Lease: r.Page.Lease, Items: items})
		if err != nil {
			return err
		}
		if len(ack.Items) != len(items) {
			return fmt.Errorf("ack returned %d outcomes for %d visible parts", len(ack.Items), len(items))
		}
		for i, got := range ack.Items {
			if got.MessageID != items[i].MessageID || got.Part != items[i].Part || (got.Ack != adapterproto.ItemRecorded && got.Ack != adapterproto.ItemCompleted && got.Ack != adapterproto.ItemDuplicate) {
				return fmt.Errorf("ack of visible part %+v returned %+v", items[i], got)
			}
		}
	}
	return fmt.Errorf("delivery drain exceeded 64 real pages")
}

// Feedback is verified against the committed notice text: fixed header, public
// kind summaries and exact message/recipient item lines. A system sender alone
// proves nothing. No production decoder or notice generator is duplicated here.
func (d *DeliveryEvidence) observeNotice(m adapterproto.PageMessage, local ident.NodeID) error {
	from, err := ident.ParseAddress(string(m.From))
	if err != nil {
		return err
	}
	if from.Endpoint != ident.SystemEndpoint() {
		return nil
	}
	if from.Node != local {
		return fmt.Errorf("notice %s came from another node system endpoint", m.ID)
	}
	const header = "Notice from the channel of this node, not a message from another endpoint.\n"
	if m.Part != nil || !strings.HasPrefix(m.Body, header) {
		return fmt.Errorf("system notice %s has an unsupported body", m.ID)
	}
	if d.notices == nil {
		d.notices = make(map[feedbackRef]adapterproto.MessageID)
	}
	var kind delivery.Kind
	items := 0
	for _, line := range strings.Split(strings.TrimPrefix(m.Body, header), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			kind = 0
			for k := delivery.KindUnreachable; k <= delivery.KindLostUnknownMessage; k++ {
				if line == k.String()+": "+k.Summary() {
					kind = k
					break
				}
			}
			if kind == 0 {
				return fmt.Errorf("system notice %s has unknown feedback heading %q", m.ID, line)
			}
			continue
		}
		if kind == 0 {
			return fmt.Errorf("system notice %s has an item without a kind", m.ID)
		}
		if kind == delivery.KindLostUnknownMessage && line == "- a message of this endpoint that cannot be named" {
			items++
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "-" || fields[1] != "message" || fields[3] != "to" {
			return fmt.Errorf("system notice %s has malformed item %q", m.ID, line)
		}
		original := adapterproto.MessageID(fields[2])
		if err := original.Validate(); err != nil {
			return err
		}
		target, err := ident.ParseAddress(fields[4])
		if err != nil {
			return err
		}
		ref := feedbackRef{original, target.String(), kind}
		if previous, ok := d.notices[ref]; ok && previous != m.ID {
			return fmt.Errorf("feedback %s/%s/%s repeated in %s and %s", original, target, kind, previous, m.ID)
		}
		d.notices[ref] = m.ID
		items++
	}
	if items == 0 {
		return fmt.Errorf("system notice %s contains no feedback items", m.ID)
	}
	return nil
}

// Check never reads daemon records to infer what a model saw. Complete bodies
// come from model.saw, and legal terminal notices from real receive results.
func (d *DeliveryEvidence) Check(e scenario.Env) error {
	log, err := e.Ledger()
	if err != nil {
		return err
	}
	for _, obligation := range d.accepted {
		visible := false
		target, err := ident.ParseAddress(obligation.recipient)
		if err != nil {
			return err
		}
		for _, seen := range log.Kind(ledger.KindModelSaw) {
			if seen.Attrs.Endpoint == target.Endpoint.String() && seen.Attrs.MessageID == string(obligation.message) && seen.Attrs.BodyDigest == ledger.Digest([]byte(obligation.body)) {
				visible = true
				break
			}
		}
		if visible {
			continue
		}
		terminal := false
		for _, kind := range []delivery.Kind{delivery.KindRejected, delivery.KindExpiredUnconfirmed, delivery.KindDeliveryUnknown, delivery.KindLostAtSender, delivery.KindLostAtTarget, delivery.KindAcceptedUnconfirmed} {
			if _, ok := d.notices[feedbackRef{obligation.message, obligation.recipient, kind}]; ok {
				terminal = true
				break
			}
		}
		if !terminal {
			return fmt.Errorf("accepted message %s to %s has no visible body or corresponding loss feedback", obligation.message, obligation.recipient)
		}
	}
	for _, ref := range d.required {
		if _, ok := d.notices[ref]; !ok {
			return fmt.Errorf("message %s to %s lacks real %s feedback", ref.message, ref.recipient, ref.kind)
		}
	}
	return nil
}
