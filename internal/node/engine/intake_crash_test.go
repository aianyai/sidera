package engine_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A crash walk of the writes of sending and intake: before every step of the
// transactions that take a message in, the disks a crash at that instant can leave
// are taken, and each is started as a node of its own. Wherever the node died,
// the next start finds no record that has no source, goes on with what the source
// holds, and in the end the message is handled once: one target record, the
// sender's record settled, and no body that no record refers to. A message that
// came from no source of this node (an envelope from another endpoint) has no source
// to go on with, so the walk holds it to the other half: at most one record, whole.
// The walk is held to every step the transactions declare: a step no scenario
// reaches proves nothing about a crash at it.

// diskTaker is the observer that takes the disks.
type diskTaker struct {
	h   *harness.H
	txs map[string]bool
	cov *coverage

	mu    sync.Mutex
	disks []takenDisk
}

type takenDisk struct {
	where string
	disk  *fsxtest.Mem
}

// coverage is every step of the transactions under walk that some scenario reached.
type coverage struct {
	mu  sync.Mutex
	hit map[fsx.Key]int
}

func (c *coverage) note(k fsx.Key) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hit[k]++
}

func newDiskTaker(cov *coverage, txs ...string) *diskTaker {
	set := map[string]bool{}
	for _, tx := range txs {
		set[tx] = true
	}
	return &diskTaker{txs: set, cov: cov}
}

func (d *diskTaker) Reached(p fsx.Point, _ txn.Kind) {
	if d.h == nil || !d.txs[p.Tag.Tx] {
		return
	}
	mem := d.h.Disk()
	where := "before " + p.String()
	d.cov.note(p.Key)
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, cd := range crashDisks(mem) {
		d.disks = append(d.disks, takenDisk{fmt.Sprintf("%s (%s)", where, cd.kind), cd.disk})
	}
}

// restarted starts a node on a disk a crash left, registers the endpoints of the
// scenario under the same keys, and lets it go on until nothing is left to do.
func restarted(t *testing.T, disk *fsxtest.Mem, names ...string) *harness.H {
	t.Helper()
	h := harness.New(t, harness.Options{Disk: disk})
	for _, n := range names {
		h.MustDirect(n)
	}
	quiet(t, h)
	for i := 0; i < 3; i++ {
		h.Advance(resendBackoff + pullInterval)
		quiet(t, h)
	}
	return h
}

// outcome is how a message that has a source ends: the state of the target's
// record and of the sender's, and whether the target let the answer in on the
// permission the evidence earned.
type outcome struct {
	state  delivery.State
	reject delivery.RejectReason
	// permitted is set for an answer: the target took it in on the reply permission.
	permitted bool
}

var (
	taken   = outcome{state: delivery.StateAccepted}
	refused = outcome{state: delivery.StateRejected, reject: delivery.RejectEndpointDeclined}
	answer  = outcome{state: delivery.StateAccepted, permitted: true}
)

// checkSettled holds a restarted node to the invariants of a message sent from a
// to b: no target record without the source's, and where the source has the
// message, one target record and both sides settled as the outcome says.
func checkSettled(t *testing.T, h *harness.H, msg adapterproto.MessageID, want outcome) {
	t.Helper()
	sc := scanOf(t, h)
	var outs []store.OutRecord
	for _, o := range sc.Out {
		if o.Key.Message == delivery.MessageID(msg) {
			outs = append(outs, o)
		}
	}
	ins := inRecords(t, h, msg)
	switch {
	case len(outs) == 0 && len(ins) != 0:
		t.Errorf("%d target records of a message the source never accepted", len(ins))
	case len(outs) == 1 && len(ins) != 1:
		t.Errorf("%d target records of a message the source holds, want 1", len(ins))
	case len(outs) == 1:
		if got := ins[0].Record; got.State != want.state || got.Reject != want.reject {
			t.Errorf("the target record is %s (%s), want %s (%s)", got.State, got.Reject, want.state, want.reject)
		}
		if got := ins[0].ReplyDecision.Granted(); got != want.permitted {
			t.Errorf("the reply permission of the target record is %v (%v), want %v", got, ins[0].ReplyDecision.Reason, want.permitted)
		}
		if got := outs[0].Outbound.Recipients[0].Delivery; got.State != want.state || got.Reject != want.reject {
			t.Errorf("the source record is %s (%s), want %s (%s)", got.State, got.Reject, want.state, want.reject)
		}
		if outs[0].Body != nil {
			t.Errorf("the source record still refers to a body it has no use for")
		}
	case len(outs) > 1:
		t.Errorf("%d source records of one message", len(outs))
	}
	checkNoOrphanBody(t, sc)
}

