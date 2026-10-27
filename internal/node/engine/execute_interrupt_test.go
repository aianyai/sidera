package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A turn ending before the adapter answers a close or interrupt must not wake the
// session while the signal is still being carried out. An interrupt leaves the
// channel open, so its settlement must judge the messages that waited again.
func TestSessionIsNotPromptedWhileCloseOrInterruptIsBeingCarriedOut(t *testing.T) {
	for _, tc := range []struct {
		name   string
		signal adapterproto.ControlSignal
		pause  string
	}{
		{"close", adapterproto.SignalSessionClose, fakeadapter.PauseCloseAfterForward},
		{"interrupt", adapterproto.SignalSessionInterrupt, fakeadapter.PauseInterruptAfterForward},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := sessionNode(t, harness.Options{}, "a")
			_, id, _ := startedSession(t, h, "a", "")
			addr := addressOf(id)
			sendToAddr(t, h, "a", addr, "waiting for the running turn")
			quiet(t, h)
			ctrl := h.AdapterLaunches(harness.Instance)[0].Controller()
			if err := ctrl.PauseAt(h.Ctx(), tc.pause); err != nil {
				t.Fatal(err)
			}
			msg := signal(t, h, "a", addr, tc.signal, "")
			ctx, cancel := context.WithTimeout(h.Ctx(), 10*time.Second)
			defer cancel()
			if err := ctrl.WaitPaused(ctx, tc.pause); err != nil {
				t.Fatal(err)
			}
			quiet(t, h)
			log, err := h.Ledger()
			if err != nil {
				t.Fatal(err)
			}
			if n := len(log.Kind(ledger.KindRuntimeTurnEnded)); n != 1 {
				t.Fatalf("%d turns ended before the held answer, want one", n)
			}
			wantPrompts(t, h, addr, 0)
			// An arrival during the held call also goes through the prompt decision.
			sendToAddr(t, h, "a", addr, "arriving while the signal waits")
			quiet(t, h)
			wantPrompts(t, h, addr, 0)
			if err := ctrl.Release(h.Ctx(), tc.pause); err != nil {
				t.Fatal(err)
			}
			if tc.signal == adapterproto.SignalSessionClose {
				wantClose(t, replyOf(t, h, "a", msg), adapterproto.CloseReplyClosed, 0)
				wantPrompts(t, h, addr, 0)
			} else {
				wantInterrupt(t, replyOf(t, h, "a", msg), adapterproto.InterruptReplyStopped, 0)
				wantPrompts(t, h, addr, 1)
			}
		})
	}
}

// An adapter that asked the runtime to stop a turn does not yet know the turn stopped,
// and the sender is told what is known: that the turn ended, when it does within
// Params.InterruptConfirmWait, and that the interrupt was requested, when it does not.
func TestInterruptOfARunningTurnIsToldByWhetherTheTurnEnds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		endTurn bool
		want    adapterproto.ControlInterruptOutcome
	}{
		{"the turn ends within the wait", true, adapterproto.InterruptReplyStopped},
		{"the turn does not end within the wait", false, adapterproto.InterruptReplyRequested},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := sessionNode(t, harness.Options{}, "a")
			_, id, _ := startedSession(t, h, "a", "")
			ctrl, err := h.Adapter(harness.Instance)
			if err != nil {
				t.Fatal(err)
			}
			if err := ctrl.SetOutcome(h.Ctx(), fakeadapter.RequestInterrupt, fakeadapter.Outcome{Kind: fakeadapter.OutcomeRequested}); err != nil {
				t.Fatal(err)
			}
			rt, err := h.Runtime()
			if err != nil {
				t.Fatal(err)
			}
			var native string
			for _, ep := range sessionEndpoints(t, h) {
				if ep.ID == id {
					native = ep.Session.NativeRef
				}
			}
			// The first turn of a session is running from its creation.
			msg := signal(t, h, "a", addressOf(id), adapterproto.SignalSessionInterrupt, "")
			quiet(t, h)
			if r := controlReply(t, h, "a", msg); r != nil {
				t.Fatalf("a reply %+v to an interrupt while the turn runs", r)
			}
			if tc.endTurn {
				if err := rt.EndTurn(native, fakeruntime.FinalInterrupted); err != nil {
					t.Fatal(err)
				}
			} else {
				h.Advance(engine.DefaultParams().InterruptConfirmWait)
			}
			wantInterrupt(t, replyOf(t, h, "a", msg), tc.want, 0)
			if n := ledgerCount(t, h, ledger.KindInterruptReceived, id); n != 1 {
				t.Errorf("the adapter was asked to interrupt %d times, want once", n)
			}
		})
	}
}
