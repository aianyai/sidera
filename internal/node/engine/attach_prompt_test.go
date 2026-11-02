package engine_test

import (
	"fmt"
	"testing"

	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// personsSession is a session a person opened in the fake runtime, attached to the
// node and idle. n makes its references differ from those of the other sessions of
// the test. It returns the address of the attached endpoint.
func personsSession(t *testing.T, h *harness.H, n int) string {
	t.Helper()
	rt, err := h.Runtime()
	if err != nil {
		t.Fatal(err)
	}
	native, err := rt.(fakeruntime.Launcher).Create(h.Ctx(), fakeruntime.CreateRequest{
		Attempt: fmt.Sprintf("00000000-0000-4000-8000-%012d", n), Endpoint: fmt.Sprintf("person-%d", n), Preset: "default",
		FirstAttempt: fmt.Sprintf("00000000-0000-4000-8000-%012d", 100+n), FirstPrompt: "opened by a person",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.Engine().AttachSession(h.Ctx(), engine.AttachRequest{Adapter: harness.Instance, NativeRef: native.Ref()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Wake.State != engine.WakeReady {
		t.Fatalf("the person's session is attached with wake %+v", res.Wake)
	}
	quiet(t, h)
	// The turn of the person's first prompt is theirs; it ends before the node prompts.
	if err := rt.EndTurn(native.Ref(), fakeruntime.FinalCompleted); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	return addressOf(res.Endpoint.Endpoint)
}

// The cap on the turns that prompts have started (node_prompt_turns) limits the
// sessions the node runs. An attached session is the person's own, in their own
// runtime: its turn takes no place of the cap, and its prompts are not held back
// when the node is full.
func TestAnAttachedSessionIsOutsideTheNodePromptCap(t *testing.T) {
	h := sessionNode(t, harness.Options{NodeSettings: `{"node_prompt_turns":1}`}, "a")
	_, created := idleSession(t, h, "a")
	_, waiting := idleSession(t, h, "a")
	first, second := personsSession(t, h, 1), personsSession(t, h, 2)

	// The turn of an attached session runs, and the one place of the node is still free
	// for a session the node created.
	sendToAddr(t, h, "a", first, "for the person's session")
	quiet(t, h)
	wantPrompts(t, h, first, 1)
	sendToAddr(t, h, "a", created, "takes the place")
	quiet(t, h)
	wantPrompts(t, h, created, 1)

	// With the node full another created session waits, and an attached one does not.
	sendToAddr(t, h, "a", waiting, "waits for a place")
	quiet(t, h)
	wantPrompts(t, h, waiting, 0)
	sendToAddr(t, h, "a", second, "for another of the person's sessions")
	quiet(t, h)
	wantPrompts(t, h, second, 1)
}

// A session that comes back, as one the person resumed does, is prompted for what
// waited while it was away at the attachment itself, not at the end of the wait that
// follows a failed prompt.
func TestAReattachedSessionIsPromptedWithoutWaitingForTheRetry(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	addr := personsSession(t, h, 1)
	ctrl, err := h.Adapter(harness.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.QueueOutcome(h.Ctx(), fakeadapter.RequestPrompt, fakeadapter.Outcome{Kind: fakeadapter.OutcomeRefuse, Reason: string(adapterproto.RefusalNativeUnavailable)}); err != nil {
		t.Fatal(err)
	}
	sendToAddr(t, h, "a", addr, "for a session that is gone")
	quiet(t, h)
	wantPrompts(t, h, addr, 1) // the prompt that failed
	h.Advance(handoff.DefaultRetryAfterFailure / 2)
	quiet(t, h)
	wantPrompts(t, h, addr, 1)

	endpoint, err := ident.ParseAddress(addr)
	if err != nil {
		t.Fatal(err)
	}
	native := endpointRecord(t, scanOf(t, h), endpoint.Endpoint).Session.NativeRef
	// The same session attaches again; the refusal is spent, so the prompt gets through.
	if _, err := h.Engine().AttachSession(h.Ctx(), engine.AttachRequest{Adapter: harness.Instance, NativeRef: native}); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	wantPrompts(t, h, addr, 2)
}
