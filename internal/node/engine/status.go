package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

type cmdStatus struct {
	call[adapterproto.MessageStatusResult]
	caller  Caller
	message adapterproto.MessageID
}

func (c *cmdStatus) gate() gate { return gateRead }
func (c *cmdStatus) run(e *Engine, now time.Time) {
	res, err := e.messageStatus(now, c.caller, c.message)
	c.done(res, e.classify(err))
}

// MessageStatus is the state of each delivery of a message the caller's endpoint
// sent, as the node holds it: the sender's record, which the reports of the
// targets bring up to date. A message the endpoint did not send is not known to
// it, whoever did, so one endpoint cannot learn what became of another's. The
// reply of a control signal is part of the result once the node has one: it is
// read from the record the node verified when it took the reply in.
func (e *Engine) MessageStatus(ctx context.Context, c Caller, id adapterproto.MessageID) (adapterproto.MessageStatusResult, error) {
	cmd := &cmdStatus{call: newCall[adapterproto.MessageStatusResult](), caller: c, message: id}
	return roundTrip(ctx, e, submitExternal, cmd, cmd.call)
}

func (e *Engine) messageStatus(now time.Time, c Caller, id adapterproto.MessageID) (adapterproto.MessageStatusResult, error) {
	st, err := e.view.resolve(c)
	if err != nil {
		return adapterproto.MessageStatusResult{}, err
	}
	name, ok := e.view.byMessage[msgKey{st.rec.ID, delivery.MessageID(id)}]
	if !ok {
		return adapterproto.MessageStatusResult{}, refuse(ReasonMessageUnknown)
	}
	o := e.view.out[name]
	recipients, err := statusRecipients(o)
	if err != nil {
		return adapterproto.MessageStatusResult{}, err
	}
	res := adapterproto.MessageStatusResult{Recipients: recipients}
	if res.ControlReply, err = e.statusControlReply(now, o); err != nil {
		return adapterproto.MessageStatusResult{}, err
	}
	return res, nil
}

// statusControlReply is the reply of the control signal of a source record in the
// values of the adapter protocol, nil while the node has none. The record was
// verified when it was taken in, and is verified again here: what an agent is told
// is never read from bytes nobody checked.
func (e *Engine) statusControlReply(now time.Time, o store.OutRecord) (*adapterproto.ControlReply, error) {
	if len(o.ControlReply) == 0 || o.Lost != nil {
		return nil, nil
	}
	rec, err := record.Decode(o.ControlReply, record.DefaultMaxInput)
	if err != nil {
		return nil, fmt.Errorf("engine: the control reply stored for %s: %w", o.Key.Message, err)
	}
	sr, err := e.verifyControlReply(now, rec, o.Outbound.Recipients[0].Address.Node)
	if err != nil {
		return nil, fmt.Errorf("engine: the control reply stored for %s: %w", o.Key.Message, err)
	}
	reply := sr.Reply().Result.AdapterForm()
	return &reply, nil
}

// statusRecipients are the recipients of a source record with the state of the
// delivery to each, in the order the sender gave them. A record that replaces one
// that was lost has the deliveries on their own, in the order of the key.
func statusRecipients(o store.OutRecord) ([]adapterproto.RecipientStatus, error) {
	var out []adapterproto.RecipientStatus
	add := func(to string, d delivery.Record) error {
		state, err := wireState(d.State)
		if err != nil {
			return err
		}
		s := adapterproto.RecipientStatus{To: adapterproto.Address(to), State: state}
		if d.State == delivery.StateRejected {
			if s.Reason, err = wireReject(d.Reject); err != nil {
				return err
			}
		}
		out = append(out, s)
		return nil
	}
	if o.Lost != nil {
		if len(o.Lost.Deliveries) != len(o.Key.Recipients) {
			return nil, fmt.Errorf("engine: the record of %s holds %d deliveries for %d recipients", o.Key.Message, len(o.Lost.Deliveries), len(o.Key.Recipients))
		}
		for i, to := range o.Key.Recipients {
			if err := add(to.String(), o.Lost.Deliveries[i]); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	for _, t := range o.Outbound.Recipients {
		if err := add(t.Address.String(), t.Delivery); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// wireState and wireReject name a state and a reason of the delivery rules as the
// Agent interface does: the names are the same, and one that the interface does not
// know is a defect of the engine, not something to say to a client.
func wireState(s delivery.State) (adapterproto.DeliveryState, error) {
	w := adapterproto.DeliveryState(s.String())
	if !w.Valid() {
		return "", fmt.Errorf("engine: the delivery state %s has no name on the wire", s)
	}
	return w, nil
}

func wireReject(r delivery.RejectReason) (adapterproto.RejectReason, error) {
	w := adapterproto.RejectReason(r.String())
	if !w.Valid() {
		return "", fmt.Errorf("engine: the rejection reason %s has no name on the wire", r)
	}
	return w, nil
}
