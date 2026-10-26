package engine_test

import (
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/fsx"
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
			h.Advance(mergeWindow)
			quiet(t, h)
			if got := toldKinds(t, h, "a"); !slices.Equal(got, []delivery.Kind{tc.kind}) {
				t.Fatalf("a was told %v, want %s once more", got, tc.kind)
			}
			if got := pending(t, h, "a"); got != 0 {
				t.Errorf("%d items are still pending", got)
			}
			res = recvAs(t, h, h.Agent("a"), fakeagent.RecvInput{})
			if len(res.Page.Messages) != 1 || string(res.Page.Messages[0].From) != h.Engine().SystemAddress().String() {
				t.Errorf("a was handed %+v, want the new notice of the system endpoint", res.Page.Messages)
			}
		})
	}
}

// What a notice told is put back on the source records before the notice is settled,
// so a crash between the two finds the notice damaged again and writes the same items
// again. Wherever the node dies while recv settles a notice whose body is damaged, the
// sender is told again exactly once after the restart and the next recv: not never,
// because the notice was settled before the items were owed again, and not twice,
// because the items written before the crash were written again.
func TestADamagedNoticeIsToldAgainOnceWhereverTheNodeDiesSettlingIt(t *testing.T) {
	cov := &coverage{hit: map[fsx.Key]int{}}
	taker := newDiskTaker(cov, "source-update", "quarantine")
	h := nodeWith(t, harness.Options{Observer: taker}, "a", "b")
	declineAll(t, h, "b")
	send(t, h, "a", "b", "to an endpoint that takes nothing")
	quiet(t, h)
	h.Advance(mergeWindow)
	quiet(t, h)
	ns := noticesFor(t, h, "a")
	if len(ns) != 1 {
		t.Fatalf("a holds %d notices, want 1", len(ns))
	}
	h.Stop()
	flipBodyFile(t, h, ns[0].in)
	h.Start()
	quiet(t, h)

	taker.h = h
	wantEmpty(t, recvAs(t, h, h.Agent("a"), fakeagent.RecvInput{}))
	quiet(t, h)
	for _, tx := range []string{"source-update", "quarantine"} {
		reached := false
		for k := range cov.hit {
			reached = reached || k.Tag.Tx == tx
		}
		if !reached {
			t.Fatalf("no step of transaction %s was reached", tx)
		}
	}
	for _, td := range taker.disks {
		t.Run(td.where, func(t *testing.T) {
			// The restarted node is asked for the page at once: what a restart finds owed
			// is told again only after the merge window, and the damaged notice is settled
			// before that.
			r := harness.New(t, harness.Options{Disk: td.disk, Start: td.at})
			r.MustDirect("a")
			r.MustDirect("b")
			quiet(t, r)
			wantEmpty(t, recvAs(t, r, r.Agent("a"), fakeagent.RecvInput{}))
			quiet(t, r)
			r.Advance(mergeWindow)
			quiet(t, r)
			if got := toldKinds(t, r, "a"); !slices.Equal(got, []delivery.Kind{delivery.KindRejected}) {
				t.Errorf("a was told %v, want %s once more", got, delivery.KindRejected)
			}
			if got := pending(t, r, "a"); got != 0 {
				t.Errorf("%d items are still pending", got)
			}
		})
	}
}
