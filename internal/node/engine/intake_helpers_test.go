package engine_test

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The helpers of the tests of sending and intake. They read what happened from the
// result of a call, the ledger the fixture wrote, the answers of the loopback and,
// where the scenario is judged by the records, the records on the disk.

// inWriteCounter is a transaction observer that counts the writes of target
// records: every file a transaction creates under its "in" role. A test that says
// a record was not written again compares the count before and after, because a
// record written again with the content it had cannot be told from the disk.
type inWriteCounter struct{ n atomic.Int64 }

func (c *inWriteCounter) Reached(p fsx.Point, _ txn.Kind) {
	if p.Tag.Role == "in" && p.Step == fsx.StepCreate {
		c.n.Add(1)
	}
}

// writes is how many target records have been written so far.
func (c *inWriteCounter) writes() int { return int(c.n.Load()) }

// nodeWith makes a fixture and registers a direct endpoint under each name.
func nodeWith(t *testing.T, o harness.Options, names ...string) *harness.H {
	t.Helper()
	h := harness.New(t, o)
	for _, n := range names {
		h.MustDirect(n)
	}
	return h
}

// addrOf is the address of the endpoint registered as name.
func addrOf(t testing.TB, h *harness.H, name string) ident.Address {
	t.Helper()
	a, err := ident.ParseAddress(h.Addr(name))
	if err != nil {
		t.Fatalf("the address of %s: %v", name, err)
	}
	return a
}

// trySend sends a text from one endpoint to another as its agent does and returns
// what the agent got.
func trySend(h *harness.H, from, to, body, idemKey string) (fakeagent.SendResult, error) {
	return h.Agent(from).Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr(to)}, Body: body, IdemKey: idemKey})
}

// send is trySend for a send that must be accepted.
func send(t testing.TB, h *harness.H, from, to, body string) adapterproto.MessageID {
	t.Helper()
	res, err := trySend(h, from, to, body, "")
	if err != nil {
		t.Fatalf("send %s -> %s: %v", from, to, err)
	}
	return res.MessageID
}

// settle waits until the node has stopped reacting to what the test did.
func quiet(t testing.TB, h *harness.H) {
	t.Helper()
	if err := h.Quiesce(); err != nil {
		t.Fatalf("the node did not settle: %v", err)
	}
}

// scanOf reads the records on the disk. Read them after settle.
func scanOf(t testing.TB, h *harness.H) *store.Scan {
	t.Helper()
	sc, err := h.Records()
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	return sc
}

// inRecords are the target records of a message, one for each recipient that has
// one.
func inRecords(t testing.TB, h *harness.H, msg adapterproto.MessageID) []store.InRecord {
	t.Helper()
	var out []store.InRecord
	for _, in := range scanOf(t, h).In {
		if in.Key.Message == delivery.MessageID(msg) {
			out = append(out, in)
		}
	}
	return out
}

// inOf is the one target record of a message; the test fails when there are not
// exactly one.
func inOf(t testing.TB, h *harness.H, msg adapterproto.MessageID) store.InRecord {
	t.Helper()
	ins := inRecords(t, h, msg)
	if len(ins) != 1 {
		t.Fatalf("message %s has %d target records, want 1", msg, len(ins))
	}
	return ins[0]
}

// outOf is the one source record of a message.
func outOf(t testing.TB, h *harness.H, msg adapterproto.MessageID) store.OutRecord {
	t.Helper()
	var found []store.OutRecord
	for _, o := range scanOf(t, h).Out {
		if o.Key.Message == delivery.MessageID(msg) {
			found = append(found, o)
		}
	}
	if len(found) != 1 {
		t.Fatalf("message %s has %d source records, want 1", msg, len(found))
	}
	return found[0]
}

// sourceDelivery is the sender's record of the delivery of a message to its first
// recipient.
func sourceDelivery(t testing.TB, h *harness.H, msg adapterproto.MessageID) delivery.Record {
	t.Helper()
	o := outOf(t, h, msg)
	if len(o.Outbound.Recipients) == 0 {
		t.Fatalf("the source record of %s has no recipient", msg)
	}
	return o.Outbound.Recipients[0].Delivery
}

// knobHits counts the hits of each knob the fixture wrote to the ledger.
func knobHits(t testing.TB, h *harness.H) map[string]int {
	t.Helper()
	log, err := h.Ledger()
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	hits := map[string]int{}
	for _, e := range log.Kind(ledger.KindKnobHit) {
		hits[e.Attrs.Knob]++
	}
	return hits
}

// callHits counts the calls of the kind ("send" or "pull") that the loopback made
// unreachable for the node, as the knob hits of the ledger tell. Make the node
// unreachable before the call is made, or it is not counted.
func callHits(t testing.TB, h *harness.H, call string, node ident.NodeID) int {
	t.Helper()
	log, err := h.Ledger()
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	n := 0
	for _, e := range log.Kind(ledger.KindKnobHit) {
		if e.Attrs.Knob == "unreachable" && strings.Contains(e.Attrs.Detail, "call="+call+" ") && strings.Contains(e.Attrs.Detail, "node="+node.String()) {
			n++
		}
	}
	return n
}

// clientResults are the results of the calls an endpoint's agent made, in order.
func clientResults(t testing.TB, h *harness.H, from string) []ledger.Entry {
	t.Helper()
	log, err := h.Ledger()
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	return log.Select(func(e ledger.Entry) bool {
		return e.Kind == ledger.KindClientResult && e.Writer == "agent/"+addrOf(t, h, from).Endpoint.String()
	})
}

// resendBackoff and pullInterval are how long the sender waits before it sends
// again after no answer and before it asks again for a report.
var (
	resendBackoff = engine.DefaultParams().ResendBackoff
	pullInterval  = engine.DefaultParams().PullInterval
)

// forged is an envelope from a third party that names a message as the one it
// answers, the way a node that was never sent the message would make it.
func forged(t testing.TB, h *harness.H, id string, to ident.Address, replyTo adapterproto.MessageID, body string, evidence ...record.Record) []byte {
	t.Helper()
	now := h.Clock().Now().Truncate(time.Second)
	accept := now.Add(delivery.DefaultAcceptBefore)
	env := envelope.Envelope{
		ID: delivery.MessageID(id), To: []ident.Address{to}, Kind: envelope.KindMessage,
		ReplyTo: delivery.MessageID(replyTo), Seq: map[ident.EndpointID]uint64{to.Endpoint: 1},
		SentAt: now, AcceptBefore: accept, ReportBy: delivery.ReportBy(accept),
		ContentType: "text/plain", Body: []byte(body), Evidence: evidence,
	}
	b, err := env.Encode(envelope.DefaultLimits())
	if err != nil {
		t.Fatalf("encode the forged envelope: %v", err)
	}
	return b
}
