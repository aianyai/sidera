package adapter

import (
	"context"
	"fmt"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/card"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/lane"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Texts the answer flow writes where Hermes shows them. None holds remote text.
const (
	summaryReplyFailed = "Sidera could not send this reply; the reason is in the result"
	commentReplyLapsed = "The permission to reply to the original message had expired, so the answer was sent as a plain message."
)

// HandleReplyCard answers a request: the card names a request card the adapter made
// of a message, and its task goes back to the sender of that message, from the
// endpoint that received it. The card is not claimed. It stays ready until it is
// completed or blocked, which Hermes allows from ready, so every step of the flow is
// resumed by the poll listing the card again and the record of how far it got;
// a claim would need a clock of its own to let go of a card the flow left halfway.
func (in *inbound) HandleReplyCard(ctx context.Context, c kanban.Card) {
	in.mu.Lock()
	defer in.mu.Unlock()
	d := in.env.State.View()
	if r := d.Replies[c.ID]; r != nil && r.Phase == state.PhaseFinished {
		return
	}
	req, inv := in.checkReply(d, c)
	if inv != nil {
		in.fail(ctx, c.ID, lane.ReasonInvalidCard, inv.Reason)
		return
	}
	in.answer(ctx, c.ID, req.Task, *d.Requests[req.ReplyTo])
}

// checkReply validates a reply card: the creator list and the envelope as for any
// card, and the request it names must be one the adapter made of a message.
func (in *inbound) checkReply(d state.Data, c kanban.Card) (card.Request, *card.InvalidError) {
	env, err := card.Decode(c.Body)
	if err != nil {
		// Decode returns nothing else.
		return card.Request{}, err.(*card.InvalidError)
	}
	req, inv := in.env.validate(env, c.CreatedBy, false)
	if inv != nil {
		return card.Request{}, inv
	}
	if _, ok := d.Requests[req.ReplyTo]; !ok {
		return card.Request{}, &card.InvalidError{Reason: fmt.Sprintf(
			"reply_to %q is not a request card this node made of a message, or its record has expired", req.ReplyTo)}
	}
	return req, nil
}

// answer sends the text to the sender of the request and completes the card. The
// attempt is written down before the send: the idempotency key is built from its
// number, and recovery decides from its start whether the same message may still be
// sent. A card listed again goes on from the record: sent goes straight to the
// completion, an attempt in progress is sent again under the same key, a card its
// creator unblocked is a new attempt.
func (in *inbound) answer(ctx context.Context, id, text string, orig state.Request) {
	attempt, plain := 1, false
	if r := in.env.State.View().Replies[id]; r != nil {
		switch r.Phase {
		case state.PhaseSent:
			in.complete(ctx, id, orig)
			return
		case state.PhaseAttempting:
			if in.env.Now().Sub(r.AttemptAt) > adapterproto.SentKeyWindow {
				in.fail(ctx, id, lane.ReasonOutcomeUnknown, "The adapter cannot tell whether the answer was sent; it was too long ago to send it again.")
				return
			}
			attempt, plain = r.Attempt, r.Plain
		case state.PhaseBlocked:
			attempt = r.Attempt + 1
		}
	}
	from, ok := in.env.EndpointRef(orig.Endpoint)
	if !ok {
		in.fail(ctx, id, lane.ReasonDeliveryFailed, "The endpoint that received the request is closed, so the answer cannot be sent from it.")
		return
	}
	for {
		if !in.begin(id, attempt, plain) {
			return
		}
		out := Outgoing{From: from, To: adapterproto.Address(orig.Sender), Body: text,
			ReplyTo: adapterproto.MessageID(orig.MessageID), IdempotencyKey: lane.IdempotencyKey(id, attempt)}
		if plain {
			out.ReplyTo = ""
			out.Body = fmt.Sprintf("Reply to your message %s:\n\n%s", orig.MessageID, text)
		}
		mid, err := in.env.Send(ctx, out)
		switch {
		case err == nil:
			if err := in.update(id, func(r *state.Reply) { r.Sent(orig.CardID, string(mid), in.env.Now()) }); err != nil {
				in.env.Log.Error("recording a sent answer", "card", id, "error", err)
				return
			}
			in.complete(ctx, id, orig)
			return
		case !plain && refusalReason(err) == adapterproto.SendRefusedMessageUnknown:
			// The permission to reply ended with the node's record of the message: the node
			// forgets a received message a few hours after it.
			in.comment(ctx, id, commentReplyLapsed)
			attempt, plain = attempt+1, true
		case refused(err):
			in.fail(ctx, id, lane.ReasonDeliveryFailed, clip(fmt.Sprintf("The answer was refused: %v", err)))
			return
		default:
			// Not known to have failed: the poll lists the card again and the same
			// attempt is sent again with the same key, within the window.
			in.env.Log.Warn("sending an answer; it is tried again", "card", id, "error", err)
			return
		}
	}
}

// begin writes the start of the attempt unless it is the attempt already recorded as
// under way, whose start must stay. It reports whether the record is written.
func (in *inbound) begin(id string, attempt int, plain bool) bool {
	if r := in.env.State.View().Replies[id]; r != nil && r.Phase == state.PhaseAttempting && r.Attempt == attempt && r.Plain == plain {
		return true
	}
	if err := in.update(id, func(r *state.Reply) { r.BeginAttempt(attempt, plain, in.env.Now()) }); err != nil {
		in.env.Log.Error("recording an attempt to answer; nothing is sent", "card", id, "error", err)
		return false
	}
	return true
}

// complete ends the card after its answer was sent. The summary's first line is the
// adapter's. A completion that fails for a reason that may pass is tried again when
// the card is listed again; the record says the answer is sent, so it is not sent
// twice. One Hermes refuses for good (the card moved on) ends the record.
func (in *inbound) complete(ctx context.Context, id string, orig state.Request) {
	r := in.env.State.View().Replies[id]
	err := in.env.Board.Complete(ctx, id, kanban.Completion{
		Summary:  "Sent over Sidera to " + in.who(adapterproto.Address(orig.Sender)),
		Metadata: map[string]any{metaMessageID: r.MessageID, metaAttempt: r.Attempt},
	})
	switch {
	case err == nil:
	case isPermanent(err):
		in.env.Log.Warn("an answered card cannot be completed", "card", id, "error", err)
	default:
		in.env.Log.Warn("completing an answered card; it is tried again", "card", id, "error", err)
		return
	}
	if err := in.update(id, func(r *state.Reply) { r.Finish(in.env.Now()) }); err != nil {
		in.env.Log.Error("recording a finished answer", "card", id, "error", err)
	}
}

// fail ends what the card was doing, once per cause: the first time it is blocked so
// that its creator decides, the second time it is completed with the reason, since
// Hermes sends a card blocked twice for one kind to a state in which nobody is woken
// (lane.OnFailure).
func (in *inbound) fail(ctx context.Context, id string, reason lane.Reason, text string) {
	var blocks map[state.Category]int
	if r := in.env.State.View().Replies[id]; r != nil {
		blocks = r.Blocks
	}
	cat := reason.Category()
	if lane.OnFailure(blocks, reason) == lane.VerdictBlock {
		err := in.env.Board.Block(ctx, id, lane.BlockKindOf(cat), clip(text))
		if err != nil {
			in.env.Log.Warn("blocking a reply card; it is looked at again if it is still ready", "card", id, "error", err)
			return
		}
		if err := in.update(id, func(r *state.Reply) { r.Block(cat) }); err != nil {
			in.env.Log.Error("recording a blocked reply card", "card", id, "error", err)
		}
		return
	}
	summary := summaryReplyFailed
	if cat == state.CategoryInvalidCard {
		summary = summaryInvalid
	}
	err := in.env.Board.Complete(ctx, id, kanban.Completion{Summary: summary, Result: clip(text)})
	if err != nil && !isPermanent(err) {
		in.env.Log.Warn("completing a reply card; it is tried again", "card", id, "error", err)
		return
	}
	if err := in.update(id, func(r *state.Reply) { r.Finish(in.env.Now()) }); err != nil {
		in.env.Log.Error("recording a finished reply card", "card", id, "error", err)
	}
}

// update applies fn to the record of the card's answer, making it first if there is
// none.
func (in *inbound) update(id string, fn func(r *state.Reply)) error {
	return in.env.State.Update(func(d *state.Data) error {
		r, ok := d.Replies[id]
		if !ok {
			r = &state.Reply{CardID: id}
			d.Replies[id] = r
		}
		fn(r)
		return nil
	})
}

// comment appends a comment to the card; a failure is logged and not retried.
func (in *inbound) comment(ctx context.Context, id, text string) {
	if err := in.env.Board.Comment(ctx, id, text); err != nil {
		in.env.Log.Warn("commenting on a card", "card", id, "error", err)
	}
}
