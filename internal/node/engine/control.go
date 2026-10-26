package engine

import (
	"context"
	"errors"
	"time"

	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// ControlRequest is a control signal a local endpoint asks the node to send. A
// control signal has one recipient, the endpoint it acts on.
type ControlRequest struct {
	To     ident.Address
	Signal adapterproto.ControlSignal
	// Preset is the preset a session creation starts; it is required for
	// session.create and refused for every other signal.
	Preset string
	// Body is the initial message of a session creation, delivered to the session
	// the creation makes, and ContentType says what it is (DefaultContentType when
	// empty). Both are refused for every other signal, and a content type without a
	// body.
	Body        []byte
	ContentType string
	// IdempotencyKey, when set, makes a repeat of the same request return the
	// signal the first one made instead of making another.
	IdempotencyKey string
	// AcceptBefore is how long the signal may wait to reach its target; zero is
	// delivery.DefaultAcceptBefore.
	AcceptBefore time.Duration
}

type cmdControl struct {
	call[SendResult]
	caller Caller
	req    ControlRequest
}

func (c *cmdControl) gate() gate { return gateWrite }
func (c *cmdControl) run(e *Engine, now time.Time) {
	res, err := e.control(now, c.caller, c.req)
	c.done(res, e.classify(err))
}

// Control accepts a control signal from the endpoint the caller is, as Send accepts
// a message: the capacity of the node is judged, and the record of the signal is
// on disk before the call returns. What the target made of it is learned from the
// state of its delivery and from the control reply, which the status of the
// message carries once there is one. A creation may carry an initial message, which
// is delivered to the session it makes.
func (e *Engine) Control(ctx context.Context, c Caller, r ControlRequest) (SendResult, error) {
	cmd := &cmdControl{call: newCall[SendResult](), caller: c, req: r}
	return roundTrip(ctx, e, submitExternal, cmd, cmd.call)
}

func (e *Engine) control(now time.Time, c Caller, r ControlRequest) (SendResult, error) {
	st, err := e.view.resolve(c)
	if err != nil {
		return SendResult{}, err
	}
	kind, err := controlKind(r)
	if err != nil {
		return SendResult{}, err
	}
	m := outgoing{to: []ident.Address{r.To}, kind: kind, preset: r.Preset, accept: r.AcceptBefore, idemKey: r.IdempotencyKey}
	if len(r.Body) > 0 { // an empty body is none, as the protocol says of a creation
		m.body, m.ctype = r.Body, contentTypeOf(r)
	}
	return e.accept(now, st, m)
}

// controlKind is the kind of the envelope a request asks for, or why the request
// is not one the node sends. The rule between a signal and its parameters is the
// adapter protocol's own (adapterproto.Control.Validate), not written again here.
func controlKind(r ControlRequest) (envelope.Kind, error) {
	ctl := adapterproto.Control{Signal: r.Signal}
	if r.Preset != "" {
		ctl.Params = &adapterproto.ControlParams{Preset: r.Preset}
	}
	if err := ctl.Validate(); err != nil {
		return envelope.KindInvalid, invalidArgument("signal", err)
	}
	kind, ok := envelope.KindOfSignal(r.Signal)
	if !ok {
		return envelope.KindInvalid, invalidArgument("signal", errors.New("not a control signal"))
	}
	switch {
	case kind != envelope.KindSessionCreate && len(r.Body) > 0:
		return envelope.KindInvalid, invalidArgument("body", errors.New("only a session creation carries a message"))
	case len(r.Body) == 0 && r.ContentType != "":
		return envelope.KindInvalid, invalidArgument("content_type", errors.New("a content type belongs to a message that has a body"))
	}
	return kind, nil
}

// contentTypeOf is the content type of the initial message of a request: the one it
// names, DefaultContentType for a message that names none, and none for a request
// that carries no message.
func contentTypeOf(r ControlRequest) string {
	switch {
	case len(r.Body) == 0:
		return ""
	case r.ContentType == "":
		return DefaultContentType
	}
	return r.ContentType
}
