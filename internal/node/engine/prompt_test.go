package engine_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// A session endpoint is woken by a prompt: a text that says messages wait and carries
// none of them. These tests run one node over an adapter host with a fake adapter and
// one fake runtime, and judge what happened from the ledger the fakes write (which
// prompts reached the adapter and the runtime, which turns started), from the records
// on the disk and from the notices a sender is told. When a prompt is sent is the
// decision of package handoff; what is shown here is that the engine asks it at the
// moments that matter, sends no more than it allows, and never sends one twice.

// wantNoFeedback fails the test unless the sender has been told nothing of the message
// and holds nothing it still has to tell.
func wantNoFeedback(t testing.TB, h *harness.H, sender string, msg adapterproto.MessageID) {
	t.Helper()
	h.Advance(mergeWindow)
	quiet(t, h)
	if kinds := toldKinds(t, h, sender); len(kinds) != 0 {
		t.Errorf("%s was told %v, want nothing: no feedback means delivered", sender, kinds)
	}
	if kinds := feedbackOf(t, h, msg); len(kinds) != 0 {
		t.Errorf("the source record of %s is owed feedback %v, want none", msg, kinds)
	}
	if n := pending(t, h, sender); n != 0 {
		t.Errorf("%d feedback items wait for %s, want none", n, sender)
	}
}

// An agent that is busy, and one that does not read what it is woken for, is behaving
// as an agent: the sender is told nothing, and the message waits for the agent until
// its queue time is over.
func TestBusyOrUnreadAgentGetsNoFeedback(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	agent, addr := newSession(t, h, "a") // the first turn of a new session is running
	msg := sendToAddr(t, h, "a", addr, "for a session that is busy")
	quiet(t, h)
	h.Advance(10 * time.Minute)
	quiet(t, h)

	wantPrompts(t, h, addr, 0)
	wantNoFeedback(t, h, "a", msg)
	if in := inOf(t, h, msg); in.Record.State != delivery.StateAccepted || in.Record.Sub != delivery.SubNone {
		t.Errorf("the record is %s (%v), want accepted and not notified: no prompt was sent into a busy session", in.Record.State, in.Record.Sub)
	}

	// The turn ends and the message is unread: the agent is woken, and what it does
	// with the prompt is its own business.
	endTurn(t, h, addr)
	wantPrompts(t, h, addr, 1)
	h.Advance(10 * time.Minute)
	quiet(t, h)
	wantNoFeedback(t, h, "a", msg)

	wantOnly(t, recvAs(t, h, agent, fakeagent.RecvInput{}), msg)
}

// An agent that leaves a prompt unread is woken once more when its turn ends, and no
// more: a prompt it has decided to ignore must not become a loop of turns. Its message
// stays queued, marked as one a prompt was sent for.
func TestUnreadPromptRepromptedOnce(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	_, addr := idleSession(t, h, "a")
	msg := sendToAddr(t, h, "a", addr, "never read")
	quiet(t, h)
	wantPrompts(t, h, addr, 1)

	endTurn(t, h, addr) // the prompt started a turn that ended without a recv
	wantPrompts(t, h, addr, 2)
	endTurn(t, h, addr) // and again
	h.Advance(time.Minute)
	quiet(t, h)
	wantPrompts(t, h, addr, 2)

	if got, want := attemptKinds(t, h, addr), []handoff.PromptKind{handoff.PromptKindFirst, handoff.PromptKindReprompt}; !slices.Equal(got, want) {
		t.Errorf("the prompts delivered were %v, want %v", got, want)
	}
	if in := inOf(t, h, msg); in.Record.State != delivery.StateAccepted || in.Record.Sub != delivery.SubNotified {
		t.Errorf("the record is %s (%v), want accepted and notified", in.Record.State, in.Record.Sub)
	}
	wantNoFeedback(t, h, "a", msg)
}

