package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/lane"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// attachDir is the directory below the state directory where the text of an
// attachment is written for Hermes to copy; the client removes the file after.
const attachDir = "attach"

// Keys of the metadata a reply leaves on the completed card. The message id is how a
// replayed message is recognised as written already.
const (
	metaMessageID = "sidera_message_id"
	metaFrom      = "sidera_from"
	metaAttempt   = "sidera_attempt"
)

// HandleMessage takes a message of a conversation or orphan endpoint: a reply is
// written to its card, anything else is the inbound half's. A notice of the node is
// routed before anything else: it is no reply to a card and no request, whoever the
// card of the endpoint is waiting for.
func (f *laneFlows) HandleMessage(ctx context.Context, m Message) (Verdict, error) {
	if m.Feedback {
		return f.notice(ctx, m)
	}
	if m.ToInstance() {
		return f.inbound.HandleInbound(ctx, m)
	}
	v, inbound, err := f.reply(ctx, m)
	if inbound {
		return f.inbound.HandleInbound(ctx, m)
	}
	return v, err
}

// notice explains the node's notice about an endpoint: first by ending the cards
// that sent from it and whose message the node says did not arrive, then, when none
// ended, by the inbound half. A card ended here is blocked or completed on the board
// before the notice is acknowledged: a failure of Hermes that may pass is a Retry.
//
// A card that the tick already ended for the same failure explains the notice as
// well: the tick reads the status of a message every ten seconds and usually comes
// first, and a notice that finds nothing in flight would otherwise be relayed to the
// conversation as a card of its own, waking it a second time about the failure the
// blocked card already told it. Such a failure explains the first notice handled
// after it (state.Endpoint.FailedAt, NoticedAt), and only while the failure is recent
// (noticeFollowsWithin).
func (f *laneFlows) notice(ctx context.Context, m Message) (Verdict, error) {
	f.mu.Lock()
	ended, err := f.settleEndpoint(ctx, m.To.Key)
	explained := ended || f.failureNotNoticed(m.To.Key)
	f.mu.Unlock()
	if err != nil {
		return Retry, err
	}
	v, err := f.inbound.HandleNotice(ctx, m, explained)
	if v == Written {
		f.mu.Lock()
		f.noteNotice(m.To.Key)
		f.mu.Unlock()
	}
	return v, err
}

// noteDeliveryFailure records that a card of the endpoint ended because the node
// said its message did not arrive. The caller holds mu.
func (f *laneFlows) noteDeliveryFailure(endpoint string) {
	f.noteEndpoint(endpoint, func(e *state.Endpoint) { e.FailedAt = f.env.Now() })
}

// noteNotice records that a notice about the endpoint was handled. The caller holds mu.
func (f *laneFlows) noteNotice(endpoint string) {
	f.noteEndpoint(endpoint, func(e *state.Endpoint) { e.NoticedAt = f.env.Now() })
}

func (f *laneFlows) noteEndpoint(endpoint string, fn func(e *state.Endpoint)) {
	err := f.env.State.Update(func(d *state.Data) error {
		if e, ok := d.Endpoints[endpoint]; ok {
			fn(e)
		}
		return nil
	})
	if err != nil {
		f.env.Log.Error("recording an event of an endpoint", "endpoint", endpoint, "error", err)
	}
}

// noticeFollowsWithin is how long after a card ended for a failed delivery the node's
// notice about it can still arrive, and so how long that failure may explain a notice.
// The node merges the notices of a sender endpoint for a minute and sends at most ten
// notices in ten minutes, holding the rest (docs/references/delivery); a notice is
// therefore due within about eleven minutes of the status that failed the card. The
// bound is generous: a notice that arrives later is explained by nothing and is
// relayed, which costs one card, while a marker that never expired would swallow a
// notice about something else.
const noticeFollowsWithin = 30 * time.Minute

// failureNotNoticed reports whether a card of the endpoint ended for a failed
// delivery, recently, since the last notice about the endpoint was handled: the one
// notice it explains. The caller holds mu.
func (f *laneFlows) failureNotNoticed(endpoint string) bool {
	d := f.env.State.View()
	e, ok := d.Endpoints[endpoint]
	return ok && e.FailedAt.After(e.NoticedAt) && f.env.Now().Sub(e.FailedAt) < noticeFollowsWithin
}

// reply matches the message to a card and writes it there. inbound says the
// message answers no card the lane can still complete.
func (f *laneFlows) reply(ctx context.Context, m Message) (v Verdict, inbound bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.env.State.View()
	match := lane.MatchReply(d.CardList(), m.To.Key, string(m.From), string(m.ID), false)
	if match.Kind == lane.MatchReread {
		f.refreshCreations(ctx, m.To.Key)
		d = f.env.State.View()
		match = lane.MatchReply(d.CardList(), m.To.Key, string(m.From), string(m.ID), true)
	}
	if match.Kind != lane.MatchCard {
		return 0, true, nil
	}
	return f.write(ctx, m, *d.Cards[match.CardID])
}

