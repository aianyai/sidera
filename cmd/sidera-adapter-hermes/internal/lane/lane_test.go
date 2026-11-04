package lane

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aianyai/sidera/sdk/go/adapterproto"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"
)

var t0 = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return t0.Add(-d) }

func TestEndpointKey(t *testing.T) {
	long := strings.Repeat("x", 200)
	cases := []struct {
		name string
		src  Source
		want string
	}{
		{"chat", Source{Profile: "feishu", Platform: "feishu", ChatID: "oc_1"}, "chat:feishu:feishu:oc_1"},
		{"chat in a thread", Source{Profile: "p", Platform: "slack", ChatID: "chan1", ThreadID: "17.5"}, "chat:p:slack:chan1:17.5"},
		{"chat wins over cron", Source{Profile: "p", Platform: "slack", ChatID: "chan1", CronJobID: "j"}, "chat:p:slack:chan1"},
		{"cron", Source{Profile: "p", CronJobID: "daily"}, "cron:p:daily"},
		{"a platform without a chat is no conversation", Source{Profile: "p", Platform: "slack"}, "orphan"},
		{"nothing", Source{}, "orphan"},
		{"colons in a chat id cannot make two sources one", Source{Profile: "p", Platform: "tui", ChatID: "a:b"}, "chat:p:tui:a%3Ab"},
	}
	for _, tc := range cases {
		if got := EndpointKey(tc.src); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	// Two sources whose joined text would be the same stay apart.
	a := EndpointKey(Source{Profile: "p", Platform: "x:y", ChatID: "z"})
	b := EndpointKey(Source{Profile: "p", Platform: "x", ChatID: "y:z"})
	if a == b {
		t.Errorf("collision: %q", a)
	}
	// A key over the bound is cut and ends in a digest of the whole, the same
	// every time, and differs for sources that share a prefix.
	k1 := EndpointKey(Source{Profile: "p", Platform: "tui", ChatID: long + "1"})
	k2 := EndpointKey(Source{Profile: "p", Platform: "tui", ChatID: long + "2"})
	if utf8.RuneCountInString(k1) != adapterproto.MaxIdentLen || k1 == k2 || k1 != EndpointKey(Source{Profile: "p", Platform: "tui", ChatID: long + "1"}) {
		t.Errorf("long keys: %q %q", k1, k2)
	}
	if !strings.HasPrefix(k1, "chat:p:tui:xxx") || KindOf(k1) != KindConversation {
		t.Errorf("long key lost its prefix: %q", k1)
	}
	// Multi-byte text is cut on a character, not in the middle of one.
	k3 := EndpointKey(Source{Profile: "p", Platform: "tui", ChatID: strings.Repeat("\u4f1a", 200)})
	if utf8.RuneCountInString(k3) != adapterproto.MaxIdentLen || !utf8.ValidString(k3) {
		t.Errorf("multi-byte key: %q", k3)
	}
}

func TestKindOf(t *testing.T) {
	for key, want := range map[string]Kind{"orphan": KindOrphan, "cron:p:j": KindCron, "chat:p:x:y": KindConversation} {
		if got := KindOf(key); got != want {
			t.Errorf("KindOf(%q) = %v", key, got)
		}
	}
}

func card(id, endpoint, peer string, phase state.Phase) state.Card {
	return state.Card{ID: id, Endpoint: endpoint, Peer: peer, Phase: phase}
}

func TestMatchReply(t *testing.T) {
	cards := []state.Card{
		card("a", "e1", "peer-1", state.PhaseSent),
		card("b", "e1", "peer-2", state.PhaseBlocked),
		card("c", "e2", "peer-1", state.PhaseSent),
		card("d", "e1", "peer-3", state.PhaseFinished),
		card("e", "e1", "peer-4", state.PhaseAttempting),
	}
	cases := []struct {
		name     string
		cards    []state.Card
		endpoint string
		from     string
		reread   bool
		want     Match
	}{
		{"waiting card of that endpoint and sender", cards, "e1", "peer-1", false, Match{MatchCard, "a"}},
		{"same sender, other endpoint", cards, "e2", "peer-1", false, Match{MatchCard, "c"}},
		{"a send whose message id is not recorded yet still waits", cards, "e1", "peer-4", false, Match{MatchCard, "e"}},
		{"blocked card does not wait", cards, "e1", "peer-2", false, Match{Kind: MatchNone}},
		{"finished card does not wait", cards, "e1", "peer-3", false, Match{Kind: MatchNone}},
		{"stranger", cards, "e1", "peer-9", false, Match{Kind: MatchNone}},
		{"endpoint without cards", cards, "e3", "peer-1", false, Match{Kind: MatchNone}},
		{"session not known yet: read the creations first", append(cards[:1:1], card("n", "e1", "", state.PhaseSent)), "e1", "session-x", false, Match{Kind: MatchReread}},
		{"read already, still none", append(cards[:1:1], card("n", "e1", "", state.PhaseSent)), "e1", "session-x", true, Match{Kind: MatchNone}},
		{"a known peer is matched without reading", append(cards[:1:1], card("n", "e1", "", state.PhaseSent)), "e1", "peer-1", false, Match{MatchCard, "a"}},
		{"unknown session on another endpoint does not matter", append(cards[:1:1], card("n", "e2", "", state.PhaseSent)), "e1", "session-x", false, Match{Kind: MatchNone}},
		{"oldest of two on one peer answers", []state.Card{card("a", "e", "p", state.PhaseSent), card("b", "e", "p", state.PhaseSent)}, "e", "p", false, Match{MatchCard, "a"}},
	}
	for _, tc := range cases {
		if got := MatchReply(tc.cards, tc.endpoint, tc.from, tc.reread); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestQueue(t *testing.T) {
	cards := []state.Card{
		card("a", "e", "p", state.PhaseSent),
		card("b", "e", "p", state.PhaseNew),
		card("c", "e", "p", state.PhaseNew),
		card("d", "e", "other", state.PhaseNew),
		card("f", "e2", "p", state.PhaseNew),
		card("g", "e", "", state.PhaseNew),
		card("h", "e", "p", state.PhaseBlocked),
	}
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"b", true},  // a waits on the same peer
		{"d", false}, // other peer
		{"f", false}, // other endpoint
		{"g", false}, // a new session has no peer: it never waits
		{"a", false}, // nothing ahead of the waiting card itself
	} {
		var c state.Card
		for _, x := range cards {
			if x.ID == tc.id {
				c = x
			}
		}
		if got := ShouldQueue(cards, c); got != tc.want {
			t.Errorf("ShouldQueue(%s) = %v, want %v", tc.id, got, tc.want)
		}
	}

	if got := NextQueued(cards, "e", "p"); got != "" {
		t.Errorf("a card still waits, got %q", got)
	}
	cards[0].Phase = state.PhaseFinished
	if got := NextQueued(cards, "e", "p"); got != "b" {
		t.Errorf("the oldest queued card goes first, got %q", got)
	}
	cards[1].Phase = state.PhaseAttempting
	if got := NextQueued(cards, "e", "p"); got != "" {
		t.Errorf("b sends now, c waits behind it, got %q", got)
	}
}

func TestOnReady(t *testing.T) {
	window := 3 * time.Hour
	mk := func(phase state.Phase, mut func(*state.Card)) *state.Card {
		c := &state.Card{ID: "t", Phase: phase, Attempt: 2, AttemptAt: ago(time.Hour)}
		if mut != nil {
			mut(c)
		}
		return c
	}
	cases := []struct {
		name string
		card *state.Card
		want Action
	}{
		{"unknown card", nil, Action{Kind: ActionNew}},
		{"finished card", mk(state.PhaseFinished, nil), Action{Kind: ActionIgnore}},
		{"unblocked invalid card", mk(state.PhaseBlocked, func(c *state.Card) { c.BlockedFor = state.CategoryInvalidCard }), Action{Kind: ActionRevalidate}},
		{"unblocked after no reply", mk(state.PhaseBlocked, func(c *state.Card) { c.BlockedFor = state.CategoryNoReply }), Action{Kind: ActionKeepWaiting, ResetTimer: true}},
		{"unblocked after a transient failure", mk(state.PhaseBlocked, func(c *state.Card) { c.BlockedFor = state.CategoryTransient }), Action{Kind: ActionSend, Attempt: 3}},
		{"never attempted", mk(state.PhaseNew, func(c *state.Card) { c.Attempt = 0 }), Action{Kind: ActionSend, Attempt: 1}},
		{"attempt inside the window keeps its number", mk(state.PhaseAttempting, nil), Action{Kind: ActionSend, Attempt: 2}},
		{"attempt exactly at the window", mk(state.PhaseAttempting, func(c *state.Card) { c.AttemptAt = ago(window) }), Action{Kind: ActionSend, Attempt: 2}},
		{"attempt past the window is not repeated", mk(state.PhaseAttempting, func(c *state.Card) { c.AttemptAt = ago(window + time.Second) }), Action{Kind: ActionOutcomeUnknown}},
		{"already sent", mk(state.PhaseSent, nil), Action{Kind: ActionKeepWaiting}},
	}
	for _, tc := range cases {
		if got := OnReady(tc.card, t0, window); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestOnFailure(t *testing.T) {
	fresh := state.Card{}
	blockedBefore := func(cat state.Category) state.Card { return state.Card{Blocks: map[state.Category]int{cat: 1}} }
	cases := []struct {
		name   string
		card   state.Card
		reason Reason
		want   Verdict
	}{
		{"first invalid", fresh, ReasonInvalidCard, VerdictBlock},
		{"first delivery failure", fresh, ReasonDeliveryFailed, VerdictBlock},
		{"first no reply", fresh, ReasonNoReply, VerdictBlock},
		{"second delivery failure", blockedBefore(state.CategoryTransient), ReasonDeliveryFailed, VerdictComplete},
		{"unknown outcome after a delivery failure counts as the same kind", blockedBefore(state.CategoryTransient), ReasonOutcomeUnknown, VerdictComplete},
		{"closed session after a delivery failure", blockedBefore(state.CategoryTransient), ReasonSessionClosed, VerdictComplete},
		{"second no reply", blockedBefore(state.CategoryNoReply), ReasonNoReply, VerdictComplete},
		{"second invalid", blockedBefore(state.CategoryInvalidCard), ReasonInvalidCard, VerdictComplete},
		{"another kind is still a first block", blockedBefore(state.CategoryNoReply), ReasonDeliveryFailed, VerdictBlock},
	}
	for _, tc := range cases {
		if got := OnFailure(tc.card, tc.reason); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNoReplyDue(t *testing.T) {
	timeout := 24 * time.Hour
	c := state.Card{Phase: state.PhaseSent, WaitingSince: ago(timeout)}
	if !NoReplyDue(c, t0, timeout) {
		t.Error("due exactly at the timeout")
	}
	c.WaitingSince = ago(timeout - time.Second)
	if NoReplyDue(c, t0, timeout) {
		t.Error("not due yet")
	}
	c.WaitingSince, c.Phase = ago(48*time.Hour), state.PhaseBlocked
	if NoReplyDue(c, t0, timeout) {
		t.Error("a blocked card is not timed")
	}
}

func TestIdempotencyKeyDiffersByAttempt(t *testing.T) {
	if IdempotencyKey("t_1", 1) == IdempotencyKey("t_1", 2) || IdempotencyKey("t_1", 1) == IdempotencyKey("t_2", 1) {
		t.Error("keys collide")
	}
}

func TestSessionsToClose(t *testing.T) {
	p := SessionPolicy{CloseWindow: time.Hour, Retention: 7 * 24 * time.Hour, Max: 3}
	sess := func(addr, endpoint string, idle time.Duration) state.Session {
		return state.Session{Address: addr, Endpoint: endpoint, LastUse: ago(idle)}
	}
	chat, cron, orphan := "chat:p:x:y", "cron:p:j", "orphan"
	inUse := func(addr string) state.Card {
		return state.Card{ID: "u-" + addr, Peer: addr, Session: true, Phase: state.PhaseSent}
	}
	cases := []struct {
		name     string
		sessions []state.Session
		cards    []state.Card
		want     []string
	}{
		{"nothing is due", []state.Session{sess("a", chat, time.Hour), sess("b", cron, 59*time.Minute)}, nil, nil},
		{"a cron session closes after the close window", []state.Session{sess("a", cron, time.Hour)}, nil, []string{"a"}},
		{"an orphan session closes after the close window", []state.Session{sess("a", orphan, 2*time.Hour)}, nil, []string{"a"}},
		{"a conversation session waits for the retention", []state.Session{sess("a", chat, 6*24*time.Hour), sess("b", chat, 7*24*time.Hour)}, nil, []string{"b"}},
		{"a session in use is kept", []state.Session{sess("a", cron, 30*24*time.Hour)}, []state.Card{inUse("a")}, nil},
		{"a blocked card still uses its session", []state.Session{sess("a", chat, 30*24*time.Hour)}, []state.Card{{Peer: "a", Session: true, Phase: state.PhaseBlocked}}, nil},
		{"a finished card does not", []state.Session{sess("a", cron, 2*time.Hour)}, []state.Card{{Peer: "a", Session: true, Phase: state.PhaseFinished}}, []string{"a"}},
		{"a fixed address is no session", []state.Session{sess("a", cron, 2*time.Hour)}, []state.Card{{Peer: "a", Phase: state.PhaseSent}}, []string{"a"}},
		{"over the cap the least recently used go, whatever their window",
			[]state.Session{sess("a", chat, 4*time.Hour), sess("b", chat, 3*time.Hour), sess("c", chat, 2*time.Hour), sess("d", chat, time.Hour), sess("e", chat, time.Minute)},
			nil, []string{"a", "b"}},
		{"sessions already due count toward the cap",
			[]state.Session{sess("a", cron, 2*time.Hour), sess("b", chat, 3*time.Hour), sess("c", chat, 2*time.Hour), sess("d", chat, time.Hour)},
			nil, []string{"a"}},
		{"over the cap, a session in use is not closed to make room",
			[]state.Session{sess("a", chat, 4*time.Hour), sess("b", chat, 3*time.Hour), sess("c", chat, 2*time.Hour), sess("d", chat, time.Hour)},
			[]state.Card{inUse("a")}, []string{"b"}},
		{"a close not confirmed is not asked again at once but still counts as open",
			[]state.Session{{Address: "a", Endpoint: cron, LastUse: ago(5 * time.Hour), CloseTriedAt: ago(time.Minute)}, sess("b", chat, 3*time.Hour), sess("c", chat, 2*time.Hour), sess("d", chat, time.Hour)},
			nil, []string{"b"}},
		{"a close not confirmed is asked again later",
			[]state.Session{{Address: "a", Endpoint: cron, LastUse: ago(5 * time.Hour), CloseTriedAt: ago(CloseRetryInterval)}},
			nil, []string{"a"}},
	}
	for _, tc := range cases {
		got := SessionsToClose(tc.sessions, tc.cards, p, t0)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRouteFollowUp(t *testing.T) {
	open := []state.Session{{Address: "sess-1"}}
	cases := []struct {
		name    string
		parents []state.Card
		want    Route
		reason  Reason
	}{
		{"a remote session that is still open", []state.Card{{Endpoint: "e", Peer: "sess-1", Session: true, Alias: "codex"}}, Route{Endpoint: "e", Peer: "sess-1", Session: true, Alias: "codex"}, 0},
		{"a remote session that was closed", []state.Card{{Endpoint: "e", Peer: "sess-2", Session: true}}, Route{}, ReasonSessionClosed},
		{"a fixed address needs no session", []state.Card{{Endpoint: "e", Peer: "fixed"}}, Route{Endpoint: "e", Peer: "fixed"}, 0},
		{"the first parent that has a peer", []state.Card{{Endpoint: "e"}, {Endpoint: "e2", Peer: "fixed"}}, Route{Endpoint: "e2", Peer: "fixed"}, 0},
		{"no parent has a peer", []state.Card{{Endpoint: "e"}}, Route{}, ReasonInvalidCard},
		{"no parent known", nil, Route{}, ReasonInvalidCard},
	}
	for _, tc := range cases {
		got, reason, ok := RouteFollowUp(tc.parents, open)
		if got != tc.want || reason != tc.reason || ok != (tc.reason == 0) {
			t.Errorf("%s: got %+v %v %v", tc.name, got, reason, ok)
		}
	}
}

func TestPlaceEndpoint(t *testing.T) {
	ep := func(key string, idle time.Duration) state.Endpoint {
		return state.Endpoint{Key: key, LastActivity: ago(idle)}
	}
	// max 5 leaves 3 conversation endpoints: the instance and the orphan are reserved.
	full := []state.Endpoint{ep("a", 3*time.Hour), ep("b", 5*time.Hour), ep("c", 4*time.Hour), ep(OrphanKey, 10*time.Hour)}
	busy := func(endpoint string) state.Card { return state.Card{Endpoint: endpoint, Phase: state.PhaseSent} }
	cases := []struct {
		name      string
		key       string
		endpoints []state.Endpoint
		sessions  []state.Session
		cards     []state.Card
		want      Placement
	}{
		{"a registered key is used as it is", "a", full, nil, nil, Placement{Key: "a"}},
		{"the orphan is always available", OrphanKey, full, nil, nil, Placement{Key: OrphanKey}},
		{"room left", "new", full[:2], nil, nil, Placement{Key: "new"}},
		{"the orphan does not take a conversation's slot", "new", []state.Endpoint{ep("a", 0), ep("b", 0), ep(OrphanKey, 0)}, nil, nil, Placement{Key: "new"}},
		{"full: the endpoint idle longest is retired", "new", full, nil, nil, Placement{Key: "new", Retire: "b"}},
		{"full: one with an unfinished card is kept", "new", full, nil, []state.Card{busy("b")}, Placement{Key: "new", Retire: "c"}},
		{"full: a finished card does not keep it", "new", full, nil, []state.Card{{Endpoint: "b", Phase: state.PhaseFinished}}, Placement{Key: "new", Retire: "b"}},
		{"full: one that created an open session is kept", "new", full, []state.Session{{Address: "s", Endpoint: "b"}}, nil, Placement{Key: "new", Retire: "c"}},
		{"full and nothing can go: send from the orphan", "new", full, nil, []state.Card{busy("a"), busy("b"), busy("c")}, Placement{Key: OrphanKey}},
	}
	for _, tc := range cases {
		got := PlaceEndpoint(tc.key, tc.endpoints, tc.sessions, tc.cards, 5)
		reason := got.Reason
		got.Reason = ""
		if got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
		if (reason != "") != (tc.want.Key == OrphanKey && tc.key != OrphanKey) {
			t.Errorf("%s: reason %q", tc.name, reason)
		}
	}
}

func TestIdleEndpoints(t *testing.T) {
	idle := 30 * 24 * time.Hour
	eps := []state.Endpoint{
		{Key: "old", LastActivity: ago(idle + time.Hour)},
		{Key: "old-busy", LastActivity: ago(idle + time.Hour)},
		{Key: "old-sessions", LastActivity: ago(idle + time.Hour)},
		{Key: "recent", LastActivity: ago(idle - time.Hour)},
		{Key: OrphanKey, LastActivity: ago(idle * 2)},
	}
	cards := []state.Card{{Endpoint: "old-busy", Phase: state.PhaseBlocked}}
	sessions := []state.Session{{Address: "s", Endpoint: "old-sessions"}}
	got := IdleEndpoints(eps, sessions, cards, idle, t0)
	if len(got) != 1 || got[0] != "old" {
		t.Errorf("got %v", got)
	}
}

func TestParentFor(t *testing.T) {
	cs := []Candidate{
		{ID: "a", Done: true, DoneAt: ago(3 * time.Hour)},
		{ID: "b", Done: true, DoneAt: ago(time.Hour)},
		{ID: "c", Done: false, DoneAt: ago(time.Minute)},
		{ID: "d", Done: true, DoneAt: ago(2 * time.Hour)},
	}
	for _, tc := range []struct {
		name string
		key  string
		cs   []Candidate
		want string
	}{
		{"the most recent done card", "chat:p:x:y", cs, "b"},
		{"a card that is not done is no parent", "chat:p:x:y", cs[2:3], ""},
		{"no cards", "chat:p:x:y", nil, ""},
		{"a cron job's latest card", "cron:p:j", cs, "b"},
		{"the orphan has none", OrphanKey, cs, ""},
	} {
		if got := ParentFor(tc.key, tc.cs); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
