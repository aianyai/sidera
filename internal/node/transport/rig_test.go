package transport_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/record"
)

var t0 = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

func nodeOf(t testing.TB, n byte) ident.NodeID {
	t.Helper()
	signer, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{n}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	id, err := ident.NodeIDFromKey(signer.Public())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func ep(b byte) ident.EndpointID {
	id, err := ident.EndpointIDFromBytes(bytes.Repeat([]byte{b}, ident.EndpointIDLen))
	if err != nil {
		panic(err)
	}
	return id
}

// errConflict is what the fake target answers to a message id it already holds
// with another digest.
var errConflict = errors.New("same id, different content")

// inbound is one call of the engine's side that the target saw.
type inbound struct {
	from   transport.Origin
	sender ident.EndpointID
	env    envelope.Envelope
}

// target stands for the engine behind the seam. It keeps what it was handed and
// the digest of each message id, the way the receiving side keeps a record per
// id, and answers every message with one report that names the id, so a test can
// tell which message a report is about. It judges nothing else: the rules of the
// receiving side are not the transport's.
type target struct {
	mu      sync.Mutex
	calls   []inbound
	pulls   []transport.PullRequest
	held    map[delivery.MessageID]delivery.BodyDigest
	failing error // answered, once, in place of the report
}

func newTarget() *target { return &target{held: map[delivery.MessageID]delivery.BodyDigest{}} }

func reportOf(id delivery.MessageID) record.Record {
	return record.Record{Type: "test/report", Payload: []byte(id)}
}

func (g *target) Inbound(_ context.Context, from transport.Origin, sender ident.EndpointID, data []byte) (transport.Receipt, error) {
	e, err := envelope.Decode(data, envelope.DefaultLimits())
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		return transport.Receipt{}, err
	}
	g.calls = append(g.calls, inbound{from, sender, e})
	if g.failing != nil {
		err := g.failing
		g.failing = nil
		return transport.Receipt{}, err
	}
	if d, ok := g.held[e.ID]; ok && d != e.Digest() {
		return transport.Receipt{}, errConflict
	}
	g.held[e.ID] = e.Digest()
	return transport.Receipt{Reports: []record.Record{reportOf(e.ID)}}, nil
}

func (g *target) Pull(_ context.Context, _ transport.Origin, req transport.PullRequest) (transport.PullAnswer, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pulls = append(g.pulls, req)
	if _, ok := g.held[req.Message]; !ok {
		return transport.PullAnswer{}, nil
	}
	return transport.PullAnswer{Reports: []record.Record{reportOf(req.Message)}}, nil
}

func (g *target) failNext(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failing = err
}

func (g *target) inbound() []inbound {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]inbound(nil), g.calls...)
}

func (g *target) pulled() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.pulls)
}

// rig is one node with a loopback on a fake clock, in front of a fake target.
type rig struct {
	self, other ident.NodeID
	clk         *clocktest.Fake
	knobs       *transport.Knobs
	target      *target
	loop        *transport.Loopback
	hits        []transport.Hit // what the observer saw
	hitsMu      sync.Mutex
}

func newRig(t testing.TB) *rig {
	t.Helper()
	guardLeaks(t)
	r := &rig{self: nodeOf(t, 1), other: nodeOf(t, 2), clk: clocktest.NewFake(t0), target: newTarget()}
	r.knobs = transport.NewKnobs(r.clk)
	r.knobs.Observe(func(h transport.Hit) {
		r.hitsMu.Lock()
		defer r.hitsMu.Unlock()
		r.hits = append(r.hits, h)
	})
	r.loop = transport.NewLoopback(r.self, r.target, r.knobs)
	return r
}

// message is a plain message to the endpoint 1 of the node, with a body.
func (r *rig) message(t testing.TB, id, body string) []byte {
	t.Helper()
	e := envelope.Envelope{
		ID: delivery.MessageID(id), To: []ident.Address{{Node: r.self, Endpoint: ep(1)}}, Kind: envelope.KindMessage,
		Seq:    map[ident.EndpointID]uint64{ep(1): 1},
		SentAt: t0, AcceptBefore: t0.Add(time.Hour), ReportBy: delivery.ReportBy(t0.Add(time.Hour)),
		ContentType: "text/plain", Body: []byte(body),
	}
	b, err := e.Encode(envelope.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// status is a control signal without a body.
func (r *rig) status(t testing.TB, id string) []byte {
	t.Helper()
	e := envelope.Envelope{
		ID: delivery.MessageID(id), To: []ident.Address{{Node: r.self, Endpoint: ep(1)}}, Kind: envelope.KindEndpointStatus,
		Seq:    map[ident.EndpointID]uint64{ep(1): 1},
		SentAt: t0, AcceptBefore: t0.Add(time.Hour), ReportBy: delivery.ReportBy(t0.Add(time.Hour)),
	}
	b, err := e.Encode(envelope.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (r *rig) send(data []byte) (transport.Receipt, error) {
	return r.loop.Send(context.Background(), r.self, ep(9), data)
}

func (r *rig) pull(id string) (transport.PullAnswer, error) {
	return r.loop.Pull(context.Background(), r.self, transport.PullRequest{Message: delivery.MessageID(id)})
}

// hitsSeen returns what the observer was told, per knob.
func (r *rig) hitsSeen() map[transport.Knob]int {
	r.hitsMu.Lock()
	defer r.hitsMu.Unlock()
	seen := map[transport.Knob]int{}
	for _, h := range r.hits {
		seen[h.Knob]++
	}
	return seen
}

var allKnobs = []transport.Knob{
	transport.KnobDropReceipt, transport.KnobUnreachable, transport.KnobHoldReports, transport.KnobDelay, transport.KnobMutate,
}

// wantHits asserts how often each knob was hit, by the counter and by what the
// observer was told, and that every knob not named was not hit. A knob test that
// did not check this could pass on a knob that was never reached.
func (r *rig) wantHits(t testing.TB, want map[transport.Knob]int) {
	t.Helper()
	seen := r.hitsSeen()
	for _, k := range allKnobs {
		if got := r.knobs.Hits(k); got != want[k] {
			t.Errorf("%v hit %d times, want %d", k, got, want[k])
		}
		if seen[k] != want[k] {
			t.Errorf("the observer saw %v hit %d times, want %d", k, seen[k], want[k])
		}
	}
}

func reportIDs(t testing.TB, rs []record.Record) []string {
	t.Helper()
	var ids []string
	for _, r := range rs {
		if r.Type != "test/report" {
			t.Fatalf("report of type %q", r.Type)
		}
		ids = append(ids, string(r.Payload))
	}
	return ids
}

func sameIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
