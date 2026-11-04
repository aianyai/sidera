package lane

import (
	"slices"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"
)

// CloseRetryInterval is how long to wait before asking again to close a session
// whose close was not confirmed.
const CloseRetryInterval = 10 * time.Minute

// SessionPolicy is what decides when a remote session is closed.
type SessionPolicy struct {
	// CloseWindow is how long after its last use the session of a cron or orphan
	// card is kept, for a late message.
	CloseWindow time.Duration
	// Retention is how long after its last use the session of a conversation is
	// kept: the time in which a follow-up can still come.
	Retention time.Duration
	// Max is the number of sessions kept open at most.
	Max int
}

// SessionsToClose returns the addresses of the remote sessions to ask to close
// now. A session is closed when no unfinished card uses it and
// it has been idle past its window, which depends on where it came from; and,
// beyond that, the least recently used of the idle ones are closed until no more
// than Max remain open. A session in use is never closed to make room: that would
// fail a card that is waiting. A session whose close was asked and not confirmed
// still counts as open and is asked again after CloseRetryInterval.
//
// The caller, on the answer to the close, forgets the session when it is closed or
// the endpoint is closed, which counts as closed, and records the try otherwise.
func SessionsToClose(sessions []state.Session, cards []state.Card, p SessionPolicy, now time.Time) []string {
	inUse := map[string]bool{}
	for _, c := range cards {
		if c.Session && c.Unfinished() {
			inUse[c.Peer] = true
		}
	}
	var idle []state.Session
	for _, s := range sessions {
		if inUse[s.Address] {
			continue
		}
		if !s.CloseTriedAt.IsZero() && now.Sub(s.CloseTriedAt) < CloseRetryInterval {
			continue
		}
		idle = append(idle, s)
	}
	slices.SortStableFunc(idle, func(a, b state.Session) int { return a.LastUse.Compare(b.LastUse) })

	var out, rest []string
	for _, s := range idle {
		window := p.Retention
		if KindOf(s.Endpoint) != KindConversation {
			window = p.CloseWindow
		}
		if now.Sub(s.LastUse) >= window {
			out = append(out, s.Address)
		} else {
			rest = append(rest, s.Address)
		}
	}
	// Idle sessions are in order of use, so the ones that are not yet due and are
	// closed to make room are the least recently used of them.
	if excess := len(sessions) - len(out) - p.Max; excess > 0 {
		out = append(out, rest[:min(excess, len(rest))]...)
	}
	return out
}

// Route is where a follow-up card sends.
type Route struct {
	Endpoint string
	Peer     string
	// Session says the peer is a remote session the adapter created.
	Session bool
	Alias   string
}

// RouteFollowUp finds where a follow-up that names no alias goes: to the peer of
// its first parent that has one, from the endpoint that parent used, because a
// remote session answers only the endpoint that created it. The alias the parent
// was sent under must still be in the alias table: the table is the allow-list of
// what a card can make this node do, and a follow-up reaches the peer through the
// parent without naming it. When that peer is a remote session the adapter has
// since closed, the follow-up cannot go on and the reason is ReasonSessionClosed;
// when its alias was removed, ReasonAliasRemoved; when no parent has a peer to
// continue, ReasonInvalidCard.
func RouteFollowUp(parents []state.Card, sessions []state.Session, aliases config.Aliases) (Route, Reason, bool) {
	for _, p := range parents {
		if p.Peer == "" {
			continue
		}
		if _, ok := aliases.Lookup(p.Alias); !ok {
			return Route{}, ReasonAliasRemoved, false
		}
		if p.Session && !slices.ContainsFunc(sessions, func(s state.Session) bool { return s.Address == p.Peer }) {
			return Route{}, ReasonSessionClosed, false
		}
		return Route{Endpoint: p.Endpoint, Peer: p.Peer, Session: p.Session, Alias: p.Alias}, 0, true
	}
	return Route{}, ReasonInvalidCard, false
}
