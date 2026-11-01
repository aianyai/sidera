package engine_test

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// delayedPromptVerdict stands at the engine's adapter boundary. The response is
// already being produced when delivery proof arrives, so cancellation cannot
// prevent that response from reaching the engine afterwards.
type delayedPromptVerdict struct {
	*hostDouble
	called  chan struct{}
	release chan struct{}
	result  adapterproto.ReconcileResult
	err     error
}

func (h *delayedPromptVerdict) Reconcile(context.Context, string, adapterproto.ReconcileParams) (adapterproto.ReconcileResult, error) {
	close(h.called)
	<-h.release
	return h.result, h.err
}

func TestPromptDeliveryProofSurvivesALateReconcileResultWhileItsWriteIsRetried(t *testing.T) {
	for _, verdict := range []string{"error", "indeterminate", "provably absent"} {
		t.Run(verdict, func(t *testing.T) {
			noLeaks(t)
			host := &delayedPromptVerdict{hostDouble: newHostDouble(), called: make(chan struct{}), release: make(chan struct{})}
			switch verdict {
			case "error":
				host.err = errors.New("the reconciliation response was lost")
			case "indeterminate":
				host.result = adapterproto.ReconcileResult{Verdict: adapterproto.VerdictIndeterminate, Grounds: adapterproto.Grounds{Code: adapterproto.GroundsNativeUnreachable}}
			case "provably absent":
				host.result = adapterproto.ReconcileResult{Verdict: adapterproto.VerdictProvablyAbsent, Grounds: adapterproto.Grounds{Code: adapterproto.GroundsRecordCompleteWithoutAttempt}}
			}
			r := newRig(t, 10, withAdapters(host))
			id := mustEndpoint(t, 120)
			ref := adapterproto.EndpointRef(id.String())
			r.seedRecord(store.EndpointRecord{ID: id, Kind: sessionKind, Instance: "chat", Status: store.EndpointOpen, Token: bytes.Repeat([]byte{10}, store.TokenLen), Session: &store.SessionInfo{Preset: "default", NativeRef: "native-session"}})
			r.queue(1, 1, id)
			in := queuedIn(t, 1, id)
			r.seedRecord(store.AttemptsRecord{Endpoint: id, InFlight: &store.InFlightPrompt{Attempt: attemptA, Kind: handoff.PromptKindFirst, Covers: []store.TargetKey{in.Key}}})
			e := r.start()
			t.Cleanup(func() {
				select {
				case <-host.release:
				default:
					close(host.release)
				}
			})
			started(t, e, "chat")
			if err := e.AdapterProbed(ctx, "chat", probeResult("", "")); err != nil {
				t.Fatal(err)
			}
			select {
			case <-host.called:
			case <-time.After(5 * time.Second):
				t.Fatal("the adapter was not asked to reconcile the in-flight prompt")
			}
			rule := r.faulty.Fail(fsxtest.Match{Tx: "prompt-end", Role: "in", Step: fsx.StepWrite, Hit: 1}, syscall.EIO)
			n := adapterhost.Notification{Method: adapterproto.MethodPromptPersisted, Params: adapterproto.PromptPersistedParams{Endpoint: ref, Attempt: attemptA}}
			if err := e.AdapterNotified(ctx, "chat", n); err != nil {
				t.Fatal(err)
			}
			if rule.Hits() != 1 {
				t.Fatal("the proof did not reach its failed settlement write")
			}
			close(host.release)
			settle(t, e) // the late verdict is processed before the scheduled retry
			r.clk.Advance(engine.DefaultParams().RetryBackoff)
			settle(t, e)
			sc := r.scan()
			if len(sc.Attempts) != 1 || sc.Attempts[0].InFlight != nil || len(sc.Attempts[0].Attempts) != 1 || sc.Attempts[0].Attempts[0].Outcome != handoff.AttemptDelivered {
				t.Fatalf("the attempts are %+v, want delivery proof retained through the retry", sc.Attempts)
			}
			if len(sc.In) != 1 || sc.In[0].Record.Sub != delivery.SubNotified {
				t.Fatalf("the target records are %+v, want the proven prompt marked notified", sc.In)
			}
		})
	}
}

// acceptedThenFound is an adapter host that accepts every prompt without proof and,
// asked about an attempt, finds it with its turn running: the runtime holds the prompt.
type acceptedThenFound struct {
	*hostDouble
	prompts atomic.Int64
}

func (h *acceptedThenFound) DeliverPrompt(context.Context, string, adapterproto.PromptDeliverParams) (adapterproto.PromptDeliverResult, error) {
	h.prompts.Add(1)
	return adapterproto.PromptDeliverResult{Stage: adapterproto.PromptAccepted}, nil
}

func (h *acceptedThenFound) Reconcile(context.Context, string, adapterproto.ReconcileParams) (adapterproto.ReconcileResult, error) {
	return adapterproto.ReconcileResult{
		Verdict: adapterproto.VerdictFound, Turn: &adapterproto.TurnReport{Ref: "t1", State: adapterproto.TurnStateRunning},
		Grounds: adapterproto.Grounds{Code: adapterproto.GroundsAttemptInRecord},
	}, nil
}

// The turns an adapter reports as it starts are a picture of that moment. A
// reconciliation answered after it that finds the prompt with its turn running is
// newer, and the probe that follows does not read the start result again over it:
// the turn holds the session busy and no second prompt is sent while it runs.
func TestATurnFoundByReconciliationAfterTheStartIsNotUndoneByTheProbe(t *testing.T) {
	host := &acceptedThenFound{hostDouble: newHostDouble()}
	r, session, _ := waitingSession(t, host)
	e := r.start()
	started(t, e, "chat")
	if err := e.AdapterProbed(ctx, "chat", probeResult("", "")); err != nil {
		t.Fatal(err)
	}
	settle(t, e)
	if n := host.prompts.Load(); n != 1 {
		t.Fatalf("%d prompts were sent, want the one that is accepted", n)
	}
	// The adapter goes away and comes back before the prompt is proven, and reports the
	// session idle: the runtime holds the prompt and has not begun its turn.
	gone(t, e, "chat")
	idle := adapterproto.SessionState{Endpoint: adapterproto.EndpointRef(session.String()), State: adapterproto.AttachAttached}
	if err := e.AdapterStarted(ctx, "chat", adapterproto.StartResult{Sessions: []adapterproto.SessionState{idle}}); err != nil {
		t.Fatal(err)
	}
	r.clk.Advance(engine.DefaultParams().PromptConfirmWait) // the reconciliation of the accepted prompt
	settle(t, e)
	if a := headOf(t, r, session); a.InFlight != nil || len(a.Attempts) != 1 || a.Attempts[0].Outcome != handoff.AttemptDelivered {
		t.Fatalf("the attempts are %+v, want the prompt delivered by reconciliation", a)
	}
	if err := e.AdapterProbed(ctx, "chat", probeResult("", "")); err != nil {
		t.Fatal(err)
	}
	settle(t, e)
	if n := host.prompts.Load(); n != 1 {
		t.Errorf("%d prompts were sent, want 1: the turn the reconciliation found runs", n)
	}
}
