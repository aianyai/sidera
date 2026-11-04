package adapter_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/adapter"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// seedSessions seeds n idle threads, the last of which runs a turn, and returns
// their attachments (the endpoints are 32 hex digits, as the protocol wants).
func seedSessions(codex *fakeCodex, n int) []adapterproto.SessionAttach {
	access := &adapterproto.ToolAccess{Env: map[string]string{"SIDERA_TOKEN": token, "SIDERA_HOME": dataDir}}
	var out []adapterproto.SessionAttach
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("th-%d", i)
		if i == n {
			codex.seed(id, &fakeTurn{id: "tu-running", status: "inProgress", clientID: attempt1})
		} else {
			codex.seed(id)
		}
		out = append(out, adapterproto.SessionAttach{
			Endpoint: adapterproto.EndpointRef(fmt.Sprintf("%032x", i)), Preset: "dev", NativeRef: adapterproto.NativeRef(id), ToolAccess: access,
		})
	}
	return out
}

func turnStartedOf(turn string) func(json.RawMessage) bool {
	return func(r json.RawMessage) bool {
		return decode[adapterproto.TurnStartedParams](r).Turn == adapterproto.TurnRef(turn)
	}
}

// adapter.start answers by its deadline however many sessions are slow to load.
// The ones not loaded by then are unreachable for now, as when the server cannot be
// reached; they are loaded afterwards, each thread once, and a turn found running
// in one of them is reported started.
func TestStartAnswersByItsDeadlineWithManySlowSessions(t *testing.T) {
	const sessions = 10
	codex := newFakeCodex(t)
	// The fake server answers one request at a time, so the first session is loaded
	// after four resumes and all of them after ten.
	codex.resumeDelay = 400 * time.Millisecond
	h := newHarnessWith(t, codex, func(o *adapter.Options) { o.StartWait = 2 * time.Second })

	began := time.Now()
	res := h.start(seedSessions(codex, sessions)...)
	// Loading all ten sessions takes about four seconds, so an answer that waited
	// for them is told from one at the deadline with a second to spare for a slow
	// machine.
	if took := time.Since(began); took > 3*time.Second {
		t.Errorf("adapter.start took %v: it waited for the sessions instead of answering at its deadline", took)
	}
	if len(res.Sessions) != sessions {
		t.Fatalf("start = %+v, want %d sessions", res, sessions)
	}
	if got := res.Sessions[0].State; got != adapterproto.AttachAttached {
		t.Errorf("the first session is %q, want attached: it was loaded in time; start = %+v after %v", got, res, time.Since(began))
	}
	if last := res.Sessions[sessions-1]; last.State != adapterproto.AttachUnreachable || last.TurnActive {
		t.Errorf("the last session = %+v, want unreachable with no turn: it was not loaded in time", last)
	}

	h.waitNote(adapterproto.MethodTurnStarted, turnStartedOf("tu-running"))
	deadline := time.Now().Add(10 * time.Second)
	for len(codex.callsOf("thread/resume")) < sessions {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d sessions were loaded", len(codex.callsOf("thread/resume")), sessions)
		}
		time.Sleep(20 * time.Millisecond)
	}
	seen := map[string]int{}
	for _, c := range codex.callsOf("thread/resume") {
		seen[decode[struct{ ThreadID string }](c.Params).ThreadID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("thread %s was resumed %d times, want once", id, n)
		}
	}
	if n := h.countNotes(adapterproto.MethodTurnStarted); n != 1 {
		t.Errorf("%d turn.started notifications, want the one turn reported once", n)
	}
}

// A server that takes longer to be reached than adapter.start may wait is
// answered as one that cannot be reached; when it is reached the sessions are
// loaded, the availability clears and the turn found running is reported.
func TestStartAnswersBeforeTheServerIsReachedAndLoadsTheSessionsWhenItIs(t *testing.T) {
	codex := newFakeCodex(t)
	h := newHarnessWith(t, codex, func(o *adapter.Options) { o.StartWait = 200 * time.Millisecond })
	h.mgr.gate = make(chan struct{})

	res := h.start(seedSessions(codex, 2)...)
	for _, s := range res.Sessions {
		if s.State != adapterproto.AttachUnreachable || s.TurnActive {
			t.Fatalf("start = %+v, want every session unreachable", res)
		}
	}
	if c := decode[adapterproto.AvailabilityParams](h.lastAvailability()).Conditions; len(c) != 1 || c[0].Condition != adapterproto.ConditionNativeUnavailable {
		t.Fatalf("availability = %s, want native_unavailable", h.lastAvailability())
	}

	close(h.mgr.gate)
	h.waitNote(adapterproto.MethodTurnStarted, turnStartedOf("tu-running"))
	deadline := time.Now().Add(10 * time.Second)
	for len(decode[adapterproto.AvailabilityParams](h.lastAvailability()).Conditions) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("availability never cleared: %s", h.lastAvailability())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
