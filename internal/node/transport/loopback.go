package transport

import (
	"context"
	"fmt"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
)

// Loopback is the transport of a single node: it carries what leaves the
// sender's side to the receiving side of the same node, through the same two
// calls of the [Handler] that a remote transport would use, and makes every
// request it hands over a local origin. A call to any other node is
// unreachable.
type Loopback struct {
	self  ident.NodeID
	h     Handler
	knobs *Knobs
}

var _ Sender = (*Loopback)(nil)

// NewLoopback returns the loopback of node self, handing what it carries to h.
// A nil knobs means no faults.
func NewLoopback(self ident.NodeID, h Handler, knobs *Knobs) *Loopback {
	if knobs == nil {
		knobs = NewKnobs(nil)
	}
	return &Loopback{self: self, h: h, knobs: knobs}
}

// Send hands the envelope to the handler as a request from the local endpoint
// sender and returns the handler's receipt, unless a knob says otherwise.
func (l *Loopback) Send(ctx context.Context, target ident.NodeID, sender ident.EndpointID, data []byte) (Receipt, error) {
	if err := l.knobs.wait(ctx, target); err != nil {
		return Receipt{}, err
	}
	if err := l.reach(target); err != nil {
		return Receipt{}, err
	}
	var msg delivery.MessageID
	if l.knobs.watchesMessages() {
		if e, err := envelope.Decode(data, envelope.DefaultLimits()); err == nil {
			msg = e.ID
		}
	}
	rc, err := l.h.Inbound(ctx, localOrigin(l.self), sender, data)
	if err != nil {
		return Receipt{}, err
	}
	if msg != "" && l.knobs.takeMutate(msg) {
		l.replay(ctx, msg, sender, data)
	}
	if l.knobs.dropsReceipt(target) {
		return Receipt{}, fmt.Errorf("%w: the receipt was dropped", ErrTimeout)
	}
	if msg != "" && l.knobs.holds(target, msg) {
		return Receipt{}, nil
	}
	return rc, nil
}

// Pull asks the handler for the reports of a message and returns its answer,
// unless a knob says otherwise.
func (l *Loopback) Pull(ctx context.Context, target ident.NodeID, req PullRequest) (PullAnswer, error) {
	if err := l.knobs.wait(ctx, target); err != nil {
		return PullAnswer{}, err
	}
	if err := l.reach(target); err != nil {
		return PullAnswer{}, err
	}
	if l.knobs.holds(target, req.Message) {
		return PullAnswer{}, nil
	}
	return l.h.Pull(ctx, localOrigin(l.self), req)
}

// reach says whether a call to target can go on: not when a knob makes it
// unreachable, and not when it is another node, which a loopback does not know.
func (l *Loopback) reach(target ident.NodeID) error {
	if l.knobs.refuses(target) {
		return fmt.Errorf("%w: a knob refuses it", ErrUnreachable)
	}
	if target != l.self {
		return fmt.Errorf("%w: the loopback reaches only its own node", ErrUnreachable)
	}
	return nil
}

// replay delivers the envelope of msg once more, with other content, and keeps
// the handler's answer where the test that asked for it can read it. An envelope
// that cannot be rebuilt is kept as the error and is not counted as a hit.
func (l *Loopback) replay(ctx context.Context, msg delivery.MessageID, sender ident.EndpointID, data []byte) {
	changed, err := changedContent(data)
	if err != nil {
		l.knobs.keepReplay(l.self, msg, Replay{Err: fmt.Errorf("transport: cannot mutate %q: %w", msg, err)}, false)
		return
	}
	rc, err := l.h.Inbound(ctx, localOrigin(l.self), sender, changed)
	l.knobs.keepReplay(l.self, msg, Replay{Receipt: rc, Err: err}, true)
}

// changedContent returns the envelope with its body changed, or, when it has
// none, its thread: the two things the request digest covers that every kind of
// envelope may differ in.
func changedContent(data []byte) ([]byte, error) {
	e, err := envelope.Decode(data, envelope.DefaultLimits())
	if err != nil {
		return nil, err
	}
	if len(e.Body) > 0 {
		e.Body = append([]byte(nil), e.Body...)
		e.Body[0] ^= 1
	} else {
		e.Thread += "m"
	}
	return e.Encode(envelope.DefaultLimits())
}
