package preauth

import (
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// The reference below is a second implementation of the accounting rules, written
// for this test from the rules and not from the code: the books are a plain
// slice searched linearly, every entry names the connection that owns it, and
// every count is recomputed from scratch at each question, so a bookkeeping
// error in the incremental counters of the real one cannot hide behind the same
// error here.

// refEntry is one inbound connection on the books. owner is the identifier of
// the connection that created it; the real model has no such identifier, which
// is the point: the test knows it, so it can say whether a live connection is on
// the books.
type refEntry struct {
	owner       int
	peer, addr  int
	nonMember   bool
	exempt      bool
	securedAt   time.Time
	connected   bool
	connectedAt time.Time
}

type reference struct {
	p       Params
	entries []*refEntry
	learned map[int]bool
	held    map[int]bool
}

func (r *reference) isKnown(peer int) bool { return r.learned[peer] || r.held[peer] }

func (r *reference) find(peer, addr int) *refEntry {
	for _, e := range r.entries {
		if e.peer == peer && e.addr == addr {
			return e
		}
	}
	return nil
}

func (r *reference) ownedBy(id int) *refEntry {
	for _, e := range r.entries {
		if e.owner == id {
			return e
		}
	}
	return nil
}

func (r *reference) drop(e *refEntry) {
	out := r.entries[:0]
	for _, o := range r.entries {
		if o != e {
			out = append(out, o)
		}
	}
	r.entries = out
}

func (r *reference) counts() Counts {
	var c Counts
	for _, o := range r.entries {
		switch {
		case o.nonMember:
			c.NonMembers++
		case o.exempt:
			c.Exempt++
		default:
			c.Unauthenticated++
		}
	}
	return c
}

func (r *reference) secured(owner, peer, addr int, inbound bool, now time.Time) Admission {
	if !inbound {
		return Admission{VerdictAllow, AdmitOutbound}
	}
	if r.find(peer, addr) != nil {
		return Admission{VerdictRefuse, RefuseDuplicateKey}
	}
	if r.isKnown(peer) {
		r.entries = append(r.entries, &refEntry{owner: owner, peer: peer, addr: addr, exempt: true, securedAt: now})
		return Admission{VerdictAllow, AdmitKnownMember}
	}
	if r.counts().Unauthenticated >= r.p.UnauthenticatedCap {
		return Admission{VerdictRefuse, RefuseCapReached}
	}
	r.entries = append(r.entries, &refEntry{owner: owner, peer: peer, addr: addr, securedAt: now})
	return Admission{VerdictAllow, AdmitCounted}
}

func (r *reference) connect(peer, addr int, now time.Time) bool {
	e := r.find(peer, addr)
	if e == nil {
		return false
	}
	if !e.connected {
		e.connected, e.connectedAt = true, now
	}
	return true
}

func (r *reference) completed(peer, addr int, s State) Placement {
	if s == StateUnauthenticated {
		return Placement{VerdictRefuse, RefuseNotCompleted}
	}
	e := r.find(peer, addr)
	if s == StateMember || s == StateAuthorizedPerson {
		r.learned[peer] = true
		if e == nil {
			return Placement{VerdictAllow, PlaceUntracked}
		}
		r.drop(e)
		return Placement{VerdictAllow, PlaceReleased}
	}
	r.learned[peer], r.held[peer] = false, false
	switch {
	case e == nil:
		return Placement{VerdictAllow, PlaceUntracked}
	case e.nonMember:
		return Placement{VerdictAllow, PlaceAlreadyNonMember}
	case r.counts().NonMembers >= r.p.NonMemberConcurrency:
		r.drop(e)
		return Placement{VerdictRefuse, RefuseNonMemberFull}
	}
	e.nonMember, e.exempt = true, false
	return Placement{VerdictAllow, PlaceNonMember}
}

func (r *reference) disconnect(peer, addr int) {
	if e := r.find(peer, addr); e != nil {
		r.drop(e)
	}
}

func (r *reference) sweep(now time.Time) []*refEntry {
	var out []*refEntry
	for _, e := range r.entries {
		if !e.connected && now.Sub(e.securedAt) >= r.p.SweepInterval {
			out = append(out, e)
		}
	}
	for _, e := range out {
		r.drop(e)
	}
	return out
}

func (r *reference) overdue(now time.Time) []*refEntry {
	var out []*refEntry
	for _, e := range r.entries {
		if !e.connected {
			continue
		}
		limit := ExchangeDeadline
		if e.nonMember {
			limit = r.p.NonMemberLifetime
		}
		if now.Sub(e.connectedAt) >= limit {
			out = append(out, e)
		}
	}
	return out
}

func sameKeySet(a, b []ConnKey) bool {
	if len(a) != len(b) {
		return false
	}
	norm := func(ks []ConnKey) []string {
		var s []string
		for _, k := range ks {
			s = append(s, string(k.Peer.Bytes())+"|"+k.Addr)
		}
		sort.Strings(s)
		return s
	}
	x, y := norm(a), norm(b)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

type stage uint8

const (
	// stageHandshake: the secure handshake is done and the gater let the
	// connection through; the library has not established it yet. A connection
	// can be rejected here with no event at all.
	stageHandshake stage = iota
	// stageLive: the connection is established.
	stageLive
	// stageClosed: the connection ended, or the network layer closed it.
	stageClosed
)

// simConn is one connection as the network layer sees it. The layer reports to
// the model by key only, remembers per connection whether it is the accounted
// one (Connected answered true and Completed has not released it since) and
// reports a disconnect for the accounted connection only; that is the contract
// the model documents, and accounted records the layer's side of it.
type simConn struct {
	id        int
	peer      int
	addr      int // an index into the inbound addresses; outbound connections use outboundAddr
	inbound   bool
	stage     stage
	accounted bool
	released  bool
}

// outboundAddr keeps the keys of outbound connections apart from the inbound
// ones: the remote address of an outbound connection is a listen address, the
// one of an inbound connection a source address, and a connection that is both
// is the hole-punched one, which is a single connection.
const outboundAddr = -1

// TestAccountingAgainstTheReference drives the model and the reference with the
// same random sequence of what a network layer does with connections it tracks
// one by one: handshakes (inbound and outbound), rejection after the handshake
// without any event, connections established, completed exchanges (member,
// authorized person, non-member, and the refused exchange that closes the
// connection), disconnects, learning and forgetting members, sweeps, and the
// closing of overdue connections. Time moves in seconds, like the mechanism: the
// exchange deadline is 5 s, the sweep interval 3 s and the lifetime 40 s. Every
// answer and the counts are compared at every step.
//
// Beyond the answers, the test asserts what the bookkeeping is for: every live
// inbound connection is either on the books, bound by a deadline the model
// reports through Overdue, or has been released as a member or an authorized
// person; every connection the model refused or the layer closed is off the
// books, and every entry has a connection behind it that is not yet closed unless
// the entry was never connected (the sweep removes it). The seed is fixed, and
// the test asserts that each path that matters was taken often enough to count.
func TestAccountingAgainstTheReference(t *testing.T) {
	const (
		steps = 40000
		peers = 12
		addrs = 3
	)
	p := Params{UnauthenticatedCap: 3, NonMemberConcurrency: 2, NonMemberLifetime: 40 * time.Second, PairingOnlineTimeout: 30 * time.Second, SweepInterval: 3 * time.Second}
	a, err := NewAccounting(p)
	if err != nil {
		t.Fatal(err)
	}
	ref := &reference{p: p, learned: map[int]bool{}, held: map[int]bool{}}
	rng := rand.New(rand.NewSource(20261023))

	pubs := make([]ident.PublicKey, peers)
	for i := range pubs {
		pubs[i] = seedSigner(t, byte(0x40+i)).Public()
	}
	keyOfConn := func(c *simConn) ConnKey {
		if !c.inbound {
			return ConnKey{Peer: pubs[c.peer], Addr: "outbound"}
		}
		return ConnKey{Peer: pubs[c.peer], Addr: string(rune('a' + c.addr))}
	}
	addrOf := func(c *simConn) int {
		if !c.inbound {
			return outboundAddr
		}
		return c.addr
	}

	var conns []*simConn // every connection that is not closed
	nextID := 0
	closeConn := func(c *simConn) {
		c.stage, c.accounted = stageClosed, false
		for i, o := range conns {
			if o == c {
				conns = append(conns[:i], conns[i+1:]...)
				break
			}
		}
	}
	pick := func(match func(c *simConn) bool) *simConn {
		var m []*simConn
		for _, c := range conns {
			if match(c) {
				m = append(m, c)
			}
		}
		if len(m) == 0 {
			return nil
		}
		return m[rng.Intn(len(m))]
	}
	// endAccounted is the layer ending a connection: it reports the disconnect
	// to the model for the accounted connection only.
	endAccounted := func(c *simConn) {
		if c.accounted {
			a.Disconnected(keyOfConn(c))
			ref.disconnect(c.peer, addrOf(c))
		}
		closeConn(c)
	}

	cover := map[string]int{}
	now := t0
	states := []State{StateMember, StateAuthorizedPerson, StateNonMemberNode, StateNonMemberNode, StateNonMemberNode, StateNonMemberPerson, StateNonMemberPerson, StateUnauthenticated}
	for step := 0; step < steps; step++ {
		now = now.Add(time.Duration(rng.Intn(3)) * time.Second) // 0, 1 or 2 seconds
		ev := rng.Intn(100)
		switch {
		case ev < 28:
			// A handshake is done. A quarter of the inbound ones come from the key
			// of a connection that is on the books, the retry or the second
			// connection the duplicate rule is about.
			c := &simConn{id: nextID, peer: rng.Intn(peers), addr: rng.Intn(addrs), inbound: rng.Intn(6) != 0}
			nextID++
			if c.inbound && len(ref.entries) > 0 && rng.Intn(4) == 0 {
				e := ref.entries[rng.Intn(len(ref.entries))]
				c.peer, c.addr = e.peer, e.addr
			}
			dir := DirectionOutbound
			if c.inbound {
				dir = DirectionInbound
			}
			before := ref.find(c.peer, addrOf(c))
			capBefore := ref.counts().Unauthenticated >= p.UnauthenticatedCap
			want := ref.secured(c.id, c.peer, addrOf(c), c.inbound, now)
			if got := a.Secured(keyOfConn(c), dir, now); got != want {
				t.Fatalf("step %d: Secured(%d,%d) = %v/%v, want %v/%v", step, c.peer, c.addr, got.Verdict, got.Reason, want.Verdict, want.Reason)
			}
			cover["secured:"+want.Reason.String()]++
			switch {
			case want.Reason == AdmitKnownMember && capBefore:
				cover["exempt-while-cap-reached"]++
			case want.Reason == AdmitKnownMember && ref.held[c.peer] && !ref.learned[c.peer]:
				cover["exempt-by-held-certificate"]++
			case want.Reason == RefuseDuplicateKey:
				switch {
				case before.nonMember:
					cover["duplicate-of-a-non-member"]++
				case before.exempt:
					cover["duplicate-of-a-known-member"]++
				default:
					cover["duplicate-of-an-unauthenticated-connection"]++
				}
				if oc := connByID(conns, before.owner); oc == nil || oc.stage == stageClosed {
					cover["duplicate-of-a-lost-entry"]++
				}
			}
			if want.Verdict == VerdictAllow {
				conns = append(conns, c)
			}
			// A refused connection is closed by the layer with no further event for
			// its key.
		case ev < 50:
			// The library establishes a connection that the handshake let through.
			// The layer reports every established connection and remembers the
			// answer.
			c := pick(func(c *simConn) bool { return c.stage == stageHandshake })
			if c == nil {
				break
			}
			want := ref.connect(c.peer, addrOf(c), now)
			got := a.Connected(keyOfConn(c), now)
			if got != want {
				t.Fatalf("step %d: Connected = %v, want %v", step, got, want)
			}
			if c.inbound && !got {
				// The precondition of SweepInterval was broken for this connection.
				// The sweep below closes such connections before they can come up,
				// so this cannot happen in this test.
				t.Fatalf("step %d: an inbound connection the books let through is not on them when it comes up", step)
			}
			c.stage, c.accounted = stageLive, got
		case ev < 66:
			// An exchange on an accounted inbound connection or on an outbound one
			// is decided. A released connection has no further exchange.
			c := pick(func(c *simConn) bool { return c.stage == stageLive && (c.accounted || !c.inbound) })
			if c == nil {
				break
			}
			s := states[rng.Intn(len(states))]
			want := ref.completed(c.peer, addrOf(c), s)
			got := a.Completed(keyOfConn(c), s, now)
			if got != want {
				t.Fatalf("step %d: Completed(%v) = %v/%v, want %v/%v", step, s, got.Verdict, got.Reason, want.Verdict, want.Reason)
			}
			cover["completed:"+want.Reason.String()]++
			switch want.Reason {
			case PlaceReleased:
				c.accounted, c.released = false, true
			case RefuseNonMemberFull:
				closeConn(c) // the entry is gone; the layer closes without reporting
			case RefuseNotCompleted:
				endAccounted(c) // a refused exchange closes the connection
			}
			if s == StateNonMemberPerson && want.Reason == PlaceNonMember {
				cover["non-member-person-placed"]++
			}
		case ev < 76:
			// A connection ends: an established one, or one rejected after the
			// handshake, which produces no event.
			c := pick(func(c *simConn) bool { return true })
			if c == nil {
				break
			}
			switch {
			case c.stage == stageHandshake:
				cover["rejected-after-the-handshake"]++
				closeConn(c)
			case c.released:
				if e := ref.find(c.peer, addrOf(c)); e != nil {
					cover["released-connection-ended-beside-a-newer-entry"]++
				}
				closeConn(c)
			default:
				endAccounted(c)
			}
		case ev < 82:
			// The certificates held change: a random few peers are held members.
			var held []ident.PublicKey
			ref.held = map[int]bool{}
			if rng.Intn(4) == 0 {
				for i := range pubs {
					if rng.Intn(6) == 0 {
						ref.held[i] = true
						held = append(held, pubs[i])
					}
				}
			}
			a.SetHeld(held)
		case ev < 88:
			wantEntries := ref.sweep(now)
			var want []ConnKey
			for _, e := range wantEntries {
				want = append(want, ConnKey{Peer: pubs[e.peer], Addr: string(rune('a' + e.addr))})
			}
			got := a.Sweep(now)
			if !sameKeySet(got, want) {
				t.Fatalf("step %d: Sweep = %v, want %v", step, got, want)
			}
			// A connection whose entry was swept before it came up is a connection
			// that took longer than SweepInterval, which the parameter forbids; in
			// this test it never comes up.
			for _, e := range wantEntries {
				if c := connByID(conns, e.owner); c != nil && c.stage == stageHandshake {
					closeConn(c)
				}
			}
			if len(want) > 0 {
				cover["sweep-removed-entries"] += len(want)
				cover["sweep-calls-with-removals"]++
			}
		default:
			wantEntries := ref.overdue(now)
			var want []ConnKey
			for _, e := range wantEntries {
				want = append(want, ConnKey{Peer: pubs[e.peer], Addr: string(rune('a' + e.addr))})
				if e.nonMember {
					cover["overdue-non-member"]++
				} else {
					cover["overdue-unauthenticated"]++
				}
			}
			got := a.Overdue(now)
			if !sameKeySet(got, want) {
				t.Fatalf("step %d: Overdue = %v, want %v", step, got, want)
			}
			// The layer closes what is overdue, which ends the connection.
			for _, e := range wantEntries {
				c := connByID(conns, e.owner)
				if c == nil || !c.accounted {
					t.Fatalf("step %d: an overdue entry has no accounted live connection behind it", step)
				}
				endAccounted(c)
			}
		}

		checkInvariants(t, step, a, ref, conns, pubs, p, now)
	}

	// Close everything and let the sweep run: nothing may remain on the books.
	for _, c := range append([]*simConn(nil), conns...) {
		if c.stage == stageLive {
			endAccounted(c)
		} else {
			closeConn(c)
		}
	}
	now = now.Add(2 * p.SweepInterval)
	a.Sweep(now)
	ref.sweep(now)
	if got := a.Counts(); got != (Counts{}) {
		t.Errorf("entries remain after every connection ended and the sweep ran: %+v", got)
	}
	if len(ref.entries) != 0 {
		t.Errorf("the reference still holds %d entries", len(ref.entries))
	}

	// The paths that matter were taken, many times. The thresholds are far below
	// what the fixed seed produces and well above chance.
	for name, min := range map[string]int{
		"secured:" + AdmitCounted.String():               2000,
		"secured:" + AdmitKnownMember.String():           500,
		"secured:" + RefuseCapReached.String():           300,
		"secured:" + AdmitOutbound.String():              1000,
		"secured:" + RefuseDuplicateKey.String():         500,
		"duplicate-of-an-unauthenticated-connection":     100,
		"duplicate-of-a-non-member":                      50,
		"duplicate-of-a-known-member":                    100,
		"duplicate-of-a-lost-entry":                      50,
		"rejected-after-the-handshake":                   300,
		"released-connection-ended-beside-a-newer-entry": 20,
		"exempt-while-cap-reached":                       100,
		"exempt-by-held-certificate":                     100,
		"completed:" + PlaceReleased.String():            200,
		"completed:" + PlaceNonMember.String():           200,
		"completed:" + RefuseNonMemberFull.String():      30,
		"completed:" + PlaceAlreadyNonMember.String():    20,
		"completed:" + PlaceUntracked.String():           200,
		"completed:" + RefuseNotCompleted.String():       200,
		"non-member-person-placed":                       50,
		"sweep-calls-with-removals":                      200,
		"sweep-removed-entries":                          200,
		"overdue-unauthenticated":                        200,
		"overdue-non-member":                             100,
	} {
		if cover[name] < min {
			t.Errorf("path %q taken %d times, want at least %d", name, cover[name], min)
		}
	}
	if t.Failed() || testing.Verbose() {
		t.Logf("coverage: %v", cover)
	}
}

func connByID(conns []*simConn, id int) *simConn {
	for _, c := range conns {
		if c.id == id {
			return c
		}
	}
	return nil
}

// checkInvariants compares the model with the reference and asserts the
// properties of the bookkeeping itself, at every step.
func checkInvariants(t *testing.T, step int, a *Accounting, ref *reference, conns []*simConn, pubs []ident.PublicKey, p Params, now time.Time) {
	t.Helper()
	if got, want := a.Counts(), ref.counts(); got != want {
		t.Fatalf("step %d: counts %+v, want %+v", step, got, want)
	}
	if c := a.Counts(); c.Unauthenticated > p.UnauthenticatedCap || c.NonMembers > p.NonMemberConcurrency {
		t.Fatalf("step %d: a limit is exceeded: %+v", step, c)
	}
	for i := range pubs {
		if a.Known(pubs[i]) != ref.isKnown(i) {
			t.Fatalf("step %d: Known(%d) = %v", step, i, a.Known(pubs[i]))
		}
	}

	// The model's view of who must be closed now is the reference's, so a
	// connection past its deadline cannot be missing from it.
	var want []ConnKey
	for _, e := range ref.overdue(now) {
		want = append(want, ConnKey{Peer: pubs[e.peer], Addr: string(rune('a' + e.addr))})
	}
	if got := a.Overdue(now); !sameKeySet(got, want) {
		t.Fatalf("step %d: Overdue = %v, want %v", step, got, want)
	}

	// Every live inbound connection is on the books and bound by a deadline, or
	// has been released as a member or an authorized person.
	for _, c := range conns {
		e := ref.ownedBy(c.id)
		switch {
		case c.stage == stageLive && c.inbound && !c.released:
			if e == nil || !e.connected {
				t.Fatalf("step %d: live inbound connection %d (peer %d, address %d) is not on the books", step, c.id, c.peer, c.addr)
			}
			if !c.accounted {
				t.Fatalf("step %d: live inbound connection %d on the books is not the accounted one", step, c.id)
			}
		case c.stage == stageLive && c.inbound && c.released:
			if e != nil {
				t.Fatalf("step %d: released connection %d still owns an entry", step, c.id)
			}
		}
	}
	// Every entry has a connection behind it, unless it was never connected
	// (rejected after the handshake), in which case the sweep removes it.
	for _, e := range ref.entries {
		c := connByID(conns, e.owner)
		if c == nil && e.connected {
			t.Fatalf("step %d: a connected entry (peer %d, address %d) has no open connection behind it", step, e.peer, e.addr)
		}
	}
}
