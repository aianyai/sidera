package engine_test

import (
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// leaseRanOut is a node whose agent b was handed the message first under a lease that
// then ran out, with a second message queued behind it that nobody was handed. arm
// is called before the lease runs out, so that a rule can fail the writes of the tick
// the schedule makes when it does.
func leaseRanOut(t *testing.T, arm func(*fsxtest.Faulty)) (h *harness.H, first, second adapterproto.MessageID) {
	t.Helper()
	h = nodeWith(t, harness.Options{}, "a", "b")
	first = send(t, h, "a", "b", "handed out and never acknowledged")
	quiet(t, h)
	wantOnly(t, recvAs(t, h, h.Agent("b"), fakeagent.RecvInput{}), first)
	second = send(t, h, "a", "b", "queued behind it")
	quiet(t, h)
	arm(h.Faults())
	h.Advance(delivery.LeaseDuration)
	quiet(t, h)
	return h, first, second
}

// A message whose lease ran out is due: time settles it before it is handed out, and
// recv asks for that first. When the tick cannot be written the message stays due, and
// recv leaves it out of the page and hands out the rest, since the delivery rules
// refuse to hand out a message the time has not judged.
func TestRecvTicksTheRecordsThatAreDueAndLeavesOutThoseItCouldNotTick(t *testing.T) {
	tickWrite := fsxtest.Match{Tx: "tick", Role: "in", Step: fsx.StepWrite}
	t.Run("the tick is made first, and the message is handed out again", func(t *testing.T) {
		// The tick the schedule made failed once and waits to be tried again.
		first := tickWrite
		first.Hit = 1
		h, msg, other := leaseRanOut(t, func(f *fsxtest.Faulty) { f.Fail(first, syscall.EIO) })
		res := recvAs(t, h, h.Agent("b"), fakeagent.RecvInput{})
		if got := pageIDs(res); len(got) != 2 || got[0] != msg || got[1] != other {
			t.Fatalf("the page holds %v, want %s and %s", got, msg, other)
		}
		if !res.Page.Messages[0].PossiblyDuplicate {
			t.Error("the message whose lease ran out is not marked as a possible duplicate")
		}
	})
	t.Run("a message that stays due is left out and the rest is handed out", func(t *testing.T) {
		h, msg, other := leaseRanOut(t, func(f *fsxtest.Faulty) { f.Fail(tickWrite, syscall.EIO) })
		wantOnly(t, recvAs(t, h, h.Agent("b"), fakeagent.RecvInput{}), other)
		if in := inOf(t, h, msg); in.Record.State != delivery.StateAccepted || in.Record.Lease.Handouts != 1 {
			t.Errorf("the message left out is %s with %d hand-outs, want it as it was", in.Record.State, in.Record.Lease.Handouts)
		}
		// A part of it, asked for by number, is not given now either, and the message is not unknown.
		wantEmpty(t, recvAs(t, h, h.Agent("b"), fakeagent.RecvInput{Msg: msg, Part: 1}))
	})
}

// A fatal storage error stops the loop for good, and nothing is handed out or written
// after it: not the page, not the lease of its messages. The error that says a body is
// damaged does not stand for a record that was settled when the settlement itself failed
// for good.
func TestRecvHandsNothingOutAfterAFatalStorageError(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) (h *harness.H, handedOut adapterproto.MessageID)
	}{
		{"while it ticks the records that are due", func(t *testing.T) (*harness.H, adapterproto.MessageID) {
			// The tick of the schedule fails at its write and is tried again; the one recv makes
			// reaches the commit and fails when the directory is synced.
			h, _, other := leaseRanOut(t, func(f *fsxtest.Faulty) {
				f.Fail(fsxtest.Match{Tx: "tick", Role: "in", Step: fsx.StepWrite, Hit: 1}, syscall.EIO)
				f.Fail(fsxtest.Match{Tx: "tick", Role: "in-dir", Step: fsx.StepSyncDir, Hit: 1}, syscall.EIO)
			})
			return h, other
		}},
		{"while it settles a body that is gone", func(t *testing.T) (*harness.H, adapterproto.MessageID) {
			h, bad := settledMessage(t)
			good := send(t, h, "a", "b", "its body is intact")
			quiet(t, h)
			in := inOf(t, h, bad)
			h.Stop()
			removeBody(t, h, in)
			h.Start()
			quiet(t, h)
			h.Faults().Fail(fsxtest.Match{Tx: "quarantine", Step: fsx.StepSyncDir}, syscall.EIO)
			return h, good
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, other := tc.setup(t)
			res, err := h.Agent("b").Recv(h.Ctx(), fakeagent.RecvInput{Frontend: adapterproto.FrontendCLI})
			if fakeagent.CodeOf(err) != "storage_fatal" {
				t.Fatalf("recv got %+v, %v, want storage_fatal and no page", res.Page, err)
			}
			if in := inOf(t, h, other); in.Record.Lease.Handouts != 0 || len(in.Record.Lease.IDs) != 0 {
				t.Errorf("the message that was queued has a lease written (%d hand-outs, %v), want none", in.Record.Lease.Handouts, in.Record.Lease.IDs)
			}
		})
	}
}