// checkTakenOnce holds a restarted node to the invariants of a message that came
// from no source of this node: nothing goes on with it, so the record is there or
// not, once, and whole.
func checkTakenOnce(t *testing.T, h *harness.H, msg adapterproto.MessageID) {
	t.Helper()
	sc := scanOf(t, h)
	ins := inRecords(t, h, msg)
	if len(ins) > 1 {
		t.Errorf("%d target records of one envelope", len(ins))
	}
	for _, in := range ins {
		if in.Record.State != delivery.StateAccepted || in.Body == nil {
			t.Errorf("the target record is %s with body %v, want accepted and holding its body", in.Record.State, in.Body)
		}
	}
	checkNoOrphanBody(t, sc)
}

func checkNoOrphanBody(t *testing.T, sc *store.Scan) {
	t.Helper()
	referenced := map[string]bool{}
	for _, o := range sc.Out {
		if o.Body != nil {
			referenced[o.Body.Name()] = true
		}
	}
	for _, in := range sc.In {
		if in.Body != nil {
			referenced[in.Body.Name()] = true
		}
	}
	for _, b := range sc.Bodies {
		if !referenced[b] {
			t.Errorf("body %s is referred to by no record after the start", b)
		}
	}
}

// walkDisks starts a node on every disk taken and checks it with the invariant.
func walkDisks(t *testing.T, taker *diskTaker, check func(t *testing.T, h *harness.H)) {
	t.Helper()
	if len(taker.disks) == 0 {
		t.Fatal("no step of the watched transactions was reached")
	}
	for _, td := range taker.disks {
		t.Run(td.where, func(t *testing.T) {
			check(t, restarted(t, td.disk, "a", "b"))
		})
	}
}

// questionHeld sends a question from a to b that b takes in and a is not told so,
// the way a lost report leaves it: a takes nobody's messages, so only an answer
// that proves the question arrived can reach it.
func questionHeld(t *testing.T, h *harness.H) adapterproto.MessageID {
	t.Helper()
	if err := h.Agent("a").Declare(h.Ctx(), fakeagent.DeclareInput{RestrictSenders: boolp(true), AcceptFrom: &[]string{}}); err != nil {
		t.Fatal(err)
	}
	h.Unreachable(true)
	msg := send(t, h, "a", "b", "a question")
	quiet(t, h)
	h.HoldReports(string(msg))
	h.Unreachable(false)
	h.Advance(resendBackoff)
	quiet(t, h)
	return msg
}

