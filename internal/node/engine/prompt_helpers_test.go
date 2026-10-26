package engine_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

// The helpers of the tests of prompts. What was sent into a session is read from the
// ledger the fake adapter wrote when the request reached it, what the runtime did
// from the ledger of the runtime, and what the node holds from the records on the
// disk.

// endpointOfAddr is the endpoint of an address.
func endpointOfAddr(t testing.TB, addr string) ident.EndpointID {
	t.Helper()
	a, err := ident.ParseAddress(addr)
	if err != nil {
		t.Fatalf("the address %q: %v", addr, err)
	}
	return a.Endpoint
}

// nativeRef is the native session the session endpoint at addr maps to.
func nativeRef(t testing.TB, h *harness.H, addr string) string {
	t.Helper()
	id := endpointOfAddr(t, addr)
	for _, ep := range sessionEndpoints(t, h) {
		if ep.ID == id && ep.Session != nil && ep.Session.NativeRef != "" {
			return ep.Session.NativeRef
		}
	}
	t.Fatalf("the node holds no native session for %s", addr)
	return ""
}

// runtimeOfAddr is the fake runtime that holds the native session of the session
// endpoint at addr: the one of the instance the endpoint belongs to.
func runtimeOfAddr(t testing.TB, h *harness.H, addr string) fakeruntime.Controller {
	t.Helper()
	id := endpointOfAddr(t, addr)
	for _, ep := range sessionEndpoints(t, h) {
		if ep.ID == id {
			rt, err := h.RuntimeOf(ep.Instance)
			if err != nil {
				t.Fatal(err)
			}
			return rt
		}
	}
	t.Fatalf("the node holds no session endpoint %s", addr)
	return nil
}

// endTurn ends the turn the native session of the endpoint is running, as its model
// finishing it does, and waits for the node to settle.
func endTurn(t testing.TB, h *harness.H, addr string) {
	t.Helper()
	rt := runtimeOfAddr(t, h, addr)
	if err := rt.EndTurn(nativeRef(t, h, addr), fakeruntime.FinalCompleted); err != nil {
		t.Fatalf("ending the turn of %s: %v", addr, err)
	}
	quiet(t, h)
}

// holdBackTurn makes the next prompt into the session of the endpoint reach the runtime
// without starting a turn, until the test starts it (fakeruntime Runtime.StartTurn). A
// turn that begins for a prompt is reported with the prompt's attempt and proves that it
// was delivered, so a test of a prompt whose outcome the node must find out some other way
// holds the turn back: the runtime has the prompt and the node has heard nothing of a turn.
func holdBackTurn(t testing.TB, h *harness.H, addr string) {
	t.Helper()
	if err := runtimeOfAddr(t, h, addr).SetPlan(nativeRef(t, h, addr), fakeruntime.TurnPlan{ManualStart: true}); err != nil {
		t.Fatalf("holding back the next turn of %s: %v", addr, err)
	}
}

// idleSession is newSession with the first turn of the session ended. The first
// prompt of a session starts a turn at once, and no prompt goes into a session while
// a turn runs, so a test of what wakes a session starts from one that is idle.
func idleSession(t testing.TB, h *harness.H, from string) (fakeagent.Agent, string) {
	t.Helper()
	return idleSessionOn(t, h, harness.Instance, from)
}

// idleSessionOn is idleSession on another instance of the fixture.
func idleSessionOn(t testing.TB, h *harness.H, instance, from string) (fakeagent.Agent, string) {
	t.Helper()
	agent, addr := newSessionOn(t, h, instance, from)
	endTurn(t, h, addr)
	return agent, addr
}

// promptsInto are the prompt.deliver requests that reached the adapter for the
// endpoint, in the order they arrived.
func promptsInto(t testing.TB, h *harness.H, addr string) []ledger.Entry {
	t.Helper()
	log, err := h.Ledger()
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	id := endpointOfAddr(t, addr).String()
	return log.Select(func(e ledger.Entry) bool {
		return e.Kind == ledger.KindPromptDeliverReceived && e.Attrs.Endpoint == id
	})
}

// wantPrompts fails the test unless n prompts have reached the adapter for the endpoint.
func wantPrompts(t testing.TB, h *harness.H, addr string, n int) {
	t.Helper()
	if got := len(promptsInto(t, h, addr)); got != n {
		t.Errorf("%d prompts were sent into %s, want %d", got, addr, n)
	}
}

// promptOrder are the endpoints the prompts reached the adapter for, in the order
// they came, over every session.
func promptOrder(t testing.TB, h *harness.H) []string {
	t.Helper()
	log, err := h.Ledger()
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	var out []string
	for _, e := range log.Kind(ledger.KindPromptDeliverReceived) {
		out = append(out, e.Attrs.Endpoint)
	}
	return out
}

// turnsStarted is how many turns the native session of the endpoint has started.
func turnsStarted(t testing.TB, h *harness.H, addr string) int {
	t.Helper()
	log, err := h.Ledger()
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	writer := "runtime/" + nativeRef(t, h, addr)
	return len(log.Select(func(e ledger.Entry) bool { return e.Kind == ledger.KindRuntimeTurnStarted && e.Writer == writer }))
}

// attemptsOf is the record of the prompt attempts of the endpoint on the disk.
func attemptsOf(t testing.TB, h *harness.H, addr string) store.AttemptsRecord {
	t.Helper()
	id := endpointOfAddr(t, addr)
	for _, a := range scanOf(t, h).Attempts {
		if a.Endpoint == id {
			return a
		}
	}
	return store.AttemptsRecord{Endpoint: id}
}

// attemptKinds are the kinds of the prompts that were delivered to the endpoint, in
// order.
func attemptKinds(t testing.TB, h *harness.H, addr string) []handoff.PromptKind {
	t.Helper()
	var kinds []handoff.PromptKind
	for _, a := range attemptsOf(t, h, addr).Attempts {
		if a.Outcome == handoff.AttemptDelivered {
			kinds = append(kinds, a.Kind)
		}
	}
	return kinds
}

// runtimeReceived says whether the runtime of the endpoint's native session has the prompt
// of the attempt.
func runtimeReceived(t testing.TB, h *harness.H, addr, attempt string) bool {
	t.Helper()
	log, err := h.Ledger()
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	writer := "runtime/" + nativeRef(t, h, addr)
	return len(log.Select(func(e ledger.Entry) bool {
		return e.Kind == ledger.KindRuntimePromptReceived && e.Writer == writer && e.Attrs.Attempt == attempt
	})) > 0
}
