package engine_test

import (
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Creation's first prompt is a delivered message prompt only when the creation
// carries a body. Its unread barrier survives a restart, and reading it does not
// prevent a later message from waking the session normally. The restart finds the
// session idle with the prompt unread, which counts as a turn end seen once: the
// initial message's prompt is repeated one time.
func TestCreationFirstPromptIsCountedAndRecoveredOnlyWithAnInitialMessage(t *testing.T) {
	for _, body := range []string{"", "the initial message stays behind recv"} {
		name := "without initial message"
		if body != "" {
			name = "with initial message"
		}
		t.Run(name, func(t *testing.T) {
			h := sessionNode(t, harness.Options{}, "sender")
			rt, err := h.Runtime()
			if err != nil {
				t.Fatal(err)
			}
			// A persisted first prompt is proof of delivery even before its turn
			// starts. Busy state must not conceal a missing unread barrier.
			if err := rt.SetFirstTurn(fakeruntime.TurnPlan{ManualStart: true}); err != nil {
				t.Fatal(err)
			}
			msg := signal(t, h, "sender", h.RuntimeAddr(), adapterproto.SignalSessionCreate, body)
			quiet(t, h)
			ep := sessionEndpoints(t, h)[0]
			addr := addressOf(ep.ID)
			wantText, wantCount := handoff.NewSessionPromptText, 0
			if body != "" {
				wantText, wantCount = handoff.PromptText(1), 1
			}
			log, err := h.Ledger()
			if err != nil {
				t.Fatal(err)
			}
			prompts := log.Kind(ledger.KindRuntimePromptReceived)
			if len(prompts) != 1 || prompts[0].Attrs.TextDigest != ledger.Digest([]byte(wantText)) {
				t.Errorf("native first prompts: %+v, want only the body-free text %q", prompts, wantText)
			}
			check := func(wantCount, wantRepeats int) {
				t.Helper()
				head := attemptsOf(t, h, addr)
				if head.InFlight != nil || len(head.Attempts) != wantCount {
					t.Fatalf("first-prompt attempts: %+v, want %d delivered attempts", head, wantCount)
				}
				if body != "" {
					at := ctlRecords(t, h)[0].Record.DeliveredAt()
					if a := head.Attempts[0]; a.Outcome != handoff.AttemptDelivered || a.Kind != handoff.PromptKindFirst || !a.At.Equal(at) {
						t.Errorf("first prompt %+v, want delivered first at %s", a, at)
					}
					if in := inOf(t, h, msg); in.Record.Sub != delivery.SubNotified {
						t.Errorf("initial delivery is not notified: %+v", in.Record)
					}
				}
				wantPrompts(t, h, addr, wantRepeats)
			}
			check(wantCount, 0)
			h.Crash(harness.ProcessCrash)
			quiet(t, h)
			repeats := wantCount // one repeat of the unread initial message, none without one
			check(wantCount+repeats, repeats)
			if err := rt.StartTurn(ep.Session.NativeRef); err != nil {
				t.Fatal(err)
			}
			quiet(t, h)
			h.Advance(time.Second)
			agent := h.Session(ep.ID.String())
			page := recvAs(t, h, agent, fakeagent.RecvInput{})
			if body != "" {
				wantOnly(t, page, msg)
				ackAs(t, h, agent, page.Page.Lease)
			} else {
				wantEmpty(t, page)
			}
			endTurn(t, h, addr)
			wantPrompts(t, h, addr, repeats)
			sendToAddr(t, h, "sender", addr, "the next real message")
			quiet(t, h)
			wantPrompts(t, h, addr, repeats+1)
			if got := len(attemptsOf(t, h, addr).Attempts); got != wantCount+repeats+1 {
				t.Errorf("%d attempts after the next message, want %d", got, wantCount+repeats+1)
			}
		})
	}
}

// A creation whose answer is unknown after the native session exists lets the
// session run its first turn while the endpoint is still being created. That turn
// ends before the creation settles, and the settlement then records the first
// prompt as delivered and unread. The end of the turn was observed and counts once
// the endpoint opens: the unread prompt is repeated, as for a turn that ended after
// opening, so the initial message is not left waiting for an end that already came.
func TestTurnEndedWhileCreatingCountsOnceTheEndpointOpens(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "sender")
	rt, err := h.Runtime()
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.SetFirstTurn(fakeruntime.TurnPlan{ManualStart: true}); err != nil {
		t.Fatal(err)
	}
	ctrl, err := h.Adapter(harness.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.QueueOutcome(h.Ctx(), fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeIndeterminate, AfterEffect: true}); err != nil {
		t.Fatal(err)
	}
	msg := signal(t, h, "sender", h.RuntimeAddr(), adapterproto.SignalSessionCreate, "the initial message")
	quiet(t, h)
	eps := sessionEndpoints(t, h)
	if len(eps) != 1 || eps[0].Status != store.EndpointCreating {
		t.Fatalf("endpoints %+v, want one creating", eps)
	}
	lg, err := h.Ledger()
	if err != nil {
		t.Fatal(err)
	}
	created := lg.Kind(ledger.KindNativeCreateReturned)
	if len(created) != 1 {
		t.Fatalf("%d native sessions, want 1", len(created))
	}
	ref := created[0].Attrs.NativeRef
	if err := rt.StartTurn(ref); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	if err := rt.EndTurn(ref, fakeruntime.FinalCompleted); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	h.Advance(engine.DefaultParams().CreateRetryBackoff + time.Second)
	quiet(t, h)
	eps = sessionEndpoints(t, h)
	if len(eps) != 1 || eps[0].Status != store.EndpointOpen {
		t.Fatalf("endpoints %+v, want one open", eps)
	}
	addr := addressOf(eps[0].ID)
	wantPrompts(t, h, addr, 1)
	if got := attemptKinds(t, h, addr); !slices.Equal(got, []handoff.PromptKind{handoff.PromptKindFirst, handoff.PromptKindReprompt}) {
		t.Errorf("delivered prompt kinds %v, want the first prompt and one repeat", got)
	}
	agent := h.Session(eps[0].ID.String())
	page := recvAs(t, h, agent, fakeagent.RecvInput{})
	wantOnly(t, page, msg)
}

