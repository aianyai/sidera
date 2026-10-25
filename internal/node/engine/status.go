package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

type cmdStatus struct {
	call[adapterproto.MessageStatusResult]
	caller  Caller
	message adapterproto.MessageID
}

func (c *cmdStatus) gate() gate { return gateRead }
func (c *cmdStatus) run(e *Engine, _ time.Time) {
	res, err := e.messageStatus(c.caller, c.message)
	c.done(res, e.classify(err))
}

// MessageStatus is the state of each delivery of a message the caller's endpoint
// sent, as the node holds it: the sender's record, which the reports of the
// targets bring up to date. A message the endpoint did not send is not known to
// it, whoever did, so one endpoint cannot learn what became of another's. The
// reply of a control signal is not part of the result yet.
func (e *Engine) MessageStatus(ctx context.Context, c Caller, id adapterproto.MessageID) (adapterproto.MessageStatusResult, error) {
	cmd := &cmdStatus{call: newCall[adapterproto.MessageStatusResult](), caller: c, message: id}
	return roundTrip(ctx, e, submitExternal, cmd, cmd.call)
}

func (e *Engine) messageStatus(c Caller, id adapterproto.MessageID) (adapterproto.MessageStatusResult, error) {
	st, err := e.view.resolve(c)
	if err != nil {
		return adapterproto.MessageStatusResult{}, err
	}
	name, ok := e.view.byMessage[msgKey{st.rec.ID, delivery.MessageID(id)}]
	if !ok {
		return adapterproto.MessageStatusResult{}, refuse(ReasonMessageUnknown)
	}
	recipients, err := statusRecipients(e.view.out[name])
	if err != nil {
		return adapterproto.MessageStatusResult{}, err
	}
	return adapterproto.MessageStatusResult{Recipients: recipients}, nil
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
