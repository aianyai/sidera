package lane

import (
	"strconv"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// IdempotencyKey is the key of one attempt to send a card. It is built from the
// attempt number the adapter persisted before the send, never from anything Hermes
// hands out per run, so that a resend after a crash is the same message to the
// channel and a deliberate retry is a new one.
func IdempotencyKey(cardID string, attempt int) string {
	return cardID + ":" + strconv.Itoa(attempt)
}

// Reason is why a card cannot go on.
type Reason int

const (
	// ReasonInvalidCard: the card is not one this node serves.
	ReasonInvalidCard Reason = iota + 1
	// ReasonDeliveryFailed: the channel reported the message as failed.
	ReasonDeliveryFailed
	// ReasonOutcomeUnknown: the message may or may not have been sent, and it is
	// too late to find out by sending it again.
	ReasonOutcomeUnknown
	// ReasonSessionClosed: the remote session a follow-up continues is gone.
	ReasonSessionClosed
	// ReasonNoReply: the peer did not answer in time.
	ReasonNoReply
)

// Category is the category the reason is counted in.
func (r Reason) Category() state.Category {
	switch r {
	case ReasonInvalidCard:
		return state.CategoryInvalidCard
	case ReasonNoReply:
		return state.CategoryNoReply
	default:
		return state.CategoryTransient
	}
}

// BlockKindOf is the kind a card blocked in the category is blocked with in
// Hermes. It is the one place that maps the adapter's categories to Hermes'
// vocabulary: an invalid card cannot be done as written (capability), a failure
// that may be gone on the next try is transient, and a card waiting for a peer
// that has not answered waits for input. The three stay distinct because Hermes
// counts blocks per kind and sends a card to triage, where nobody is woken, on the
// second block of one kind; mapping two categories to one kind would spend that
// second block on a different cause.
func BlockKindOf(cat state.Category) kanban.BlockKind {
	switch cat {
	case state.CategoryInvalidCard:
		return kanban.BlockCapability
	case state.CategoryNoReply:
		return kanban.BlockNeedsInput
	default:
		return kanban.BlockTransient
	}
}

// Verdict says what to do with a card that cannot go on.
type Verdict int

const (
	// VerdictBlock: block the card and let its creator decide.
	VerdictBlock Verdict = iota + 1
	// VerdictComplete: complete the card and say in the result why it failed. A
	// second block of the same kind would send the card to a state in which nobody
	// is woken, so a card is blocked once per category and ends the second time.
	VerdictComplete
)

// OnFailure decides what to do when a card cannot go on for the reason, given the
// blocks it has had so far per category (state.Card.Blocks, state.Reply.Blocks).
func OnFailure(blocks map[state.Category]int, r Reason) Verdict {
	if blocks[r.Category()] > 0 {
		return VerdictComplete
	}
	return VerdictBlock
}

// NoReplyDue reports whether a card has waited for its reply longer than the
// timeout.
func NoReplyDue(c state.Card, now time.Time, timeout time.Duration) bool {
	return c.Phase == state.PhaseSent && !now.Before(c.WaitingSince.Add(timeout))
}

// ActionKind is what to do with a card seen again.
type ActionKind int

const (
	// ActionNew: the card is not known, or its claim was only intended and never
	// confirmed; handle it as a new one.
	ActionNew ActionKind = iota + 1
	// ActionIgnore: the card is finished here; nothing to do.
	ActionIgnore
	// ActionRevalidate: read the card again and check it again.
	ActionRevalidate
	// ActionKeepWaiting: claim again and go on waiting for the reply; do not send.
	ActionKeepWaiting
	// ActionSend: claim again and send with the attempt number given.
	ActionSend
	// ActionOutcomeUnknown: the attempt was begun too long ago to repeat; treat the
	// outcome as unknown (ReasonOutcomeUnknown).
	ActionOutcomeUnknown
)

// Action is the answer of OnReady.
type Action struct {
	Kind ActionKind
	// Attempt is the number to send with, for ActionSend.
	Attempt int
	// ResetTimer says the wait for the reply starts again now, for
	// ActionKeepWaiting.
	ResetTimer bool
}

// OnReady decides what to do with a card the board lists as ready. A nil card is
// one the adapter has no record of. A known card is ready again because its
// creator unblocked it, or because its claim lapsed or was released, and the
// record says which:
//
//   - claiming: the record was written before the claim and nothing after it, so
//     the card is taken again like a new one;
//   - blocked as invalid: the creator may have fixed the card, so read it again;
//   - blocked for no reply: wait again without sending, with the timer reset;
//   - blocked as transient (failed, outcome unknown, session closed): the creator
//     asked for a retry, so send again as a new attempt;
//   - not blocked and not sent: send. A card that never began an attempt gets the
//     next number. One that did sends with the same number, so the channel
//     recognizes it as the same message, unless the attempt began longer ago than
//     adapterproto.SentKeyWindow: then it is not repeated, because a repeat might
//     create a second remote session, and the outcome is unknown;
//   - sent: it already is; go on waiting, timer as it was.
//
// The window is the SDK's, not a setting: a copied value would drift silently
// when the protocol timing changes. It holds only while the adapter's sends name
// no accept_before_s, so the node's default deadline applies; a send that names
// one must not rely on it, and the window has to be re-derived from that deadline.
func OnReady(c *state.Card, now time.Time) Action {
	switch {
	case c == nil, c.Phase == state.PhaseClaiming:
		return Action{Kind: ActionNew}
	case c.Phase == state.PhaseFinished:
		return Action{Kind: ActionIgnore}
	case c.Phase == state.PhaseBlocked && c.BlockedFor == state.CategoryInvalidCard:
		return Action{Kind: ActionRevalidate}
	case c.Phase == state.PhaseBlocked && c.BlockedFor == state.CategoryNoReply:
		return Action{Kind: ActionKeepWaiting, ResetTimer: true}
	case c.Phase == state.PhaseBlocked, c.Phase == state.PhaseNew:
		return Action{Kind: ActionSend, Attempt: c.Attempt + 1}
	case c.Phase == state.PhaseAttempting && now.Sub(c.AttemptAt) > adapterproto.SentKeyWindow:
		return Action{Kind: ActionOutcomeUnknown}
	case c.Phase == state.PhaseAttempting:
		return Action{Kind: ActionSend, Attempt: c.Attempt}
	default:
		return Action{Kind: ActionKeepWaiting}
	}
}
