package adapter

import (
	"context"
	"log/slog"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Message is one inbound message of a direct endpoint, whole: a message that the
// node handed out in parts reaches a handler only once every part is in hand.
type Message struct {
	ID   adapterproto.MessageID
	From adapterproto.Address
	// To is the endpoint that received it. A message to the instance endpoint is a
	// new request; one to a conversation endpoint continues that conversation or
	// answers a card.
	To   Endpoint
	Body string
	// PossiblyDuplicate is set when any part was handed out before and not
	// acknowledged.
	PossiblyDuplicate bool
}

// ToInstance reports whether the message is addressed to Hermes itself.
func (m Message) ToInstance() bool { return m.To.Key == InstanceKey }

// Verdict is what a MessageHandler says about a message. It decides whether the
// adapter acknowledges the message, and acknowledging is the one thing that tells
// the sender the message arrived.
type Verdict int

const (
	// Written: everything the message is for is on the board. The adapter records
	// the message id as processed and acknowledges the message.
	Written Verdict = iota + 1
	// Retry: Hermes failed in a way that may pass (it cannot be run, the database is
	// locked). The message is not acknowledged, and receiving pauses with a growing
	// pause before the next attempt; the message stays with Sidera meanwhile.
	Retry
	// Skip: the message cannot be written and nothing about it will change, a
	// failure the handler could not route anywhere. It is not acknowledged and
	// receiving goes on with the next message. Its lease expires and, after the
	// redeliveries, the sender is told that the delivery's outcome is unknown.
	// Acknowledging it instead would tell the sender it arrived.
	Skip
)

// MessageHandler turns an inbound message into what it is for. The returned error
// is the cause the adapter logs for Retry and Skip; it does not change the verdict.
// A handler may be called again with a message it has seen (a lease that expired
// after the card was written), so its effects must be repeatable.
type MessageHandler interface {
	HandleMessage(ctx context.Context, m Message) (Verdict, error)
}

// CardHandler takes a card that the poll of the board found ready and assigned to
// the lane. The poll lists the same card again until it is claimed, so the handler
// must treat a card it has already taken as taken. It reports nothing: whatever it
// could not do, the next poll presents the card again.
type CardHandler interface {
	HandleCard(ctx context.Context, c kanban.Card)
}

// TimerHandler does the work that is due by the clock rather than by an event:
// recovery after a start, waits that ran out, sends that were interrupted,
// remote sessions to close.
type TimerHandler interface {
	// Recover runs once after start, before the first Tick, and again after a
	// failure. It reconciles the records of the adapter with the board.
	Recover(ctx context.Context) error
	// Tick does what is due now. It is called every timing.tick while Hermes can be
	// used.
	Tick(ctx context.Context)
}

// Handlers are the flows the loops feed. Timers may be nil: no timer loop runs.
type Handlers struct {
	Messages MessageHandler
	Cards    CardHandler
	Timers   TimerHandler
}

// Flows builds the handlers once the adapter has what they work with.
type Flows func(env *Env) Handlers

// LogOnly is the flows of an adapter that does nothing with what it reads: it
// logs each message and card and takes no action. A message is left
// unacknowledged (Skip), so nothing is told to its sender as delivered that was not.
func LogOnly(env *Env) Handlers {
	return Handlers{Messages: logOnly{env.Log}, Cards: logOnly{env.Log}}
}

type logOnly struct{ log *slog.Logger }

func (l logOnly) HandleMessage(_ context.Context, m Message) (Verdict, error) {
	l.log.Info("an inbound message is not handled", "id", m.ID, "from", m.From, "endpoint", m.To.Key)
	return Skip, nil
}

func (l logOnly) HandleCard(_ context.Context, c kanban.Card) {
	l.log.Info("a ready card is not handled", "card", c.ID, "assignee", c.Assignee)
}
