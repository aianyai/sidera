package state

import (
	"cmp"
	"slices"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Phase is where a card is in its delegation.
type Phase string

const (
	// PhaseClaiming: the adapter is about to claim the card on the board, or has
	// claimed it and not yet written the rest of the record. The record exists so
	// that a crash between the claim and the write leaves a trace: the card is
	// running under this adapter's claim for the whole claim time, and nothing
	// else would ever list it again.
	PhaseClaiming Phase = "claiming"
	// PhaseNew: claimed, nothing sent, no attempt begun. A card that waits its turn
	// behind another one is here.
	PhaseNew Phase = "new"
	// PhaseAttempting: the attempt number and its start are persisted. The send
	// may or may not have happened; the message id is not known yet.
	PhaseAttempting Phase = "attempting"
	// PhaseSent: the channel took the message and returned its id; the card waits
	// for the peer's reply.
	PhaseSent Phase = "sent"
	// PhaseBlocked: Hermes has the card blocked, for BlockedFor.
	PhaseBlocked Phase = "blocked"
	// PhaseFinished: the card is completed in Hermes. A record outlives it, as the
	// parent of a follow-up, until Prune.
	PhaseFinished Phase = "finished"
)

// Category groups the reasons a card is blocked as Hermes does: a second block of
// the same kind sends a card to triage, where nobody is woken, so the adapter
// counts blocks per category and never blocks twice for the same one.
type Category string

const (
	CategoryInvalidCard Category = "invalid_card"
	CategoryTransient   Category = "transient"
	CategoryNoReply     Category = "no_reply"
)

// Card is what the adapter keeps about one claimed card.
type Card struct {
	ID string `json:"id"`
	// Endpoint is the key of the conversation endpoint the card sends from. A
	// remote session answers its creator, so a follow-up sends from the same one.
	Endpoint string `json:"endpoint"`
	// Parent is the card this one continues, when it does.
	Parent string `json:"parent,omitempty"`
	Mode   string `json:"mode"`
	Alias  string `json:"alias,omitempty"`
	// Peer is the address the card's messages go to and its reply comes from. For a
	// new session it is empty until the control reply of the creation is read.
	Peer string `json:"peer,omitempty"`
	// Session says Peer is a remote session this adapter created, as opposed to a
	// fixed address of an alias.
	Session bool `json:"session,omitempty"`

	// Attempt is the number of the latest attempt to send, 0 before the first. It
	// and AttemptAt are persisted before the send they describe.
	Attempt   int       `json:"attempt"`
	AttemptAt time.Time `json:"attempt_at"`
	// MessageID is the channel's id of the message the latest attempt created.
	MessageID string `json:"message_id,omitempty"`
	// Delivered says the channel reported the latest attempt as delivered (or, for
	// a new session, that its control reply was read), so the adapter stops asking
	// about it.
	Delivered bool `json:"delivered,omitempty"`

	Phase Phase `json:"phase"`
	// ReplyID is the Sidera id of the reply being written to the card as its
	// completion, recorded before the completion is made. A card finished by a crash
	// right after Hermes took the completion still names the reply, so the node's
	// redelivery of that reply is recognised and not taken for a new message.
	ReplyID string `json:"reply_id,omitempty"`
	// BlockedFor is the category of the block in force, in PhaseBlocked.
	BlockedFor Category `json:"blocked_for,omitempty"`
	// Blocks counts the blocks so far per category.
	Blocks map[Category]int `json:"blocks,omitempty"`

	ClaimedAt time.Time `json:"claimed_at"`
	// WaitingSince is when the wait for the reply began, reset when Hermes unblocks
	// a card that timed out.
	WaitingSince time.Time `json:"waiting_since"`
	FinishedAt   time.Time `json:"finished_at"`
}

// Waiting reports whether the card holds its peer: an attempt is under way or the
// message is sent and unanswered. A blocked card does not, or one stuck card
// would hold up every card behind it for as long as Hermes leaves it blocked.
func (c Card) Waiting() bool { return c.Phase == PhaseAttempting || c.Phase == PhaseSent }

// AwaitsReply reports whether a reply from the card's peer would still answer it:
// the card is waiting, or it is blocked after a message may have gone out (no
// reply in time, or a transient failure whose outcome was not certain). The peer
// answers late, not wrongly, so the reply belongs to the card even though a
// blocked card no longer holds its peer (Waiting). A card blocked as invalid
// never sent anything.
func (c Card) AwaitsReply() bool {
	return c.Waiting() ||
		(c.Phase == PhaseBlocked && (c.BlockedFor == CategoryNoReply || c.BlockedFor == CategoryTransient))
}

// Unfinished reports whether the card may still send or receive.
func (c Card) Unfinished() bool { return c.Phase != PhaseFinished }

// BeginAttempt records the start of attempt number n. The record is written
// before the send, because the idempotency key is built from it.
func (c *Card) BeginAttempt(n int, now time.Time) {
	c.Attempt, c.AttemptAt, c.MessageID, c.Delivered = n, now, "", false
	c.Phase, c.BlockedFor = PhaseAttempting, ""
}

// Sent records the message the attempt created and starts the wait for the reply.
func (c *Card) Sent(messageID string, now time.Time) {
	c.MessageID, c.WaitingSince = messageID, now
	c.Phase, c.BlockedFor = PhaseSent, ""
}

// Block records that Hermes now has the card blocked for the category.
func (c *Card) Block(cat Category) {
	if c.Blocks == nil {
		c.Blocks = map[Category]int{}
	}
	c.Blocks[cat]++
	c.Phase, c.BlockedFor = PhaseBlocked, cat
}

// Unblock records that Hermes released the card again and the adapter took it
// back before anything is sent: it waits for its turn like a card just claimed.
// A card whose claim was only intended (PhaseClaiming) is claimed the same way.
func (c *Card) Unblock() { c.Phase, c.BlockedFor = PhaseNew, "" }

// Finish records that the card is completed.
func (c *Card) Finish(now time.Time) { c.Phase, c.BlockedFor, c.FinishedAt = PhaseFinished, "", now }

// Endpoint is a conversation endpoint the adapter holds.
type Endpoint struct {
	Key string `json:"key"`
	// Ref is the daemon's reference to the endpoint, valid while it is registered.
	Ref          string    `json:"ref"`
	LastActivity time.Time `json:"last_activity"`
	// Target is the subscription to fall back on when an inbound message has no
	// finished card of the conversation to continue: the fields of the source of
	// the first card, taken at registration.
	Target config.Target `json:"target"`
}

// Session is a remote session this adapter created and has not closed.
type Session struct {
	Address string `json:"address"`
	// Endpoint is the key of the endpoint that created it, which any close must be
	// sent from.
	Endpoint string `json:"endpoint"`
	// LastUse is when a card last used the session: created, or finished.
	LastUse time.Time `json:"last_use"`
	// CloseTriedAt is when a close was last asked and not confirmed.
	CloseTriedAt time.Time `json:"close_tried_at"`
	// CloseMessage is the message that carried the close signal and whose reply has
	// not been read yet.
	CloseMessage string `json:"close_message,omitempty"`
}

// Request is a message another node sent to this one, as the card the adapter made
// of it. The reply path needs it: a card that answers the request names the card,
// and the answer goes back to the sender from the endpoint that received the message.
type Request struct {
	// CardID is the request card on the board.
	CardID string `json:"card_id"`
	// Endpoint is the key of the endpoint that received the message.
	Endpoint string `json:"endpoint"`
	// MessageID is the Sidera id of the message, which a reply names.
	MessageID string `json:"message_id"`
	// Sender is the address the message came from and the answer goes to.
	Sender    string    `json:"sender"`
	CreatedAt time.Time `json:"created_at"`
}

// Reply is what the adapter keeps about a card that answers a request: which message
// it sent, and how far it got, so that a card listed again is not sent twice. Its
// phase is one of PhaseAttempting, PhaseSent, PhaseBlocked and PhaseFinished.
type Reply struct {
	CardID string `json:"card_id"`
	Phase  Phase  `json:"phase"`
	// Attempt is the number of the latest attempt to send, 0 before the first. It and
	// AttemptAt are persisted before the send they describe; the idempotency key is
	// built from the number.
	Attempt   int       `json:"attempt"`
	AttemptAt time.Time `json:"attempt_at"`
	// Plain says the latest attempt is a plain message, not a reply: the node no
	// longer admitted the reply to the original message.
	Plain bool `json:"plain,omitempty"`
	// MessageID is the channel's id of the message the latest attempt created.
	MessageID string `json:"message_id,omitempty"`
	// RequestCard is the request card the message answers, which says where the
	// answer went and which endpoint sent it. Set with MessageID.
	RequestCard string `json:"request_card,omitempty"`
	// SentAt is when the message was sent. The node's notice about a failed delivery
	// is looked for only within adapterproto.SentKeyWindow of it (Watched).
	SentAt time.Time `json:"sent_at"`
	// Reported says the failure of the message was reported to the conversation, so
	// that it is reported once however many notices follow.
	Reported   bool             `json:"reported,omitempty"`
	Blocks     map[Category]int `json:"blocks,omitempty"`
	FinishedAt time.Time        `json:"finished_at"`
}

// BeginAttempt records the start of attempt number n, in the form of a plain message
// or of a reply. The record is written before the send, because the idempotency key
// is built from the number.
func (r *Reply) BeginAttempt(n int, plain bool, now time.Time) {
	r.Attempt, r.AttemptAt, r.Plain, r.MessageID, r.Phase = n, now, plain, "", PhaseAttempting
	r.Reported = false
}

// Sent records the message the attempt created, which answers the request card.
func (r *Reply) Sent(requestCard, messageID string, now time.Time) {
	r.RequestCard, r.MessageID, r.SentAt, r.Phase = requestCard, messageID, now, PhaseSent
}

// Watched reports whether the delivery of the answer is still looked at when the
// node sends a notice about the endpoint: the message was sent, its failure has not
// been reported, and the node still holds what became of it. Past the window the
// node may have forgotten the message, and a notice then explains nothing about it.
func (r Reply) Watched(now time.Time) bool {
	return r.MessageID != "" && !r.Reported && now.Sub(r.SentAt) <= adapterproto.SentKeyWindow
}

// Block records that Hermes now has the card blocked for the category.
func (r *Reply) Block(cat Category) {
	if r.Blocks == nil {
		r.Blocks = map[Category]int{}
	}
	r.Blocks[cat]++
	r.Phase = PhaseBlocked
}

// Finish records that the card is completed.
func (r *Reply) Finish(now time.Time) { r.Phase, r.FinishedAt = PhaseFinished, now }

// RequestRetention is how long a Request is kept. The node admits a reply as a reply
// only for a day after the delivery and forgets the message a few hours later, so
// past that an answer goes out as a plain message; the record is still needed then,
// for the address and for the line that says which message is answered. Hermes
// clears the subscriptions of cards done and idle for 30 days (and with them the
// way to wake anybody), so a request older than that is not answered any more.
const RequestRetention = 30 * 24 * time.Hour

// currentVersion is the version of the state file.
const currentVersion = 1

// Data is everything the adapter keeps. It is plain data: the decisions over it
// are in the lane package, the file is the Store's.
type Data struct {
	Version   int                  `json:"version"`
	Cards     map[string]*Card     `json:"cards"`
	Endpoints map[string]*Endpoint `json:"endpoints"`
	Sessions  map[string]*Session  `json:"sessions"`
	// Requests maps the id of a request card to the message it was made of.
	Requests map[string]*Request `json:"requests"`
	// Replies maps the id of a card that answers a request to how far it got.
	Replies map[string]*Reply `json:"replies"`
	// Processed maps the id of an inbound message to the time its record may be
	// dropped. A message the channel redelivers is recognized by it.
	Processed map[string]time.Time `json:"processed"`
}

func newData() Data {
	return Data{
		Version:   currentVersion,
		Cards:     map[string]*Card{},
		Endpoints: map[string]*Endpoint{},
		Sessions:  map[string]*Session{},
		Requests:  map[string]*Request{},
		Replies:   map[string]*Reply{},
		Processed: map[string]time.Time{},
	}
}

// Seen reports whether the inbound message was processed and its record is still
// kept.
func (d *Data) Seen(messageID string, now time.Time) bool {
	until, ok := d.Processed[messageID]
	return ok && now.Before(until)
}

// MarkProcessed records the message as processed and drops the records that have
// expired. The record is kept for adapterproto.ReceivedRedeliveryWindow from now,
// the time the message was first received: until then the node may hand the
// message out again, and after it nothing can. The window is the SDK's, not a
// setting, so it follows the protocol timing when that changes.
func (d *Data) MarkProcessed(messageID string, now time.Time) {
	for id, u := range d.Processed {
		if !now.Before(u) {
			delete(d.Processed, id)
		}
	}
	d.Processed[messageID] = now.Add(adapterproto.ReceivedRedeliveryWindow)
}

// Prune drops the records of cards that finished more than keep ago, except those
// whose remote session is still open: a follow-up needs its parent's peer for as
// long as the session lives. It drops the requests older than RequestRetention, and the
// records of answers that finished more than keep ago, too.
func (d *Data) Prune(now time.Time, keep time.Duration) {
	for id, r := range d.Replies {
		if r.Phase == PhaseFinished && now.Sub(r.FinishedAt) > keep {
			delete(d.Replies, id)
		}
	}
	for id, r := range d.Requests {
		if now.Sub(r.CreatedAt) > RequestRetention {
			delete(d.Requests, id)
		}
	}
	for id, c := range d.Cards {
		if c.Phase != PhaseFinished || now.Sub(c.FinishedAt) <= keep {
			continue
		}
		if _, open := d.Sessions[c.Peer]; c.Session && open {
			continue
		}
		delete(d.Cards, id)
	}
}

// CardList returns the cards in the order they were claimed.
func (d *Data) CardList() []Card {
	out := make([]Card, 0, len(d.Cards))
	for _, c := range d.Cards {
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b Card) int {
		return cmp.Or(a.ClaimedAt.Compare(b.ClaimedAt), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// EndpointList returns the endpoints ordered by key.
func (d *Data) EndpointList() []Endpoint {
	out := make([]Endpoint, 0, len(d.Endpoints))
	for _, e := range d.Endpoints {
		out = append(out, *e)
	}
	slices.SortFunc(out, func(a, b Endpoint) int { return cmp.Compare(a.Key, b.Key) })
	return out
}

// SessionList returns the open remote sessions ordered by address.
func (d *Data) SessionList() []Session {
	out := make([]Session, 0, len(d.Sessions))
	for _, s := range d.Sessions {
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b Session) int { return cmp.Compare(a.Address, b.Address) })
	return out
}
