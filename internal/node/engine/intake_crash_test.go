package engine_test

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A crash walk of the writes of sending and intake: before every step of the
// transactions that take a message in, the disks a crash at that instant can leave
// are taken, and each is started as a node of its own. Wherever the node died,
// the next start finds no record that has no source, goes on with what the source
// holds, and in the end the message is delivered once: one target record, the
// sender's record settled, and no body that no record refers to.

// diskTaker is the observer that takes the disks.
type diskTaker struct {
	h   *harness.H
	txs map[string]bool
	rng *rand.Rand

	mu    sync.Mutex
	disks []takenDisk
	steps map[fsx.Key]bool
}

type takenDisk struct {
	where string
	disk  *fsxtest.Mem
}

func newDiskTaker(seed uint64, txs ...string) *diskTaker {
	set := map[string]bool{}
	for _, tx := range txs {
		set[tx] = true
	}
	return &diskTaker{txs: set, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), steps: map[fsx.Key]bool{}}
}

func (d *diskTaker) Reached(p fsx.Point, _ txn.Kind) {
	if d.h == nil || !d.txs[p.Tag.Tx] {
		return
	}
	mem := d.h.Disk()
	where := "before " + p.String()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.steps[p.Key] = true
	d.disks = append(d.disks,
		takenDisk{where + " (process crash)", mem.Clone()},
		takenDisk{where + " (worst power loss)", mem.CrashCloneWorst()})
	for i := 0; i < 2; i++ {
		d.disks = append(d.disks, takenDisk{fmt.Sprintf("%s (power loss %d)", where, i), mem.CrashClone(d.rng)})
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

// checkSettled holds a restarted node to the invariants of a message sent from a
// to b: no target record without the source's, and where the source has the
// message, one target record and both sides settled as accepted.
func checkSettled(t *testing.T, h *harness.H, msg adapterproto.MessageID) {
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
		if got := ins[0].Record.State; got != delivery.StateAccepted {
			t.Errorf("the target record is %s", got)
		}
		if got := outs[0].Outbound.Recipients[0].Delivery.State; got != delivery.StateAccepted {
			t.Errorf("the source record is %s, want accepted", got)
		}
		if outs[0].Body != nil {
			t.Errorf("the source record still refers to a body it has no use for")
		}
	case len(outs) > 1:
		t.Errorf("%d source records of one message", len(outs))
	}
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

func TestSendingAndIntakeRecoverFromACrashAtEveryPoint(t *testing.T) {
	t.Run("a message from a to b", func(t *testing.T) {
		taker := newDiskTaker(21, "intake", "deliver", "source-update")
		h := nodeWith(t, harness.Options{Observer: taker}, "a", "b")
		taker.h = h
		msg := send(t, h, "a", "b", "does it survive")
		quiet(t, h)
		walkDisks(t, taker, msg, []string{"intake", "deliver", "source-update"})
	})
	t.Run("an answer that carries evidence", func(t *testing.T) {
		taker := newDiskTaker(22, "deliver-reply")
		h := nodeWith(t, harness.Options{Observer: taker}, "a", "b")
		taker.h = h
		h.Unreachable(true)
		msg := send(t, h, "a", "b", "a question")
		quiet(t, h)
		h.HoldReports(string(msg))
		h.Unreachable(false)
		h.Advance(resendBackoff)
		quiet(t, h)
		res, err := h.Agent("b").Reply(h.Ctx(), fakeagent.ReplyInput{MessageID: msg, Body: "an answer"})
		if err != nil {
			t.Fatal(err)
		}
		quiet(t, h)
		walkDisks(t, taker, msg, []string{"deliver-reply"})
		// Wherever the node died, the answer is taken in at most once.
		for _, td := range taker.disks {
			t.Run(td.where+" answer", func(t *testing.T) {
				r := restarted(t, td.disk, "a", "b")
				if n := len(inRecords(t, r, res.MessageID)); n > 1 {
					t.Errorf("%d target records of the answer", n)
				}
			})
		}
	})
}

// walkDisks starts a node on every disk taken and checks it, and asserts that the
// walk reached every step of the transactions it watched.
func walkDisks(t *testing.T, taker *diskTaker, msg adapterproto.MessageID, txs []string) {
	t.Helper()
	if len(taker.disks) == 0 {
		t.Fatal("no step of the watched transactions was reached")
	}
	for _, tx := range txs {
		found := false
		for k := range taker.steps {
			found = found || k.Tag.Tx == tx
		}
		if !found {
			t.Errorf("transaction %s was never reached: the walk would prove nothing about it", tx)
		}
	}
	for _, td := range taker.disks {
		t.Run(td.where, func(t *testing.T) {
			h := restarted(t, td.disk, "a", "b")
			checkSettled(t, h, msg)
		})
	}
}
