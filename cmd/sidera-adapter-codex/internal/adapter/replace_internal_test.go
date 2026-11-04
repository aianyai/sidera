package adapter

import (
	"testing"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The server is replaced only when every session of its own is idle, and idle is
// strict: a thread the adapter has not seen in this connection, or in a state it
// does not know, is busy. An attached session runs in a person's own Codex and a
// lost thread runs nowhere, so neither holds the replacement back.
func TestBusyReason(t *testing.T) {
	mk := func(change func(*session)) *session {
		s := newSession("ep", "dev", "th", nil)
		if change != nil {
			change(s)
		}
		return s
	}
	for _, tc := range []struct {
		name     string
		sessions []*session
		busy     bool
	}{
		{"no session", nil, false},
		{"idle", []*session{mk(nil)}, false},
		{"thread not loaded in this connection", []*session{mk(func(s *session) { s.status = appserver.ThreadNotLoaded })}, true},
		{"thread active", []*session{mk(func(s *session) { s.status = appserver.ThreadActive })}, true},
		{"thread in a state the adapter does not know", []*session{mk(func(s *session) { s.status = "systemError" })}, true},
		{"a turn is reported running", []*session{mk(func(s *session) { s.activeTurn = "tu" })}, true},
		{"a turn start is being answered", []*session{mk(func(s *session) { s.pending = &pendingStart{} })}, true},
		{"the session is being created", []*session{mk(func(s *session) { s.creating = true })}, true},
		{"one idle, one busy", []*session{mk(nil), mk(func(s *session) { s.activeTurn = "tu" })}, true},
		{"attached session in any state", []*session{mk(func(s *session) {
			s.attached, s.status, s.activeTurn = true, appserver.ThreadNotLoaded, "tu"
		})}, false},
		{"lost thread", []*session{mk(func(s *session) { s.lost, s.status = true, appserver.ThreadNotLoaded })}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := New(Options{})
			for i, s := range tc.sessions {
				s.endpoint = adapterproto.EndpointRef("ep" + string(rune('a'+i)))
				a.sessions[s.endpoint] = s
			}
			if why := a.busyReason(); (why != "") != tc.busy {
				t.Fatalf("busyReason = %q, want busy %v", why, tc.busy)
			}
		})
	}
}
