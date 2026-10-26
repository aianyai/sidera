package engine_test

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A crash walk of the writes of recv and ack: before every step of the transactions
// that lease a page, close what the second check refused, and acknowledge, the disks
// a crash at that instant can leave are taken, and each is started as a node of its
// own. Wherever the node died, the sender is never told delivered of what the
// recipient did not acknowledge, no accepted record is left without its body, no body
// is left without a record, and the agent that asks again is handed what it was not
// acknowledged to have seen, so that every message ends as it was owed: delivered, or
// refused by the second check.
func TestRecvAndAckRecoverFromACrashAtEveryPoint(t *testing.T) {
	walked := []string{"lease", "ack"}
	cov := &coverage{hit: map[fsx.Key]int{}}

	// checkPulled holds a restarted node to the invariants, drains the queue of b as its
	// agent does and holds every message to its end.
	checkPulled := func(t *testing.T, h *harness.H, ends map[adapterproto.MessageID]delivery.State) {
		t.Helper()
		sc := scanOf(t, h)
		checkNoOrphanBody(t, sc)
		for msg := range ends {
			ins := inRecords(t, h, msg)
			if len(ins) != 1 {
				t.Fatalf("message %s has %d target records, want 1", msg, len(ins))
			}
			if in := ins[0]; in.Record.State == delivery.StateAccepted && in.Body == nil {
				t.Errorf("message %s is accepted and holds no body", msg)
			}
			if src := sourceDelivery(t, h, msg).State; src == delivery.StateDelivered && ins[0].Record.State != delivery.StateDelivered {
				t.Errorf("message %s is delivered at the source and %s at the target: a false acknowledgement", msg, ins[0].Record.State)
			}
		}
		b := h.Agent("b")
		for i := 0; i < 4; i++ {
			res, err := b.Recv(h.Ctx(), fakeagent.RecvInput{Frontend: adapterproto.FrontendCLI})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Page.Messages) == 0 {
				break
			}
			if _, err := b.Ack(h.Ctx(), fakeagent.AckInput{Lease: res.Page.Lease}); err != nil {
				t.Fatal(err)
			}
		}
		quiet(t, h)
		for msg, end := range ends {
			if got := inOf(t, h, msg).Record.State; got != end {
				t.Errorf("message %s ends %s, want %s", msg, got, end)
			}
		}
		checkNoOrphanBody(t, scanOf(t, h))
	}
	walk := func(t *testing.T, taker *diskTaker, ends map[adapterproto.MessageID]delivery.State) {
		t.Helper()
		if len(taker.disks) == 0 {
			t.Fatal("no step of the watched transactions was reached")
		}
		for _, td := range taker.disks {
			t.Run(td.where, func(t *testing.T) {
				checkPulled(t, restarted(t, td.disk, "a", "b", "c"), ends)
			})
		}
	}

	t.Run("a page of two messages that is then acknowledged", func(t *testing.T) {
		taker := newDiskTaker(cov, "lease", "ack")
		h := nodeWith(t, harness.Options{Observer: taker}, "a", "b", "c")
		taker.h = h
		first := send(t, h, "a", "b", "the first")
		second := send(t, h, "c", "b", "the second")
		quiet(t, h)
		res := recvAs(t, h, h.Agent("b"), fakeagent.RecvInput{})
		ackAs(t, h, h.Agent("b"), res.Page.Lease)
		quiet(t, h)
		walk(t, taker, map[adapterproto.MessageID]delivery.State{first: delivery.StateDelivered, second: delivery.StateDelivered})
	})
	t.Run("a page that leases one message and closes another the second check refused", func(t *testing.T) {
		taker := newDiskTaker(cov, "lease")
		h := nodeWith(t, harness.Options{Observer: taker}, "a", "b", "c")
		taker.h = h
		refusedMsg := send(t, h, "a", "b", "from the sender the declaration will refuse")
		quiet(t, h)
		h.Advance(time.Second)
		kept := send(t, h, "c", "b", "from the one it keeps")
		quiet(t, h)
		accept := []string{h.Addr("c")}
		if err := h.Agent("b").Declare(h.Ctx(), fakeagent.DeclareInput{RestrictSenders: boolp(true), AcceptFrom: &accept}); err != nil {
			t.Fatal(err)
		}
		wantOnly(t, recvAs(t, h, h.Agent("b"), fakeagent.RecvInput{}), kept)
		quiet(t, h)
		walk(t, taker, map[adapterproto.MessageID]delivery.State{refusedMsg: delivery.StateRejected, kept: delivery.StateDelivered})
	})

	declared := 0
	for _, k := range harness.ProgramTable(t).Keys() {
		for _, tx := range walked {
			if k.Tag.Tx != tx {
				continue
			}
			declared++
			if cov.hit[k.Key] == 0 {
				t.Errorf("the step %s %q of transaction %s was never reached: the walk proves nothing about a crash at it", k.Step, k.Tag.Role, tx)
			}
		}
	}
	if declared == 0 {
		t.Fatal("no step of the walked transactions is declared")
	}
}
