package harness_test

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

// The generator of control signals reaches every path it names, and every record it
// makes ends: after the time the signals were allowed, no control record of the target
// is still accepted and no source record of a signal is still waiting for its outcome. A model test that judges the closing of every record starts from this.
func TestControlGenReachesEveryPathAndEveryControlRecordCloses(t *testing.T) {
	h := harness.New(t, harness.Options{Adapter: &harness.AdapterOptions{Host: hostParams(), Runtime: true}})
	h.MustDirect("a")
	h.MustDirect("b")
	if err := h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	g := harness.NewControlGen(h, 7, []string{"a", "b"}, []string{"a", "b"})
	const steps, minPerPath = 120, 3
	for i := 0; i < steps; i++ {
		if _, err := g.Next(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		step(t, h, time.Minute)
	}
	if g.Sent() != steps {
		t.Fatalf("%d signals sent, want %d", g.Sent(), steps)
	}
	for _, p := range harness.ControlPaths {
		if n := g.Paths()[p]; n < minPerPath {
			t.Errorf("path %q taken %d times, want at least %d: the generator no longer exercises it", p, n, minPerPath)
		}
	}

	// The longest a signal waits for the node to carry it out is a few minutes, and
	// the sender learns the reply on its next pull. The clock moves by minutes, as the
	// host's pings are due every half minute.
	for i := 0; i < 10; i++ {
		step(t, h, time.Minute)
	}
	sc, err := h.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Ctl) == 0 {
		t.Fatal("the target holds no control record")
	}
	for _, c := range sc.Ctl {
		if c.Record.State == delivery.StateAccepted {
			t.Errorf("the control record %s is still accepted after its deadlines", c.Key)
		}
	}
	for _, o := range sc.Out {
		if !o.Key.Control {
			continue
		}
		// The delivery of an initial message waits for the session to read it, which
		// nobody does here, and is not the signal's.
		for _, d := range o.Outbound.Recipients {
			if !d.Delivery.State.IsTerminal() {
				t.Errorf("the delivery of %s to %s is %s after its deadlines", o.Outbound.Message, d.Address, d.Delivery.State)
			}
		}
	}
}
