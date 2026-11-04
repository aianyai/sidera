package lane

import "github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"

// MatchKind is the verdict on a received message.
type MatchKind int

const (
	// MatchNone: no card waits for this sender; the message is a new one.
	MatchNone MatchKind = iota
	// MatchCard: the message answers the card in Match.CardID.
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
// answers: the card waiting at that endpoint whose peer is the sender. A
// session's first reply can reach the endpoint before the adapter has read the
// control reply of the creation that names the session, so when no card matches
// and some waiting card has no peer yet, the verdict is to read again first;
// reread says the caller has done so. Cards are in claim order, and if more than
// one waits on the same peer, which the queue prevents, the oldest answers.
func MatchReply(cards []state.Card, endpoint, from string, reread bool) Match {
	unknownPeer := false
	for _, c := range cards {
		if c.Endpoint != endpoint || !c.Waiting() {
			continue
		}
		if c.Peer == from {
			return Match{Kind: MatchCard, CardID: c.ID}
		}
		unknownPeer = unknownPeer || c.Peer == ""
	}
	if unknownPeer && !reread {
		return Match{Kind: MatchReread}
	}
	return Match{Kind: MatchNone}
}
