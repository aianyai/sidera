package engine_test

import (
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

// A notice is the proof that its items were told, and the items are taken off the
// source records once it is written. When the agent's recv finds that the body of
// the notice is not what its record says, the notice is settled like any record with
// a damaged body, and what it covered is owed again: the items go back on the source
// records they came from and a new notice tells them, once.
func TestANoticeWhoseBodyIsDamagedIsToldAgainFromWhatItCovered(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(t *testing.T) *harness.H
		kind delivery.Kind
	}{
		{"rejected", func(t *testing.T) *harness.H {
			h := nodeWith(t, harness.Options{}, "a", "b")
			declineAll(t, h, "b")
			send(t, h, "a", "b", "to an endpoint that takes nothing")
			quiet(t, h)
			return h
		}, delivery.KindRejected},
		{"accepted_unconfirmed", func(t *testing.T) *harness.H {
			h := nodeWith(t, harness.Options{}, "a", "b")
			msg := finalPullStates[0].setup(t, h)
			reportBy := sourceDelivery(t, h, msg).ReportBy
			h.Unreachable(true)
			sleepUntilAfter(t, h, reportBy, time.Minute)
			return h
		}, delivery.KindAcceptedUnconfirmed},
		{"expired_unconfirmed", func(t *testing.T) *harness.H {
			h := nodeWith(t, harness.Options{}, "a", "b")
			msg := finalPullStates[1].setup(t, h)
			reportBy := sourceDelivery(t, h, msg).ReportBy
			h.Unreachable(true)
			sleepUntilAfter(t, h, reportBy, time.Minute)
			return h
		}, delivery.KindExpiredUnconfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.make(t)
			h.Advance(mergeWindow)
			quiet(t, h)
			ns := noticesFor(t, h, "a")
			if len(ns) != 1 || !slices.Equal(ns[0].kinds(), []delivery.Kind{tc.kind}) {
				t.Fatalf("a holds %d notices (%v), want one for %s", len(ns), toldKinds(t, h, "a"), tc.kind)
			}
			h.Stop()
			flipBodyFile(t, h, ns[0].in)
			h.Start()
			quiet(t, h)

			res := recvAs(t, h, h.Agent("a"), fakeagent.RecvInput{})
			wantEmpty(t, res)
			if n := len(alertsOfKind(t, h, "corrupt_body")); n != 1 {
				t.Errorf("the owner has %d alerts of kind corrupt_body, want 1", n)
			}
			if got := pending(t, h, "a"); got != 1 {
				t.Fatalf("%d items are owed to a after its notice was settled, want the one it covered", got)
			}
			toldOnce(t, h, []delivery.Kind{tc.kind})
			res = recvAs(t, h, h.Agent("a"), fakeagent.RecvInput{})
			if len(res.Page.Messages) != 1 || string(res.Page.Messages[0].From) != h.Engine().SystemAddress().String() {
				t.Errorf("a was handed %+v, want the new notice of the system endpoint", res.Page.Messages)
			}
		})
	}
}
