package lane

import "github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"

// ShouldQueue reports whether the card has to wait before it sends: another card
// still waits for a reply from the same peer at the same endpoint. Every send
// follows the rule, follow-ups included, because a reply carries nothing that says
// which of two outstanding messages it answers. A new session has no peer yet and
// never waits: each creates a session of its own.
func ShouldQueue(cards []state.Card, c state.Card) bool {
	if c.Peer == "" {
		return false
	}
	for _, o := range cards {
		if o.ID != c.ID && o.Endpoint == c.Endpoint && o.Peer == c.Peer && o.Waiting() {
			return true
		}
	}
	return false
}

// NextQueued returns the card that may send now on the pair of endpoint and peer:
// the oldest claimed card that has not begun an attempt, provided no card still
// waits there. Cards are in claim order. It returns "" when there is none.
func NextQueued(cards []state.Card, endpoint, peer string) string {
	next := ""
	for _, c := range cards {
		if c.Endpoint != endpoint || c.Peer != peer {
			continue
		}
		if c.Waiting() {
			return ""
		}
		if next == "" && c.Phase == state.PhaseNew {
			next = c.ID
		}
	}
	return next
}
