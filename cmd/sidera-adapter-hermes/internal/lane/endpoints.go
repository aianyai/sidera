package lane

import (
	"slices"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"
)

// reservedEndpoints are the slots of the cap kept for the instance endpoint and the
// orphan endpoint, so that a card can always be sent from the orphan.
const reservedEndpoints = 2

// Placement is where a card's endpoint comes from.
type Placement struct {
	// Key is the key of the endpoint to send from. It is the key asked for, or
	// OrphanKey when the cap left no room.
	Key string
	// Retire is the key of the endpoint to retire first to make room, or "".
	Retire string
	// Reason says, for a comment on the card, why it is sent from the orphan
	// endpoint instead of its own. Empty otherwise.
	Reason string
}

// retirable lists the conversation endpoints that can be retired, least recently
// active first: no unfinished card sends from one and no open remote session was
// created by one, since a session can be closed only from its creator.
func retirable(endpoints []state.Endpoint, sessions []state.Session, cards []state.Card) []state.Endpoint {
	busy := map[string]bool{}
	for _, c := range cards {
		if c.Unfinished() {
			busy[c.Endpoint] = true
		}
	}
	for _, s := range sessions {
		busy[s.Endpoint] = true
	}
	var out []state.Endpoint
	for _, e := range endpoints {
		if e.Key != OrphanKey && !busy[e.Key] {
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b state.Endpoint) int { return a.LastActivity.Compare(b.LastActivity) })
	return out
}

// PlaceEndpoint decides which endpoint a card of the source key sends from, with
// at most max endpoints held, the instance endpoint and the orphan included. A key
// that is registered, and the orphan, are used as they are. A new key takes a free
// slot; when none is free, the conversation endpoint idle longest that can be
// retired makes room; when none can, the card is sent from the orphan endpoint.
func PlaceEndpoint(key string, endpoints []state.Endpoint, sessions []state.Session, cards []state.Card, max int) Placement {
	if key == OrphanKey || slices.ContainsFunc(endpoints, func(e state.Endpoint) bool { return e.Key == key }) {
		return Placement{Key: key}
	}
	conversations := 0
	for _, e := range endpoints {
		if e.Key != OrphanKey {
			conversations++
		}
	}
	if conversations < max-reservedEndpoints {
		return Placement{Key: key}
	}
	if r := retirable(endpoints, sessions, cards); len(r) > 0 {
		return Placement{Key: key, Retire: r[0].Key}
	}
	return Placement{
		Key:    OrphanKey,
		Reason: "the node's endpoint limit is reached and every conversation endpoint is in use, so this card is sent from the shared endpoint",
	}
}

// IdleEndpoints returns the keys of the endpoints to retire for lack of use: idle
// longer than idle and retirable. Retiring is cheap: the same key opens the same
// address again.
func IdleEndpoints(endpoints []state.Endpoint, sessions []state.Session, cards []state.Card, idle time.Duration, now time.Time) []string {
	var out []string
	for _, e := range retirable(endpoints, sessions, cards) {
		if now.Sub(e.LastActivity) > idle {
			out = append(out, e.Key)
		}
	}
	return out
}
