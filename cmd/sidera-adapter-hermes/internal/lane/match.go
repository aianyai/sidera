package lane

import "github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"

// MatchKind is the verdict on a received message.
type MatchKind int

const (
	// MatchNone: no card waits for this sender; the message is a new one.
	MatchNone MatchKind = iota
	// MatchCard: the message answers the card in Match.CardID, which is waiting for
	// it, or is finished by an earlier hand-out of this very message (Card.ReplyID)
	// and so is for the caller to read back from the board.
	MatchCard
	// MatchReread: a card waits on a session whose address is not known yet, and
	// the message may come from it. The caller reads the control replies of the
	// endpoint's pending creations, records the addresses, and asks again with
	// reread set.
	MatchReread
)

// Match is the answer of MatchReply.
type Match struct {
	Kind   MatchKind
	CardID string
}

// MatchReply finds the card a message received on endpoint from the address from
// answers: the card at that endpoint whose peer is the sender and that still
// awaits a reply (state.Card.AwaitsReply), which includes a card blocked for no
// reply or a transient failure: the peer was only late, and the reply belongs to
// the card even though the card no longer holds its peer for queueing. A
// session's first reply can reach the endpoint before the adapter has read the
// control reply of the creation that names the session, so when no card matches
// and some waiting card has no peer yet, the verdict is to read again first;
// reread says the caller has done so. A blocked card without a peer does not
// trigger that read: it is not expecting a first reply, and the read would be
// repeated for every message that arrives while its creator leaves it blocked.
// Cards are in claim order and the oldest answers: a peer answers in the order it
// was asked, and a blocked card was asked before the card now waiting behind it.
//
// The matched card may be blocked in Hermes, ready again after its creator
// unblocked it, or claimed again by the adapter; Hermes completes it from any of
// these without a release or a new claim (see the reference), so the caller
// completes it with the reply as it would a waiting card.
//
// A message whose id a finished card names as the reply it was completed with
// (state.Card.ReplyID) matches that card first. It was handed out before; the
// adapter completed the card and ended before it could record the message as
// processed, and the card is finished in the record because the next start found
// Hermes had finished it. The caller reads the card to learn whether this reply
// finished it.
func MatchReply(cards []state.Card, endpoint, from, messageID string, reread bool) Match {
	for _, c := range cards {
		if c.Endpoint == endpoint && c.Phase == state.PhaseFinished && c.ReplyID != "" && c.ReplyID == messageID {
			return Match{Kind: MatchCard, CardID: c.ID}
		}
	}
	unknownPeer := false
	for _, c := range cards {
		if c.Endpoint != endpoint || !c.AwaitsReply() {
			continue
		}
		if c.Peer == from {
			return Match{Kind: MatchCard, CardID: c.ID}
		}
		unknownPeer = unknownPeer || (c.Peer == "" && c.Waiting())
	}
	if unknownPeer && !reread {
		return Match{Kind: MatchReread}
	}
	return Match{Kind: MatchNone}
}