// A message that arrives after the last prompt wakes the endpoint again once the
// minimum interval since that prompt has passed, and once: until then it waits, and
// no clock of the node but the one that says the interval is over brings it back.
func TestNewArrivalAfterMinIntervalWakesOnce(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	_, addr := idleSession(t, h, "a")
	sendToAddr(t, h, "a", addr, "first, never read")
	quiet(t, h)
	endTurn(t, h, addr)
	endTurn(t, h, addr)
	wantPrompts(t, h, addr, 2) // the allowance of repeats is spent

	h.Advance(time.Minute)
	quiet(t, h)
	sendToAddr(t, h, "a", addr, "second, a minute after the last prompt")
	quiet(t, h)
	wantPrompts(t, h, addr, 2)

	h.Advance(handoff.DefaultMinInterval - time.Minute)
	quiet(t, h)
	wantPrompts(t, h, addr, 3)

	// The turn that prompt started is running: nothing more is sent however long it
	// takes.
	h.Advance(handoff.DefaultMinInterval)
	quiet(t, h)
	wantPrompts(t, h, addr, 3)
}

// Messages that arrive together wake the agent once: the later ones arrive while the
// turn the first one started runs, and the agent that reads them all leaves nothing
// to wake it for.
func TestFiveArrivalsStartOneTurn(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	agent, addr := idleSession(t, h, "a")
	for i := 0; i < 5; i++ {
		sendToAddr(t, h, "a", addr, fmt.Sprintf("message %d", i))
	}
	quiet(t, h)

	wantPrompts(t, h, addr, 1)
	if n := turnsStarted(t, h, addr); n != 2 {
		t.Errorf("the session started %d turns, want 2: the first one, and one for the five messages", n)
	}
	page := recvAs(t, h, agent, fakeagent.RecvInput{})
	if len(page.Page.Messages) != 5 {
		t.Fatalf("the page holds %v, want the five messages", pageIDs(page))
	}
	ackAs(t, h, agent, page.Page.Lease)
	endTurn(t, h, addr)
	wantPrompts(t, h, addr, 1)
}