// write completes the card with the reply. Every step can be repeated: the message
// may be handed over again after the card was completed and before the adapter
// recorded the message as processed.
func (f *laneFlows) write(ctx context.Context, m Message, c state.Card) (Verdict, bool, error) {
	detail, err := f.env.Board.Show(ctx, c.ID)
	if err != nil {
		if isPermanent(err) {
			f.finishLocal(ctx, c.ID)
			return 0, true, nil
		}
		return Retry, false, err
	}
	if terminal(detail.Card.Status) {
		// Hermes finished the card without this reply: the reply is a new message of
		// the conversation. Unless this very reply finished it, in which case the
		// card was written before and only the acknowledgement was lost.
		f.finishLocal(ctx, c.ID)
		if completedBy(detail, m.ID) {
			return Written, false, nil
		}
		return 0, true, nil
	}

	who := c.Alias
	if who == "" {
		who = "the peer"
	}
	// The reply is another agent's text and only ever reaches the card as an
	// attachment: Hermes uploads to the creator's chat every local file that the
	// summary or the result of a completed card names, so a reply there could make
	// it send a file of this machine to a chat. The summary and result are the
	// adapter's words and name no path.
	name := replyAttachmentName(m.ID)
	if err := f.attach(ctx, c.ID, name, m.Body); err != nil {
		return f.failed(ctx, c.ID, err)
	}
	done := kanban.Completion{
		Summary:  fmt.Sprintf("Sidera reply from %s; the full text is in the attachment %s", who, name),
		Result:   fmt.Sprintf("The reply of %s is attached as %s.", who, name),
		Metadata: map[string]any{metaMessageID: string(m.ID), metaFrom: string(m.From), metaAttempt: c.Attempt},
	}
	// The reply is named on the record before the completion, so that a crash that
	// follows the completion cannot make the node's redelivery of the reply look like
	// a new message: the next start finds the card finished on the board, and the
	// message is then recognised by this id (lane.MatchReply).
	if err := f.update(c.ID, func(_ *state.Data, r *state.Card) { r.ReplyID = string(m.ID) }); err != nil {
		return Retry, false, fmt.Errorf("recording the reply written to a card: %w", err)
	}
	if err := f.env.Board.Complete(ctx, c.ID, done); err != nil {
		return f.failed(ctx, c.ID, err)
	}
	f.finishLocal(ctx, c.ID)
	return Written, false, nil
}

// failed turns a failure of Hermes while writing a reply into a verdict: one that
// may pass is a Retry, so the message stays unacknowledged and receiving pauses;
// one that will not (the card is gone, or its state does not allow the completion)
// ends the card here and leaves the message to the inbound half. A message is never
// acknowledged for a card that was not written.
func (f *laneFlows) failed(ctx context.Context, id string, err error) (Verdict, bool, error) {
	if !isPermanent(err) {
		return Retry, false, err
	}
	f.env.Log.Warn("a reply cannot complete its card", "card", id, "error", err)
	f.finishLocal(ctx, id)
	return 0, true, nil
}

// replyAttachmentName is the name of the attachment that holds the reply to a
// message. It is derived from the message id and is not the id itself: an id is
// chosen by the sender and may be any visible ASCII, a file path included, and the
// name is quoted in the result, where Hermes reads paths.
func replyAttachmentName(id adapterproto.MessageID) string {
	sum := sha256.Sum256([]byte(id))
	return "reply-" + hex.EncodeToString(sum[:8]) + ".txt"
}

// attach stores the text as an attachment of the card under the name, unless it is
// there already: Hermes renames a second attachment of the same name, so a replay
// that attached again would leave the reply twice.
func (f *laneFlows) attach(ctx context.Context, id, name, text string) error {
	atts, err := f.env.Board.Attachments(ctx, id)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(atts, func(a kanban.Attachment) bool { return a.Filename == name }) {
		return nil
	}
	return f.env.Board.Attach(ctx, id, filepath.Join(f.env.StateDir, attachDir), name, []byte(text))
}

// completedBy reports whether a run of the card was closed by the reply with this
// message id.
func completedBy(d *kanban.Detail, id adapterproto.MessageID) bool {
	return slices.ContainsFunc(d.Runs, func(r kanban.Run) bool {
		var meta map[string]any
		if json.Unmarshal(r.Metadata, &meta) != nil {
			return false
		}
		got, _ := meta[metaMessageID].(string)
		return got == string(id)
	})
}
