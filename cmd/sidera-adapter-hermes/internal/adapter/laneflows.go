package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/lane"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// Inbound is what other nodes start: a message to the instance endpoint is a new
// request, a message to a conversation endpoint that answers no card continues the
// conversation, and a card whose mode is reply answers a request. The delegation
// flows (cards sent out, replies written back) hand these over; they never decide
// them.
type Inbound interface {
	// HandleInbound takes a message the delegation flows do not own: one to the
	// instance endpoint, one to a conversation or orphan endpoint that matches no
	// waiting card, and a reply whose card Hermes has already finished or no longer
	// allows to be completed. It answers like MessageHandler.HandleMessage: Written
	// only once everything the message is for is on the board.
	HandleInbound(ctx context.Context, m Message) (Verdict, error)
	// HandleNotice takes a notice of the node about the endpoint (a Message with
	// Feedback set). settled says the delegation flows already ended a card because
	// of it. The notice is explained to the conversation only when nothing else
	// explained it. It answers like HandleInbound.
	HandleNotice(ctx context.Context, m Message, settled bool) (Verdict, error)
	// HandleReplyCard takes a ready card whose mode is reply. The delegation flows
	// do not claim it; the poll lists it again until it is claimed.
	HandleReplyCard(ctx context.Context, c kanban.Card)
}

// LaneFlows builds the flows of the lane: delegating cards, writing replies back,
// the clock-driven work. inbound builds the other half; nil leaves messages and
// reply cards unhandled (a message is then left unacknowledged).
func LaneFlows(inbound func(env *Env) Inbound) Flows {
	return func(env *Env) Handlers {
		var in Inbound = &unhandledInbound{log: env.Log, logged: map[string]bool{}}
		if inbound != nil {
			in = inbound(env)
		}
		f := &laneFlows{env: env, inbound: in, queued: map[string]bool{}}
		return Handlers{Messages: f, Cards: f, Timers: f}
	}
}

// unhandledInbound is the inbound half until there is one: it acknowledges
// nothing and says so once per reply card.
type unhandledInbound struct {
	log    *slog.Logger
	mu     sync.Mutex
	logged map[string]bool
}

func (u *unhandledInbound) HandleInbound(_ context.Context, m Message) (Verdict, error) {
	u.log.Warn("an inbound message is not handled", "id", m.ID, "from", m.From, "endpoint", m.To.Key)
	return Skip, nil
}

func (u *unhandledInbound) HandleNotice(_ context.Context, m Message, _ bool) (Verdict, error) {
	u.log.Warn("a notice of the node is not handled", "id", m.ID, "endpoint", m.To.Key)
	return Skip, nil
}

func (u *unhandledInbound) HandleReplyCard(_ context.Context, c kanban.Card) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.logged[c.ID] {
		u.logged[c.ID] = true
		u.log.Warn("a reply card is not handled and stays ready", "card", c.ID)
	}
}

// laneFlows implements the message, card and timer handlers of the lane.
//
// One mutex serializes them. The receive loop, the poll and the timers all read
// and move the same cards, and what each does is a short run of commands; running
// them one at a time is what keeps "the record says sent" and "the message went
// out" from being seen halfway by another.
type laneFlows struct {
	env     *Env
	inbound Inbound

	mu sync.Mutex
	// queued are the cards already commented as queued; a restart may comment once
	// more, which is harmless.
	queued map[string]bool
	// blockedCheckedAt is when the blocked cards were last read (reconcileBlocked).
	blockedCheckedAt time.Time
}

// Texts the flows write into cards. They are short, and none holds remote text.
const (
	summaryNoDelivery = "Sidera could not deliver this task; the reason is in the result"
	summaryNoReply    = "Sidera got no reply from the peer; the reason is in the result"
	summaryInvalid    = "Sidera cannot serve this card; the reason is in the result"
	commentQueued     = "Queued behind an earlier card to the same peer."
	commentUnconfirm  = "The remote session may exist but was not confirmed ready; still waiting for its reply."
)

// failureSummary is the summary a card that cannot go on is completed with. Its
// first line is what Hermes wakes the creator with, so it is the adapter's.
func failureSummary(cat state.Category, replyCard bool) string {
	switch {
	case cat == state.CategoryInvalidCard:
		return summaryInvalid
	case replyCard:
		return summaryReplyFailed
	case cat == state.CategoryNoReply:
		return summaryNoReply
	}
	return summaryNoDelivery
}

