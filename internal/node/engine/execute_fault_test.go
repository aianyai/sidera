package engine_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A result that could not be written is written again, and only written: the adapter
// has done what it was asked, and asking again could stop the next turn of the session
// (an interrupt) or hide that the first one had stopped this one. A result that could not
// be written at the deadline is not left for the next start: the signal would stay
// accepted, its sender told nothing, until the node restarted or the sender gave up.
func TestFailedWriteOfTheResultOfASignalIsTriedAgainWithoutAskingTheAdapter(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  adapterproto.ControlSignal
		// away says the adapter is gone for the whole test, so the deadline settles the
		// signal; its restart is put further off than the time the test moves the clock.
		away bool
		// asked is the ledger kind of the call to the adapter, and times how often it
		// is made.
		asked string
		times int
		want  func(testing.TB, *adapterproto.ControlReply)
	}{
		{"the result of an interrupt", adapterproto.SignalSessionInterrupt, false, ledger.KindInterruptReceived, 1,
			func(t testing.TB, r *adapterproto.ControlReply) {
				wantInterrupt(t, r, adapterproto.InterruptReplyStopped, 0)
			}},
		{"the result of a close", adapterproto.SignalSessionClose, false, ledger.KindCloseReceived, 1,
			func(t testing.TB, r *adapterproto.ControlReply) { wantClose(t, r, adapterproto.CloseReplyClosed, 0) }},
		{"the result at the deadline", adapterproto.SignalSessionClose, true, ledger.KindCloseReceived, 0,
			func(t testing.TB, r *adapterproto.ControlReply) {
				wantClose(t, r, adapterproto.CloseReplyUnconfirmed, adapterproto.CloseReasonAdapterOffline)
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := sessionHostParams()
			host.RestartBackoffMin, host.RestartBackoffMax = 10*engine.DefaultParams().ControlReplyDeadline, 10*engine.DefaultParams().ControlReplyDeadline
			h := sessionNodeOf(t, harness.Options{}, host, "a")
			_, id, _ := startedSession(t, h, "a", "")
			settle := quiet
			if tc.away {
				adapterEnded(t, h, h.AdapterLaunches(harness.Instance)[0])
				settle = engineQuiet
			}
			// The first write of a control result is the creation's, the second the signal's.
			rule := h.Faults().Fail(fsxtest.Match{Tx: "control-result", Role: "ctl", Step: fsx.StepWrite, Hit: 2}, fsxtest.DiskFull)

			msg := signal(t, h, "a", addressOf(id), tc.sig, "")
			settle(t, h)
			if tc.away {
				h.Advance(engine.DefaultParams().ControlReplyDeadline)
				settle(t, h)
			}
			if rule.Hits() == 0 {
				t.Fatal("the write of the result did not fail")
			}
			h.Advance(engine.DefaultParams().RetryBackoff)
			settle(t, h)
			h.Advance(pullInterval)
			settle(t, h)

			tc.want(t, controlReply(t, h, "a", msg))
			if n := ledgerCount(t, h, tc.asked, id); n != tc.times {
				t.Errorf("the adapter was asked %d times, want %d", n, tc.times)
			}
			if tc.sig == adapterproto.SignalSessionClose {
				for _, ep := range sessionEndpoints(t, h) {
					if ep.ID == id && ep.Status != store.EndpointClosed {
						t.Errorf("the session endpoint is %v, want closed", ep.Status)
					}
				}
			}
		})
	}
}