// A prompt that reached the session before the node went down is not sent again after
// it: the node asks the adapter whether it arrived, is told it did, and records it. The
// turn it started goes on meanwhile, and nothing but the answer of the adapter says so.
func TestDaemonKilledNativeTurnContinuesAndReconcilesWithoutResend(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	_, addr := idleSession(t, h, "a")
	first := h.AdapterLaunches(harness.Instance)[0]
	if err := first.Controller().PauseAt(h.Ctx(), fakeadapter.PausePromptAfterForward); err != nil {
		t.Fatal(err)
	}
	msg := sendToAddr(t, h, "a", addr, "sent while the node is killed")
	waitCtx, cancel := context.WithTimeout(h.Ctx(), 10*time.Second)
	defer cancel()
	if err := first.Controller().WaitPaused(waitCtx, fakeadapter.PausePromptAfterForward); err != nil {
		t.Fatalf("no prompt reached the adapter: %v", err)
	}
	// The prompt reached the runtime, the adapter has not answered, and the node dies.
	wantPrompts(t, h, addr, 1)
	if n := turnsStarted(t, h, addr); n != 2 {
		t.Fatalf("the session started %d turns, want 2: the prompt reached the runtime", n)
	}
	if a := attemptsOf(t, h, addr); a.InFlight == nil {
		t.Fatal("the node did not record the prompt as in flight before it called the adapter")
	}
	h.Crash(harness.ProcessCrash)
	quiet(t, h)

	launches := h.AdapterLaunches(harness.Instance)
	if len(launches) != 2 {
		t.Fatalf("%d adapter processes, want 2", len(launches))
	}
	var reconciles, delivers int
	for _, m := range launches[1].Methods() {
		switch m {
		case adapterproto.MethodPromptReconcile:
			reconciles++
		case adapterproto.MethodPromptDeliver:
			delivers++
		}
	}
	if reconciles != 1 || delivers != 0 {
		t.Errorf("the new adapter was asked %d reconciliations and %d prompts, want 1 and 0", reconciles, delivers)
	}
	wantPrompts(t, h, addr, 1)
	if n := turnsStarted(t, h, addr); n != 2 {
		t.Errorf("the session started %d turns, want 2: the prompt was not delivered again", n)
	}
	a := attemptsOf(t, h, addr)
	if a.InFlight != nil || len(a.Attempts) != 1 || a.Attempts[0].Outcome != handoff.AttemptDelivered {
		t.Errorf("the attempts are %+v, want one delivered and none in flight", a)
	}
	if in := inOf(t, h, msg); in.Record.Sub != delivery.SubNotified {
		t.Errorf("the record is %s (%v), want accepted and notified", in.Record.State, in.Record.Sub)
	}
	v, err := h.Engine().Adapter(h.Ctx(), harness.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(v.Busy, adapterproto.EndpointRef(endpointOfAddr(t, addr).String())) {
		t.Errorf("the busy endpoints are %v, want the session: its turn went on", v.Busy)
	}
	// Time alone sends nothing into a session whose turn is running.
	h.Advance(handoff.DefaultMinInterval)
	quiet(t, h)
	wantPrompts(t, h, addr, 1)
}

// A node runs only so many turns that prompts started, whatever the number of
// endpoints that wait: the others wait, the one that has waited longest goes first when
// a place falls free, and waiting is no failure.
func TestPromptTurnsBeyondNodeCapWaitOldestFirst(t *testing.T) {
	// takePlace is what an agent does to leave a place: it reads what it was woken for
	// and its turn ends.
	takePlace := func(t *testing.T, h *harness.H, agent fakeagent.Agent, addr string) {
		t.Helper()
		page := recvAs(t, h, agent, fakeagent.RecvInput{})
		ackAs(t, h, agent, page.Page.Lease)
		endTurn(t, h, addr)
	}
	setCap := func(t *testing.T, h *harness.H, n int, op byte) {
		t.Helper()
		patch := fmt.Sprintf(`{"node_prompt_turns":%d}`, n)
		if _, err := h.Engine().Execute(h.Ctx(), setNodeSettingsCommand(op, patch, h.Config().Node().Version, patch)); err != nil {
			t.Fatalf("node set %s: %v", patch, err)
		}
		quiet(t, h)
	}

	t.Run("the oldest waiter goes first", func(t *testing.T) {
		h := sessionNode(t, harness.Options{NodeSettings: `{"node_prompt_turns":2}`}, "a")
		var agents [4]fakeagent.Agent
		var addrs [4]string
		for i := range addrs {
			agents[i], addrs[i] = idleSession(t, h, "a")
		}
		// Two messages take the two places; the next two wait, the one for the session
		// made last arriving first.
		var msgs [4]adapterproto.MessageID
		for _, i := range []int{0, 1, 3, 2} {
			msgs[i] = sendToAddr(t, h, "a", addrs[i], fmt.Sprintf("for session %d", i))
			quiet(t, h)
			h.Advance(time.Second)
		}
		wantPrompts(t, h, addrs[0], 1)
		wantPrompts(t, h, addrs[1], 1)
		wantPrompts(t, h, addrs[3], 0)
		wantPrompts(t, h, addrs[2], 0)

		takePlace(t, h, agents[0], addrs[0])
		wantPrompts(t, h, addrs[3], 1)
		wantPrompts(t, h, addrs[2], 0)
		takePlace(t, h, agents[1], addrs[1])
		wantPrompts(t, h, addrs[2], 1)

		ids := make([]string, 4)
		for i, a := range addrs {
			ids[i] = endpointOfAddr(t, a).String()
		}
		if got, want := promptOrder(t, h), []string{ids[0], ids[1], ids[3], ids[2]}; !slices.Equal(got, want) {
			t.Errorf("the prompts went to %v, want %v: the message that waited longest first", got, want)
		}
		for _, m := range msgs {
			wantNoFeedback(t, h, "a", m)
		}
	})

	t.Run("a raised cap lets the waiting go, oldest first", func(t *testing.T) {
		h := sessionNode(t, harness.Options{NodeSettings: `{"node_prompt_turns":1}`}, "a")
		var addrs [3]string
		for i := range addrs {
			_, addrs[i] = idleSession(t, h, "a")
		}
		for i := range addrs {
			sendToAddr(t, h, "a", addrs[i], fmt.Sprintf("for session %d", i))
			quiet(t, h)
			h.Advance(time.Second)
		}
		wantPrompts(t, h, addrs[0], 1)
		wantPrompts(t, h, addrs[1], 0)
		wantPrompts(t, h, addrs[2], 0)

		setCap(t, h, 2, 101)
		wantPrompts(t, h, addrs[1], 1)
		wantPrompts(t, h, addrs[2], 0)
		setCap(t, h, 3, 102)
		wantPrompts(t, h, addrs[2], 1)
	})

	t.Run("a turn that went on while the adapter was away holds its place until it ends", func(t *testing.T) {
		h := sessionNode(t, harness.Options{NodeSettings: `{"node_prompt_turns":1}`}, "a")
		firstAgent, first := idleSession(t, h, "a")
		_, second := idleSession(t, h, "a")
		sendToAddr(t, h, "a", first, "for the first")
		quiet(t, h)
		h.Advance(time.Second)
		sendToAddr(t, h, "a", second, "for the second")
		quiet(t, h)
		wantPrompts(t, h, first, 1)
		wantPrompts(t, h, second, 0)

		// The adapter dies and another takes the session back: the turn is still
		// running, the adapter says so, and the place stays taken.
		adapterEnded(t, h, h.AdapterLaunches(harness.Instance)[0])
		adapterRestarted(t, h, 2)
		h.Advance(time.Minute)
		quiet(t, h)
		wantPrompts(t, h, second, 0)

		takePlace(t, h, firstAgent, first)
		wantPrompts(t, h, second, 1)
	})
}

// A prompt does not make the check at recv pass: the endpoint may narrow whom it takes
// messages from after the prompt, and the message is then refused when the agent comes
// for it, with the sender told it was rejected.
func TestDeclarationTightenedAfterPromptRejectsAtRecv(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a", "b")
	agent, addr := idleSession(t, h, "a")
	msg := sendToAddr(t, h, "a", addr, "sent before the declaration changed")
	quiet(t, h)
	wantPrompts(t, h, addr, 1)
	if in := inOf(t, h, msg); in.Record.Sub != delivery.SubNotified {
		t.Fatalf("the record is %s (%v), want notified before the declaration changes", in.Record.State, in.Record.Sub)
	}

	accept := []string{h.Addr("b")}
	if err := agent.Declare(h.Ctx(), fakeagent.DeclareInput{RestrictSenders: boolp(true), AcceptFrom: &accept}); err != nil {
		t.Fatal(err)
	}
	wantEmpty(t, recvAs(t, h, agent, fakeagent.RecvInput{}))
	quiet(t, h)
	if in := inOf(t, h, msg); in.Record.State != delivery.StateRejected || in.Record.Reject != delivery.RejectSecondCheckFailed {
		t.Fatalf("the record is %s (%s), want rejected, second_check_failed", in.Record.State, in.Record.Reject)
	}
	wantPrompts(t, h, addr, 1)
}

// How a prompt ended is recorded as it ended, and one whose end is not known is asked
// of the adapter and never sent again: a refusal is a failure, so the message is not
// marked as one a prompt was sent for and the sender is told nothing; an answer lost
// after the prompt reached the session is a delivery once the adapter says it did, and
// an unknown outcome when it cannot say, in which case the attempt is not delivered a
// second time.
func TestPromptOutcomesAreRecordedAndAnUnknownOneIsNotSentAgain(t *testing.T) {
	lostAnswer := fakeadapter.Outcome{Kind: fakeadapter.OutcomeError, Code: peer.CodeInternalError, AfterEffect: true}
	for _, c := range []struct {
		rule      string
		prompt    fakeadapter.Outcome
		reconcile *fakeadapter.Outcome
		outcome   handoff.AttemptOutcome
		notified  bool
	}{
		{"a refusal is a failure", fakeadapter.Outcome{Kind: fakeadapter.OutcomeRefuse, Reason: string(adapterproto.RefusalBusy)}, nil, handoff.AttemptFailed, false},
		{"a lost answer is a delivery when the adapter finds the prompt", lostAnswer, nil, handoff.AttemptDelivered, true},
		{"a lost answer is unknown when the adapter cannot tell", lostAnswer,
			&fakeadapter.Outcome{Kind: fakeadapter.OutcomeIndeterminate, Reason: string(adapterproto.GroundsNativeUnreachable)}, handoff.AttemptUnknown, false},
		{"a lost answer is unknown when the adapter gives no verdict", lostAnswer,
			&fakeadapter.Outcome{Kind: fakeadapter.OutcomeError, Code: peer.CodeInternalError}, handoff.AttemptUnknown, false},
	} {
		t.Run(c.rule, func(t *testing.T) {
			h := sessionNode(t, harness.Options{}, "a")
			_, addr := idleSession(t, h, "a")
			ctrl, err := h.Adapter(harness.Instance)
			if err != nil {
				t.Fatal(err)
			}
			if err := ctrl.SetOutcome(h.Ctx(), fakeadapter.RequestPrompt, c.prompt); err != nil {
				t.Fatal(err)
			}
			if c.reconcile != nil {
				if err := ctrl.SetOutcome(h.Ctx(), fakeadapter.RequestReconcile, *c.reconcile); err != nil {
					t.Fatal(err)
				}
			}
			msg := sendToAddr(t, h, "a", addr, "for a prompt that goes wrong")
			quiet(t, h)

			a := attemptsOf(t, h, addr)
			if a.InFlight != nil || len(a.Attempts) != 1 || a.Attempts[0].Outcome != c.outcome {
				t.Errorf("the attempts are %+v, want one with the outcome %v and none in flight", a, c.outcome)
			}
			if got := inOf(t, h, msg).Record.Sub == delivery.SubNotified; got != c.notified {
				t.Errorf("the message is notified: %v, want %v", got, c.notified)
			}
			wantPrompts(t, h, addr, 1)
			wantNoFeedback(t, h, "a", msg)
		})
	}
}

// The checkpoint an adapter keeps for a prompt attempt is there while the attempt is in
// flight, so that a reconciliation can be given it, and is gone once the attempt has
// settled.
func TestSettledPromptAttemptDropsItsCheckpoint(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	_, addr := idleSession(t, h, "a")
	first := h.AdapterLaunches(harness.Instance)[0]
	if err := first.Controller().PauseAt(h.Ctx(), fakeadapter.PausePromptAfterForward); err != nil {
		t.Fatal(err)
	}
	sendToAddr(t, h, "a", addr, "for a prompt with a checkpoint")
	waitCtx, cancel := context.WithTimeout(h.Ctx(), 10*time.Second)
	defer cancel()
	if err := first.Controller().WaitPaused(waitCtx, fakeadapter.PausePromptAfterForward); err != nil {
		t.Fatalf("no prompt reached the adapter: %v", err)
	}
	attempt := attemptsOf(t, h, addr).InFlight.Attempt
	put := adapterproto.CheckpointPutParams{Scope: adapterproto.ScopeAttempt, ID: string(attempt), State: []byte(`{"cursor":7}`)}
	if err := h.Engine().CheckpointPut(h.Ctx(), harness.Instance, put); err != nil {
		t.Fatal(err)
	}
	if !hasCheckpoint(t, h, attempt) {
		t.Fatal("the checkpoint of the attempt in flight is not on disk")
	}
	if err := first.Controller().Release(h.Ctx(), fakeadapter.PausePromptAfterForward); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	if hasCheckpoint(t, h, attempt) {
		t.Error("the checkpoint of an attempt that settled is still on disk")
	}
}

func hasCheckpoint(t testing.TB, h *harness.H, attempt adapterproto.AttemptID) bool {
	t.Helper()
	for _, c := range scanOf(t, h).Checkpoints {
		if c.Key.Scope == string(adapterproto.ScopeAttempt) && c.Key.ID == string(attempt) {
			return true
		}
	}
	return false
}

// A session whose adapter is away is not prompted: a prompt for it is bound to fail, and
// failures are what makes delivery to it blocked. Its message waits, with no attempt
// recorded, and the adapter coming back is the event that wakes the session.
func TestMessageForAnOfflineAdapterWaitsAndPromptsWhenItIsBack(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	_, addr := idleSession(t, h, "a")
	adapterEnded(t, h, h.AdapterLaunches(harness.Instance)[0])
	msg := sendToAddr(t, h, "a", addr, "sent while the adapter is away")
	engineQuiet(t, h)
	h.Advance(restartAfter / 2) // less than the host waits before it starts the adapter again
	engineQuiet(t, h)
	if a := attemptsOf(t, h, addr); len(a.Attempts) != 0 || a.InFlight != nil {
		t.Errorf("the attempts are %+v while the adapter is away, want none: nothing was tried", a)
	}

	adapterRestarted(t, h, 2)
	wantPrompts(t, h, addr, 1)
	wantNoFeedback(t, h, "a", msg)
}