// endFailed ends what a card that cannot go on was doing, once per cause, for a
// delegated card and a reply card alike: the first time it is blocked so that its
// creator decides, the second time it is completed with the reason, since Hermes
// sends a card blocked again with its last kind to a state in which nobody is woken
// (lane.OnFailure). blocks are the blocks the card has had; text is the reason, and
// completionText replaces it when the card is completed and the second time has
// something else to say. blocked says which of the two was tried, and the error
// is Hermes' answer to it. Recording the outcome is the caller's.
func endFailed(ctx context.Context, board *kanban.Client, id string, blocks map[state.Category]int,
	reason lane.Reason, text, completionText string, replyCard bool) (blocked bool, err error) {
	cat := reason.Category()
	if lane.OnFailure(blocks, reason) == lane.VerdictBlock {
		return true, board.Block(ctx, id, lane.BlockKindOf(cat), clip(text))
	}
	if completionText != "" {
		text = completionText
	}
	return false, board.Complete(ctx, id, kanban.Completion{Summary: failureSummary(cat, replyCard), Result: clip(text)})
}

// maxReason bounds the text of a block reason or a comment that may quote a bit of
// what a card or a peer said.
const maxReason = 1024

// clip cuts s to maxReason bytes on a character boundary.
func clip(s string) string {
	if len(s) <= maxReason {
		return s
	}
	cut := s[:maxReason]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "..."
}

// isPermanent reports a failure of Hermes that will not pass.
func isPermanent(err error) bool { return errors.Is(err, kanban.ErrPermanent) }

// refusalReason is the reason the node gave for refusing a send (the data of a
// send_refused error), or "" when the error is not that.
func refusalReason(err error) adapterproto.SendRefusedReason {
	var pe *peer.Error
	if !errors.As(err, &pe) || pe.Code != adapterproto.CodeSendRefused {
		return ""
	}
	var data adapterproto.SendRefusedData
	if json.Unmarshal(pe.Data, &data) != nil {
		return ""
	}
	return data.Reason
}

// refused reports a send that the node answered with a refusal that will not
// change: the message certainly did not go. A busy node, a cancelled call and every
// failure whose outcome is unknown are not refusals; the same send is tried again
// with the same key.
func refused(err error) bool {
	var pe *peer.Error
	return errors.As(err, &pe) && peer.IsApplicationCode(pe.Code) && pe.Code != adapterproto.CodeNodeBusy
}

// sessionAddress is the address of the session a control reply names: the reply
// carries the endpoint as the node of the runtime knows it, and that node is the
// one the signal was sent to.
func sessionAddress(recipients []adapterproto.RecipientStatus, endpoint adapterproto.EndpointRef) string {
	if len(recipients) == 1 {
		if node, _, ok := strings.Cut(string(recipients[0].To), "/"); ok {
			return node + "/" + string(endpoint)
		}
	}
	return string(endpoint)
}

// subscriptionFor is the subscription that wakes the target when a card ends.
func subscriptionFor(t config.Target) kanban.Subscription {
	return kanban.Subscription{
		Platform: t.Platform, ChatID: t.ChatID, ThreadID: t.ThreadID, UserID: t.UserID,
		UserIDAlt: t.UserIDAlt, ChatType: t.ChatType, ParentChatID: t.ParentChatID, GuildID: t.GuildID,
		NotifierProfile: t.Profile, DeliveryMode: kanban.DeliveryNotifyWake,
	}
}

// update applies fn to the record of the card, if there is one.
func (f *laneFlows) update(id string, fn func(d *state.Data, c *state.Card)) error {
	return f.env.State.Update(func(d *state.Data) error {
		if c, ok := d.Cards[id]; ok {
			fn(d, c)
		}
		return nil
	})
}

// finishLocal records that the card is over for the adapter, whether the adapter
// completed it or found Hermes had, and then lets the card queued behind it go. A
// card that is finished already is left as it is.
// The session it used counts as used now, which is what its close window runs from.
func (f *laneFlows) finishLocal(ctx context.Context, id string) {
	var endpoint, peerAddr string
	if err := f.update(id, func(d *state.Data, c *state.Card) {
		if c.Phase == state.PhaseFinished {
			return
		}
		now := f.env.Now()
		endpoint, peerAddr = c.Endpoint, c.Peer
		c.Finish(now)
		if s, ok := d.Sessions[c.Peer]; ok && c.Session {
			s.LastUse = now
		}
	}); err != nil {
		f.env.Log.Error("recording a finished card", "card", id, "error", err)
		return
	}
	delete(f.queued, id)
	if peerAddr != "" {
		f.next(ctx, endpoint, peerAddr)
	}
}

// next sends the card that was queued behind the one that just ended.
func (f *laneFlows) next(ctx context.Context, endpoint, peerAddr string) {
	d := f.env.State.View()
	if id := lane.NextQueued(d.CardList(), endpoint, peerAddr); id != "" {
		f.advance(ctx, id)
	}
}

// comment appends a comment to the card; a failure is logged and not retried, the
// comment being a courtesy to whoever reads the card.
func (f *laneFlows) comment(ctx context.Context, id, text string) {
	if err := f.env.Board.Comment(ctx, id, text); err != nil {
		f.env.Log.Warn("commenting on a card", "card", id, "error", err)
	}
}
