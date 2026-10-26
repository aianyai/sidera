package engine

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
)

// DefaultContentType is the type of a body whose sender names none, which is what
// the front ends of the Agent interface say a message without one is.
const DefaultContentType = "text/plain"

// SendRequest is a message a local endpoint asks the node to send.
type SendRequest struct {
	// To are the recipients; at least one and at most envelope.MaxRecipients.
	To []ident.Address
	// Body is the content, not empty and at most envelope.MaxBody bytes.
	Body []byte
	// ContentType says what the body is; it is not empty.
	ContentType string
	// Thread is the sender's own grouping; empty for none.
	Thread string
	// DiscloseRecipients lets each recipient see the others. With one recipient it
	// shows nothing and is ignored.
	DiscloseRecipients bool
	// AcceptBefore is how long the message may wait to be delivered; zero is
	// delivery.DefaultAcceptBefore.
	AcceptBefore time.Duration
	// IdempotencyKey, when set, makes a repeat of the same request return the
	// message the first one made instead of making another.
	IdempotencyKey string
}

// ReplyRequest answers a message the caller's endpoint received. The recipient is
// the one the node's record of that message names, never one the caller gives.
type ReplyRequest struct {
	Message        delivery.MessageID
	Body           []byte
	ContentType    string
	Thread         string
	IdempotencyKey string
}

// SendResult names the message that was accepted. It is returned once the node
// has the message on disk; what the recipients' nodes make of it is learned from
// the state of its deliveries and from the feedback.
type SendResult struct {
	Message delivery.MessageID
	To      []ident.Address
	// Replayed is true when the idempotency key found the message it names.
	Replayed bool
}

// outgoing is a message to accept, as the caller's request and the node's own
// record of what it answers have made it.
type outgoing struct {
	to       []ident.Address
	replyTo  delivery.MessageID
	body     []byte
	ctype    string
	thread   string
	accept   time.Duration
	idemKey  string
	disclose bool
	// kind is what the envelope asks for; the zero value is a message. A control
	// signal has one recipient and no reply to, and preset is the parameter of a
	// session creation.
	kind   envelope.Kind
	preset string
}

type cmdSend struct {
	call[SendResult]
	caller Caller
	req    SendRequest
	// reply is the request when the command answers a message.
	reply   ReplyRequest
	isReply bool
}

func (c *cmdSend) gate() gate { return gateWrite }
func (c *cmdSend) run(e *Engine, now time.Time) {
	var res SendResult
	var err error
	if c.isReply {
		res, err = e.reply(now, c.caller, c.reply)
	} else {
		res, err = e.send(now, c.caller, c.req)
	}
	c.done(res, e.classify(err))
}

// Send accepts a message from the endpoint the caller is and returns when it is on
// disk: the body, then the record of the message with one delivery for each
// recipient. Sending it and learning what became of it go on after this returns.
// A request the node cannot take in now (the queue is full, the disk has no room,
// the wall clock cannot be trusted) is refused, and nothing is recorded.
func (e *Engine) Send(ctx context.Context, c Caller, r SendRequest) (SendResult, error) {
	cmd := &cmdSend{call: newCall[SendResult](), caller: c, req: r}
	return roundTrip(ctx, e, submitExternal, cmd, cmd.call)
}

// Reply accepts an answer to a message the caller's endpoint received.
func (e *Engine) Reply(ctx context.Context, c Caller, r ReplyRequest) (SendResult, error) {
	cmd := &cmdSend{call: newCall[SendResult](), caller: c, reply: r, isReply: true}
	return roundTrip(ctx, e, submitExternal, cmd, cmd.call)
}

func (e *Engine) send(now time.Time, c Caller, r SendRequest) (SendResult, error) {
	st, err := e.view.resolve(c)
	if err != nil {
		return SendResult{}, err
	}
	return e.accept(now, st, outgoing{
		to: r.To, body: r.Body, ctype: r.ContentType, thread: r.Thread, accept: r.AcceptBefore,
		idemKey: r.IdempotencyKey, disclose: r.DiscloseRecipients,
	})
}

