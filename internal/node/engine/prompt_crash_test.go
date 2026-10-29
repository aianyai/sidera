package engine_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

// A crash walk of the writes of a prompt: before every step of the transaction that
// records the attempt as in flight and of the one that records how it ended, the disks
// a crash at that instant can leave are taken, and each is started as a node of its own,
// with an adapter and a runtime that know nothing of the session. Wherever the node
// died, the next start leaves no attempt in flight, loses no message and tells the
// sender nothing: a prompt is no failure, and one that was not recorded as sent is not
// sent as that attempt again.
func TestAPromptIsSettledOnceAfterACrashAtEveryPoint(t *testing.T) {
	walked := []string{"prompt-begin", "prompt-end"}
	cov := &coverage{hit: map[fsx.Key]int{}}
	taker := newDiskTaker(cov, walked...)
	h := sessionNode(t, harness.Options{Observer: taker}, "a")
	taker.h = h
	_, addr := idleSession(t, h, "a")
	msg := sendToAddr(t, h, "a", addr, "sent while the node is walked")
	quiet(t, h)
	if len(taker.disks) == 0 {
		t.Fatal("no step of the watched transactions was reached")
	}
	sent := promptsInto(t, h, addr)
	if len(sent) != 1 {
		t.Fatalf("%d prompts were sent, want 1", len(sent))
	}
	h.Stop()
	for _, td := range taker.disks {
		t.Run(td.where, func(t *testing.T) {
			n := restartedWithRuntime(t, td)
			sc := scanOf(t, n)
			for _, a := range sc.Attempts {
				if a.InFlight != nil {
					t.Errorf("the attempt %s of %s is still in flight after the start", a.InFlight.Attempt, a.Endpoint)
				}
			}
			ins := inRecords(t, n, msg)
			if len(ins) != 1 || ins[0].Record.State != delivery.StateAccepted {
				t.Fatalf("the message has %d target records %+v, want one that is accepted", len(ins), ins)
			}
			if kinds := toldKinds(t, n, "a"); len(kinds) != 0 {
				t.Errorf("the sender was told %v of a message that was only prompted for", kinds)
			}
			log, err := n.Ledger()
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range log.Kind(ledger.KindPromptDeliverReceived) {
				if e.Attrs.Attempt == sent[0].Attrs.Attempt {
					t.Errorf("the attempt %s was sent again after the crash", e.Attrs.Attempt)
				}
			}
			checkNoOrphanBody(t, sc)
		})
	}
	for _, k := range harness.ProgramTable(t).Keys() {
		for _, tx := range walked {
			if k.Tag.Tx == tx && cov.hit[k.Key] == 0 {
				t.Errorf("the step %s %q of transaction %s was never reached", k.Step, k.Tag.Role, tx)
			}
		}
	}
}
