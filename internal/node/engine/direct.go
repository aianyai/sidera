package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

type cmdSendDirect struct {
	call[SendResult]
	instance string
	params   adapterproto.SendParams
}

func (c *cmdSendDirect) gate() gate { return gateWrite }
func (c *cmdSendDirect) run(e *Engine, now time.Time) {
	res, err := e.sendDirect(now, c.instance, c.params)
	c.done(res, e.classify(err))
}

// SendDirect accepts the complete adapter request in one loop command. Endpoint
// ownership, reply routing and acceptance are judged against the same view.
func (e *Engine) SendDirect(ctx context.Context, instance string, p adapterproto.SendParams) (SendResult, error) {
	cmd := &cmdSendDirect{call: newCall[SendResult](), instance: instance, params: p}
	return roundTrip(ctx, e, submitExternal, cmd, cmd.call)
}

func (e *Engine) sendDirect(now time.Time, instance string, p adapterproto.SendParams) (SendResult, error) {
	st, err := e.view.resolve(ByAdapter(instance, p.Endpoint))
	if err != nil {
		return SendResult{}, err
	}
	if err := p.Validate(); err != nil {
		return SendResult{}, invalidArgument("request", err)
	}
	m := outgoing{ctype: p.ContentType, thread: p.Thread,
		accept: time.Duration(p.AcceptBeforeS) * time.Second, idemKey: p.IdempotencyKey, disclose: p.DiscloseRecipients}
	if p.Body != "" {
		m.body = []byte(p.Body)
	}
	for i, raw := range p.To {
		to, err := ident.ParseAddress(string(raw))
		if err != nil {
			return SendResult{}, invalidArgument(fmt.Sprintf("to[%d]", i), err)
		}
		m.to = append(m.to, to)
	}
	if p.Control != nil {
		if p.ReplyTo != "" {
			return SendResult{}, invalidArgument("reply_to", errors.New("a control signal cannot be a reply"))
		}
		r := ControlRequest{Signal: p.Control.Signal, Body: m.body, ContentType: m.ctype}
		if p.Control.Params != nil {
			r.Preset = p.Control.Params.Preset
		}
		m.kind, err = controlKind(r)
		if err != nil {
			return SendResult{}, err
		}
		m.preset = r.Preset
	} else if p.ReplyTo != "" {
		to, err := e.replySender(st.rec.ID, delivery.MessageID(p.ReplyTo))
		if err != nil {
			return SendResult{}, err
		}
		if len(m.to) != 1 || m.to[0] != to {
			return SendResult{}, invalidArgument("to", errors.New("a reply goes only to the original sender"))
		}
		m.replyTo = delivery.MessageID(p.ReplyTo)
	}
	return e.accept(now, st, m)
}
