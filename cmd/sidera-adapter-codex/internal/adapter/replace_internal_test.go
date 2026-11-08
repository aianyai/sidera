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
		s := newSession("ep", "dev", "th", nil, nil)
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

// A daemon that is stopping takes the App Server with it only when it can be shown
// that nothing uses it: the replacement gate is free, the adapter has been started
// and holds an open connection, and every session is idle. When the answer is yes
// the gate stays taken, so that no turn starts in the server that is about to end;
// when it is no the gate is given back.
func TestEndIdleServer(t *testing.T) {
	for _, tc := range []struct {
		name       string
		started    bool
		connected  bool
		gateTaken  bool
		turnActive bool
		end        bool
	}{
		{"idle", true, true, false, false, true},
		{"a turn runs", true, true, false, true, false},
		{"an operation holds the gate", true, true, true, false, false},
		{"not started", false, true, false, false, false},
		{"no connection", true, false, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := New(Options{})
			a.started = tc.started
			if tc.connected {
				a.client = &appserver.Client{}
			}
			s := newSession("ep", "dev", "th", nil, nil)
			if tc.turnActive {
				s.activeTurn = "tu"
			}
			a.sessions[s.endpoint] = s
			if tc.gateTaken {
				a.replaceGate.RLock()
			}

			if got := a.endIdleServer(); got != tc.end {
				t.Fatalf("endIdleServer = %v, want %v", got, tc.end)
			}
			free := true
			if tc.gateTaken {
				a.replaceGate.RUnlock()
			}
			if !a.replaceGate.TryLock() {
				free = false
			}
			if free == tc.end {
				t.Fatalf("the gate is free: %v after an answer of %v, want it taken exactly when the server ends", free, tc.end)
			}
		})
	}
}