// A creation whose answer is lost and which the node starts again after a restart
// is answered from the adapter's stored state: the session exists and its first
// turn is over, a fact only the earlier process had seen. The answer says so
// (first_turn_ended), and the endpoint opens with the unread first prompt repeated
// once, not left waiting for an end that will never be reported again.
func TestCreationSettledWithFirstTurnEndedAfterARestartRepeatsTheUnreadPromptOnce(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "sender")
	rt, err := h.Runtime()
	if err != nil {
		t.Fatal(err)
	}
	// The first turn never starts in the runtime: the end is known only from the answer.
	if err := rt.SetFirstTurn(fakeruntime.TurnPlan{ManualStart: true}); err != nil {
		t.Fatal(err)
	}
	ctrl, err := h.Adapter(harness.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.QueueOutcome(h.Ctx(), fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeIndeterminate, AfterEffect: true}); err != nil {
		t.Fatal(err)
	}
	msg := signal(t, h, "sender", h.RuntimeAddr(), adapterproto.SignalSessionCreate, "the initial message")
	quiet(t, h)
	if eps := sessionEndpoints(t, h); len(eps) != 1 || eps[0].Status != store.EndpointCreating {
		t.Fatalf("endpoints %+v, want one creating", eps)
	}
	h.Crash(harness.ProcessCrash)
	if ctrl, err = h.Adapter(harness.Instance); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.QueueOutcome(h.Ctx(), fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeFirstTurnEnded}); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	h.Advance(engine.DefaultParams().CreateRetryBackoff + time.Second)
	quiet(t, h)
	eps := sessionEndpoints(t, h)
	if len(eps) != 1 || eps[0].Status != store.EndpointOpen {
		t.Fatalf("endpoints %+v, want one open", eps)
	}
	addr := addressOf(eps[0].ID)
	wantPrompts(t, h, addr, 1)
	if got := attemptKinds(t, h, addr); !slices.Equal(got, []handoff.PromptKind{handoff.PromptKindFirst, handoff.PromptKindReprompt}) {
		t.Errorf("delivered prompt kinds %v, want the first prompt and one repeat", got)
	}
	wantOnly(t, recvAs(t, h, h.Session(eps[0].ID.String()), fakeagent.RecvInput{}), msg)
}
