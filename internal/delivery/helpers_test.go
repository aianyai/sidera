package delivery

import (
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// t0 is the instant a test message is accepted by its sender's node; the
// default delivery deadline lies ten minutes after it.
var t0 = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return t0.Add(d) }

func node(n byte) ident.NodeID {
	b := make([]byte, ident.NodeIDLen)
	b[0] = n
	id, err := ident.NodeIDFromBytes(b)
	if err != nil {
		panic(err)
	}
	return id
}

func endpoint(n byte) ident.EndpointID {
	b := make([]byte, ident.EndpointIDLen)
	b[0] = n
	id, err := ident.EndpointIDFromBytes(b)
	if err != nil {
		panic(err)
	}
	return id
}

func addr(nodeN, epN byte) ident.Address {
	return ident.Address{Node: node(nodeN), Endpoint: endpoint(epN)}
}

// mustApply applies an event and fails the test on an error.
func mustApply(t *testing.T, r Record, now time.Time, ev Event) (Record, Outcome) {
	t.Helper()
	n, out, err := r.Apply(now, ev)
	if err != nil {
		t.Fatalf("Apply(%+v): %v", ev, err)
	}
	return n, out
}

func kindsOf(fbs []Feedback) []Kind {
	var ks []Kind
	for _, f := range fbs {
		ks = append(ks, f.Kind)
	}
	return ks
}

func sameKinds(a, b []Kind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// targetRecord returns a target record accepted at acceptedAt for a message
// whose deadline is ten minutes after t0.
func targetRecord(t *testing.T, acceptedAt time.Time, maxQueue time.Duration) Record {
	t.Helper()
	ab := at(10 * time.Minute)
	r, err := NewTargetRecord(acceptedAt, ab, ReportBy(ab), maxQueue)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// handOut hands out chunk part of a body of chunks chunks and fails the test
// unless recv would really give the body out.
func handOut(t *testing.T, r Record, now time.Time, chunks, part int) Record {
	t.Helper()
	n, res, err := r.Handout(now, AuthorizationAllow, chunks, part, freshLease())
	if err != nil || (res.Action != HandoutGranted && res.Action != HandoutResent) {
		t.Fatalf("Handout(part %d of %d) at %v: %+v %v", part, chunks, now.Sub(t0), res, err)
	}
	return n
}

// handOutAll hands out every chunk of a body, each on a page of its own.
func handOutAll(t *testing.T, r Record, now time.Time, chunks int) Record {
	t.Helper()
	for part := 1; part <= chunks; part++ {
		r = handOut(t, r, now, chunks, part)
	}
	return r
}

// ack acknowledges one chunk with the right token and fails the test on an
// error.
func ack(t *testing.T, r Record, now time.Time, part int) (Record, AckVerdict, Outcome) {
	t.Helper()
	n, v, out, err := ackUnder(r, now, Ack{Part: part, TokenMatches: true})
	if err != nil {
		t.Fatalf("Ack(part %d): %v", part, err)
	}
	return n, v, out
}

// deliverBody takes a target record through the only road to delivered: the
// body is handed out in one chunk and acknowledged.
func deliverBody(t *testing.T, r Record, now time.Time) Record {
	t.Helper()
	r = handOut(t, r, now, 1, 1)
	r, v, _ := ack(t, r, now, 1)
	if v != AckCompleted || r.State != StateDelivered {
		t.Fatalf("acknowledgement: verdict %v, state %s", v, r.State)
	}
	return r
}

// counterReader is a deterministic, never-zero randomness source: it yields the
// big-endian counter, which is incremented per Read, as the whole read.
type counterReader struct {
	mu sync.Mutex
	n  uint64
}

func (c *counterReader) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	for i := range p {
		p[i] = 0
	}
	for i := 0; i < 8 && i < len(p); i++ {
		p[len(p)-1-i] = byte(c.n >> (8 * i))
	}
	return len(p), nil
}

// testRand is the randomness every test uses for lease IDs.
var testRand = &counterReader{}

// freshLease returns a new page identifier, as a caller generates one per recv.
func freshLease() LeaseID {
	id, err := NewLeaseID(testRand)
	if err != nil {
		panic(err)
	}
	return id
}

// current returns the identifier of the page the message was last handed out
// on; the zero ID before the first hand-out.
func (l Lease) current() LeaseID {
	if len(l.IDs) == 0 {
		return LeaseID{}
	}
	return l.IDs[len(l.IDs)-1]
}

// ackUnder acknowledges as the agent would: with the page the record was last
// handed out on, unless the acknowledgement names one itself.
func ackUnder(r Record, now time.Time, a Ack) (Record, AckVerdict, Outcome, error) {
	if a.Lease.IsZero() {
		a.Lease = r.Lease.current()
	}
	return r.Ack(now, a)
}