func (e *Engine) reply(now time.Time, c Caller, r ReplyRequest) (SendResult, error) {
	st, err := e.view.resolve(c)
	if err != nil {
		return SendResult{}, err
	}
	keys := e.heldBy(st.rec.ID, r.Message)
	if len(keys) != 1 {
		// Nothing was received under that id, or two senders used it: it is not one
		// message that can be answered.
		return SendResult{}, refuse(ReasonMessageUnknown)
	}
	if keys[0].Sender.IsSystem() {
		return SendResult{}, invalidArgument("message_id", errors.New("a notice of the node itself is not answered"))
	}
	return e.accept(now, st, outgoing{
		to: []ident.Address{{Node: keys[0].SenderNode, Endpoint: keys[0].Sender}}, replyTo: r.Message,
		body: r.Body, ctype: r.ContentType, thread: r.Thread, idemKey: r.IdempotencyKey,
	})
}

// heldBy are the target records of the messages the endpoint has under the id: the
// ones that were taken in. A message refused at intake has a record, so that the
// sender can be told, but the endpoint never had it, and an answer to it could not
// earn the permission to reply that the delivery of the message gives.
func (e *Engine) heldBy(endpoint ident.EndpointID, message delivery.MessageID) []store.TargetKey {
	var keys []store.TargetKey
	for _, k := range e.view.received[msgKey{endpoint, message}] {
		if e.view.in[k].Record.State != delivery.StateRejected {
			keys = append(keys, k)
		}
	}
	return keys
}

// accept is the intake of a message: the one place new work from a local endpoint
// enters the store. The request is made into the envelope it will be sent as, which
// is what refuses a request no node would take; a repeat of one the idempotency key
// knows is answered with the first; the capacity of the node is judged, and then the
// body and the record are written in one transaction. Memory follows the disk, and
// only then is the message given to the outbound worker.
func (e *Engine) accept(now time.Time, st *endpointState, m outgoing) (SendResult, error) {
	if e.d.Keys == nil {
		return SendResult{}, errNoKeys
	}
	if e.tr == nil {
		return SendResult{}, errNoTransport
	}
	now = now.UTC().Truncate(time.Second) // what an envelope can say
	if err := checkOutgoing(&m); err != nil {
		return SendResult{}, err
	}
	sender := st.rec.ID
	id, err := e.newMessageID()
	if err != nil {
		return SendResult{}, err
	}
	env, err := e.envelopeOf(now, sender, id, m)
	if err != nil {
		return SendResult{}, err
	}
	if prior, ok := e.view.idem[idemKey{sender, m.idemKey}]; ok && now.Before(e.view.out[prior].Idempotency.RetainUntil) {
		return e.repeat(e.view.out[prior], env)
	}
	if err := e.admitNewWork(len(m.to)); err != nil {
		return SendResult{}, err
	}
	o := e.sourceRecord(now, sender, env, m)
	if err := e.d.Store.Intake(m.body, o); err != nil {
		return SendResult{}, err
	}
	for _, to := range m.to {
		e.view.seq.Note(sender, to, env.Seq[to.Endpoint])
	}
	e.putOut(now, o)
	e.dispatchOutbound(o.Name(), now)
	return SendResult{Message: id, To: m.to}, nil
}

// checkOutgoing refuses what the envelope does not and no node would take. What
// the envelope refuses (the recipients, the body and its content type, the
// identifiers) it refuses itself, in envelopeOf.
func checkOutgoing(m *outgoing) error {
	switch {
	case m.accept < 0 || m.accept > delivery.MaxAcceptBefore:
		return invalidArgument("accept_before", fmt.Errorf("%v, want up to %v", m.accept, delivery.MaxAcceptBefore))
	}
	if m.accept == 0 {
		m.accept = delivery.DefaultAcceptBefore
	}
	return nil
}

