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

// InUse is what keeps an endpoint from being retired, besides the endpoint being
// the orphan's: it is used again by something the adapter still has to do.
type InUse struct {
	// Sessions are the open remote sessions: a session can be closed only from the
	// endpoint that created it.
	Sessions []state.Session
	// Cards are the cards; an unfinished one sends from its endpoint and awaits a
	// reply there.
	Cards []state.Card
	// Requests are the messages other nodes sent that became cards. A request not
	// answered yet is answered from the endpoint that received it, by a reply card
	// that may come in a later turn of the conversation.
	Requests []state.Request
	// Replies are the answers sent. One whose delivery the node may still report
	// (state.Reply.Watched) is told about by a notice to the endpoint it was sent
	// from.
	Replies []state.Reply
	// Now is the time Watched is judged at.
	Now time.Time
}

// UseOf is what the records hold in use at the time.
func UseOf(d *state.Data, now time.Time) InUse {
	u := InUse{Sessions: d.SessionList(), Cards: d.CardList(), Now: now}
	for _, r := range d.Requests {
		u.Requests = append(u.Requests, *r)
	}
	for _, r := range d.Replies {
		u.Replies = append(u.Replies, *r)
	}
	return u
}

// busy lists the keys of the endpoints something still depends on.
func (u InUse) busy() map[string]bool {
	busy := map[string]bool{}
	for _, c := range u.Cards {
		if c.Unfinished() {
			busy[c.Endpoint] = true
		}
	}
	for _, s := range u.Sessions {
		busy[s.Endpoint] = true
	}
	requestEndpoint := map[string]string{}
	for _, r := range u.Requests {
		requestEndpoint[r.CardID] = r.Endpoint
	}
	answered := map[string]bool{}
	for _, r := range u.Replies {
		if r.RequestCard != "" {
			answered[r.RequestCard] = true
		}
		if r.Watched(u.Now) {
			busy[requestEndpoint[r.RequestCard]] = true
		}
	}
	for _, r := range u.Requests {
		if !answered[r.CardID] {
			busy[r.Endpoint] = true
		}
	}
	return busy
}

// retirable lists the conversation endpoints that can be retired, least recently
// active first: nothing in use keeps one (InUse).
func retirable(endpoints []state.Endpoint, use InUse) []state.Endpoint {
	busy := use.busy()
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
func PlaceEndpoint(key string, endpoints []state.Endpoint, use InUse, max int) Placement {
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
	if r := retirable(endpoints, use); len(r) > 0 {
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
func IdleEndpoints(endpoints []state.Endpoint, use InUse, idle time.Duration) []string {
	var out []string
	for _, e := range retirable(endpoints, use) {
		if use.Now.Sub(e.LastActivity) > idle {
			out = append(out, e.Key)
		}
	}
	return out
}