func TestSendingAndIntakeRecoverFromACrashAtEveryPoint(t *testing.T) {
	walked := []string{"intake", "deliver", "source-update", "deliver-reply"}
	cov := &coverage{hit: map[fsx.Key]int{}}

	t.Run("a message from a to b", func(t *testing.T) {
		taker := newDiskTaker(cov, "intake", "deliver", "source-update")
		h := nodeWith(t, harness.Options{Observer: taker}, "a", "b")
		taker.h = h
		msg := send(t, h, "a", "b", "does it survive")
		quiet(t, h)
		walkDisks(t, taker, func(t *testing.T, r *harness.H) { checkSettled(t, r, msg, taken) })
	})
	t.Run("a message the target refuses", func(t *testing.T) {
		// The body is the source's alone, and goes when the refusal is known.
		taker := newDiskTaker(cov, "intake", "deliver", "source-update")
		h := nodeWith(t, harness.Options{Observer: taker}, "a", "b")
		taker.h = h
		if err := h.Agent("b").Declare(h.Ctx(), fakeagent.DeclareInput{Paused: boolp(true)}); err != nil {
			t.Fatal(err)
		}
		msg := send(t, h, "a", "b", "to an endpoint that takes nothing")
		quiet(t, h)
		walkDisks(t, taker, func(t *testing.T, r *harness.H) { checkSettled(t, r, msg, refused) })
	})
	t.Run("an envelope with no source record on this node", func(t *testing.T) {
		// Nothing of this node sent it, so the target alone writes the body.
		taker := newDiskTaker(cov, "deliver")
		h := nodeWith(t, harness.Options{Observer: taker}, "a", "b")
		taker.h = h
		if _, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), addrOf(t, h, "b").Endpoint, forged(t, h, "from-nowhere", addrOf(t, h, "a"), "", "a body only the target holds")); err != nil {
			t.Fatal(err)
		}
		walkDisks(t, taker, func(t *testing.T, r *harness.H) { checkTakenOnce(t, r, "from-nowhere") })
	})
	t.Run("an answer that carries evidence", func(t *testing.T) {
		taker := newDiskTaker(cov, "intake", "deliver", "source-update", "deliver-reply")
		h := nodeWith(t, harness.Options{Observer: taker}, "a", "b")
		taker.h = h
		msg := questionHeld(t, h)
		res, err := h.Agent("b").Reply(h.Ctx(), fakeagent.ReplyInput{MessageID: msg, Body: "an answer"})
		if err != nil {
			t.Fatal(err)
		}
		quiet(t, h)
		walkDisks(t, taker, func(t *testing.T, r *harness.H) {
			checkSettled(t, r, res.MessageID, answer)
		})
	})
	t.Run("an answer whose evidence releases the last reference to a body", func(t *testing.T) {
		// The question's body is the source's alone, and the transaction that writes
		// the answer removes it.
		taker := newDiskTaker(cov, "deliver-reply")
		h := nodeWith(t, harness.Options{Observer: taker}, "a", "b")
		taker.h = h
		msg, report := bodyHeldBySourceAlone(t, h, "a question only the source holds")
		if _, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), addrOf(t, h, "b").Endpoint,
			forged(t, h, "answer-that-frees-a-body", addrOf(t, h, "a"), msg, "an answer", report)); err != nil {
			t.Fatal(err)
		}
		walkDisks(t, taker, func(t *testing.T, r *harness.H) { checkTakenOnce(t, r, "answer-that-frees-a-body") })
	})
	t.Run("an answer with no source record on this node", func(t *testing.T) {
		taker := newDiskTaker(cov, "deliver-reply")
		h := nodeWith(t, harness.Options{Observer: taker}, "a", "b")
		taker.h = h
		msg := questionHeld(t, h)
		// The evidence is the node's own report of b's receipt of the question, which
		// the held reports kept from a.
		h.Release(string(msg))
		ans, err := h.Loopback().Pull(h.Ctx(), harness.NodeID(), transport.PullRequest{Message: delivery.MessageID(msg)})
		if err != nil || len(ans.Reports) != 1 {
			t.Fatalf("the report of the receipt: %+v, %v", ans, err)
		}
		if _, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), addrOf(t, h, "b").Endpoint,
			forged(t, h, "answer-from-nowhere", addrOf(t, h, "a"), msg, "an answer only the target holds", ans.Reports...)); err != nil {
			t.Fatal(err)
		}
		walkDisks(t, taker, func(t *testing.T, r *harness.H) { checkTakenOnce(t, r, "answer-from-nowhere") })
	})

	// Every step of the transactions that were walked was crashed at in some scenario.
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
