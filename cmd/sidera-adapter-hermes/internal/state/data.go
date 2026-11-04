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

	Phase Phase `json:"phase"`
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
	c.Attempt, c.AttemptAt, c.MessageID = n, now, ""
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
}

// currentVersion is the version of the state file.
const currentVersion = 1

// Data is everything the adapter keeps. It is plain data: the decisions over it
// are in the lane package, the file is the Store's.
type Data struct {
	Version   int                  `json:"version"`
	Cards     map[string]*Card     `json:"cards"`
	Endpoints map[string]*Endpoint `json:"endpoints"`
	Sessions  map[string]*Session  `json:"sessions"`
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
// long as the session lives.
func (d *Data) Prune(now time.Time, keep time.Duration) {
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