// envelopeOf is the envelope of a message as the sender makes it, encoded to see
// that every node would take it. Its deadlines are whole seconds, because that is
// what an envelope can say and the record of the target must agree with it. A
// request the envelope refuses is refused for the field it names; one it refuses
// for another reason is a defect of the engine's own.
func (e *Engine) envelopeOf(now time.Time, sender ident.EndpointID, id delivery.MessageID, m outgoing) (envelope.Envelope, error) {
	if m.kind == envelope.KindInvalid {
		m.kind = envelope.KindMessage
	}
	acceptBefore := now.Add(m.accept).Truncate(time.Second)
	env := envelope.Envelope{
		ID: id, To: slices.Clone(m.to), MultiRecipient: len(m.to) > 1, DiscloseRecipients: m.disclose && len(m.to) > 1,
		Kind: m.kind, ReplyTo: m.replyTo, Thread: m.thread, Seq: map[ident.EndpointID]uint64{},
		SentAt: now, AcceptBefore: acceptBefore, ReportBy: delivery.ReportBy(acceptBefore), ContentType: m.ctype, Body: m.body,
		Preset: m.preset,
	}
	for _, to := range m.to {
		env.Seq[to.Endpoint] = e.view.seq.Next(sender, to)
	}
	_, err := env.Encode(envelope.DefaultLimits())
	switch {
	case err == nil:
		return env, nil
	case errors.Is(err, envelope.ErrBodyTooLarge):
		return env, &Refusal{Reason: ReasonTooLarge, Field: "body", Err: err}
	case errors.Is(err, envelope.ErrRecipients):
		return env, invalidArgument("to", err)
	case errors.Is(err, envelope.ErrBody):
		return env, invalidArgument("body", err)
	case errors.Is(err, envelope.ErrID):
		return env, invalidArgument("thread", err)
	}
	return env, fmt.Errorf("engine: the envelope of %s: %w", id, err)
}

// repeat answers a request whose idempotency key the node knows: the same request
// is the same message, any other is a conflict. The request is the same when its
// content is, as the target compares it by the request digest of the envelope made
// with the deadlines the first request had (the deadline a repeat gets is its own
// and says nothing about the content), and when it is for the same recipients in
// the same order, which the digest does not cover unless they are disclosed.
func (e *Engine) repeat(prior store.OutRecord, env envelope.Envelope) (SendResult, error) {
	env.AcceptBefore, env.ReportBy = prior.Outbound.AcceptBefore, prior.Envelope.ReportBy
	if env.Digest() != prior.BodyDigest || !slices.Equal(env.To, prior.Key.Recipients) {
		return SendResult{}, refuse(ReasonIdempotencyConflict)
	}
	return SendResult{Message: prior.Outbound.Message, To: prior.Key.Recipients, Replayed: true}, nil
}

// sourceRecord is the node's record of a message it accepted: the envelope's
// fields it needs to send it again, one delivery for each recipient, the body
// reference and the digest of the request.
func (e *Engine) sourceRecord(now time.Time, sender ident.EndpointID, env envelope.Envelope, m outgoing) store.OutRecord {
	o := store.OutRecord{
		Key: store.OutKey{Sender: sender, Message: env.ID, Recipients: env.To, Control: env.Kind.IsControl()},
		Outbound: envelope.Outbound{
			Message: env.ID, From: sender, Kind: env.Kind, BodyNonEmpty: len(env.Body) > 0, AcceptBefore: env.AcceptBefore,
		},
		Envelope: store.OutEnvelope{
			To: env.To, DiscloseRecipients: env.DiscloseRecipients, ReplyTo: env.ReplyTo, Thread: env.Thread, Seq: env.Seq, SentAt: env.SentAt,
			ReportBy: env.ReportBy, ContentType: env.ContentType, Preset: env.Preset,
		},
		BodyDigest: env.Digest(),
	}
	if len(env.Body) > 0 {
		ref := store.BodyRefOf(env.Body)
		o.Body = &ref
	}
	for _, to := range env.To {
		o.Outbound.Recipients = append(o.Outbound.Recipients, envelope.Tracked{Address: to, Delivery: delivery.NewSourceRecord(now, env.AcceptBefore)})
	}
	if m.idemKey != "" {
		o.Idempotency = &store.Idempotency{Key: m.idemKey, RetainUntil: delivery.IdempotencyKeyRetainUntil(env.ReportBy)}
	}
	return o
}

// newMessageID draws a message identifier: sixteen random bytes as lower case
// hex, from the engine's own source.
func (e *Engine) newMessageID() (delivery.MessageID, error) {
	var b [16]byte
	if _, err := io.ReadFull(e.randomReader(), b[:]); err != nil {
		return "", err
	}
	return delivery.MessageID(hex.EncodeToString(b[:])), nil
}
