package preauth

import (
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// The reference below is a second implementation of the accounting rules, written
// for this test from the rules and not from the code: connections are a plain
// slice searched linearly, and every count is recomputed from scratch at each
// question, so a bookkeeping error in the incremental counters of the real one
// cannot hide behind the same error here.

type refConn struct {
	peer, addr  int
	nonMember   bool
	exempt      bool
	securedAt   time.Time
	connected   bool
	connectedAt time.Time
}

type reference struct {
	p       Params
	conns   []*refConn
	learned map[int]bool
	held    map[int]bool
}

func (r *reference) isKnown(peer int) bool { return r.learned[peer] || r.held[peer] }

func (r *reference) find(peer, addr int) *refConn {
	for _, c := range r.conns {
		if c.peer == peer && c.addr == addr {
			return c
		}
	}
	return nil
}

func (r *reference) drop(c *refConn) {
	out := r.conns[:0]
	for _, o := range r.conns {
		if o != c {
			out = append(out, o)
		}
	}
	r.conns = out
}

func (r *reference) counts() Counts {
	var c Counts
	for _, o := range r.conns {
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

func (r *reference) secured(peer, addr int, inbound bool, now time.Time) Admission {
	if !inbound {
		return Admission{VerdictAllow, AdmitOutbound}
	}
	if r.find(peer, addr) != nil {
		return Admission{VerdictAllow, AdmitAlreadyTracked}
	}
	if r.isKnown(peer) {
		r.conns = append(r.conns, &refConn{peer: peer, addr: addr, exempt: true, securedAt: now})
		return Admission{VerdictAllow, AdmitKnownMember}
	}
	if r.counts().Unauthenticated >= r.p.UnauthenticatedCap {
		return Admission{VerdictRefuse, RefuseCapReached}
	}
	r.conns = append(r.conns, &refConn{peer: peer, addr: addr, securedAt: now})
	return Admission{VerdictAllow, AdmitCounted}
}

func (r *reference) connect(peer, addr int, now time.Time) bool {
	c := r.find(peer, addr)
	if c == nil {
		return false
	}
	if !c.connected {
		c.connected, c.connectedAt = true, now
	}
	return true
}

func (r *reference) completed(peer, addr int, s State) Placement {
	if s == StateUnauthenticated {
		return Placement{VerdictRefuse, RefuseNotCompleted}
	}
	c := r.find(peer, addr)
	if s == StateMember || s == StateAuthorizedPerson {
		r.learned[peer] = true
		if c == nil {
			return Placement{VerdictAllow, PlaceUntracked}
		}
		r.drop(c)
		return Placement{VerdictAllow, PlaceReleased}
	}
	r.learned[peer], r.held[peer] = false, false
	switch {
	case c == nil:
		return Placement{VerdictAllow, PlaceUntracked}
	case c.nonMember:
		return Placement{VerdictAllow, PlaceAlreadyNonMember}
	case r.counts().NonMembers >= r.p.NonMemberConcurrency:
		r.drop(c)
		return Placement{VerdictRefuse, RefuseNonMemberFull}
	}
	c.nonMember, c.exempt = true, false
	return Placement{VerdictAllow, PlaceNonMember}
}

func (r *reference) disconnect(peer, addr int) {
	if c := r.find(peer, addr); c != nil {
		r.drop(c)
	}
}

func (r *reference) sweep(now time.Time) []*refConn {
	var out []*refConn
	for _, c := range r.conns {
		if !c.connected && now.Sub(c.securedAt) >= r.p.SweepInterval {
			out = append(out, c)
		}
	}
	for _, c := range out {
		r.drop(c)
	}
	return out
}

func (r *reference) overdue(now time.Time) []*refConn {
	var out []*refConn
	for _, c := range r.conns {
		if !c.connected {
			continue
		}
		limit := ExchangeDeadline
		if c.nonMember {
			limit = r.p.NonMemberLifetime
		}
		if now.Sub(c.connectedAt) >= limit {
			out = append(out, c)
		}
	}
	return out
}

func asKeys(cs []*refConn, keyOf func(peer, addr int) ConnKey) []ConnKey {
	var out []ConnKey
	for _, c := range cs {
		out = append(out, keyOf(c.peer, c.addr))
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

// TestAccountingAgainstTheReference drives the model and the reference with the
// same random sequence of the events the network layer reports: handshakes
// (inbound and outbound), connections established, completed exchanges (member,
// authorized person, non-member, and the unauthenticated answer of a refused
// exchange), disconnects, learning and forgetting members, sweeps and the
// closing of overdue connections. Time moves in seconds, like the mechanism: the
// exchange deadline is 5 s, the sweep interval 3 s and the lifetime 40 s. Every
// answer and the counts are compared at every step. The seed is fixed, and the
// test asserts that each path that matters was taken often enough to count.
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
	keyOf := func(peer, addr int) ConnKey { return ConnKey{Peer: pubs[peer], Addr: string(rune('a' + addr))} }

	cover := map[string]int{}
	now := t0
	states := []State{StateMember, StateAuthorizedPerson, StateNonMember, StateNonMember, StateNonMember, StateUnauthenticated}
	for step := 0; step < steps; step++ {
		now = now.Add(time.Duration(rng.Intn(3)) * time.Second) // 0, 1 or 2 seconds
		peer, addr := rng.Intn(peers), rng.Intn(addrs)
		ev := rng.Intn(100)
		if ev >= 28 && ev < 76 && len(ref.conns) > 0 && rng.Intn(4) != 0 {
			// Most events after the handshake concern a connection that exists.
			c := ref.conns[rng.Intn(len(ref.conns))]
			peer, addr = c.peer, c.addr
		}
		k := keyOf(peer, addr)
		switch {
		case ev < 28:
			inbound := rng.Intn(6) != 0
			dir := DirectionOutbound
			if inbound {
				dir = DirectionInbound
			}
			capBefore := ref.counts().Unauthenticated >= p.UnauthenticatedCap
			want := ref.secured(peer, addr, inbound, now)
			if got := a.Secured(k, dir, now); got != want {
				t.Fatalf("step %d: Secured(%d,%d) = %v/%v, want %v/%v", step, peer, addr, got.Verdict, got.Reason, want.Verdict, want.Reason)
			}
			cover["secured:"+want.Reason.String()]++
			if want.Reason == AdmitKnownMember && capBefore {
				cover["exempt-while-cap-reached"]++
			}
			if want.Reason == AdmitKnownMember && ref.held[peer] && !ref.learned[peer] {
				cover["exempt-by-held-certificate"]++
			}
		case ev < 50:
			want := ref.connect(peer, addr, now)
			if got := a.Connected(k, now); got != want {
				t.Fatalf("step %d: Connected = %v, want %v", step, got, want)
			}
		case ev < 66:
			s := states[rng.Intn(len(states))]
			want := ref.completed(peer, addr, s)
			if got := a.Completed(k, s, now); got != want {
				t.Fatalf("step %d: Completed(%v) = %v/%v, want %v/%v", step, s, got.Verdict, got.Reason, want.Verdict, want.Reason)
			}
			cover["completed:"+want.Reason.String()]++
		case ev < 76:
			ref.disconnect(peer, addr)
			a.Disconnected(k)
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
			want := asKeys(ref.sweep(now), keyOf)
			got := a.Sweep(now)
			if !sameKeySet(got, want) {
				t.Fatalf("step %d: Sweep = %v, want %v", step, got, want)
			}
			if len(want) > 0 {
				cover["sweep-removed-entries"] += len(want)
				cover["sweep-calls-with-removals"]++
			}
		default:
			wantRef := ref.overdue(now)
			want := asKeys(wantRef, keyOf)
			got := a.Overdue(now)
			if !sameKeySet(got, want) {
				t.Fatalf("step %d: Overdue = %v, want %v", step, got, want)
			}
			for _, c := range wantRef {
				if c.nonMember {
					cover["overdue-non-member"]++
				} else {
					cover["overdue-unauthenticated"]++
				}
			}
			// The network layer closes what is overdue, which ends the connection.
			for _, c := range wantRef {
				ref.disconnect(c.peer, c.addr)
				a.Disconnected(keyOf(c.peer, c.addr))
			}
		}
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
	}

	// The paths that matter were taken, many times. The thresholds are far below
	// what the fixed seed produces and well above chance.
	for name, min := range map[string]int{
		"secured:" + AdmitCounted.String():            2000,
		"secured:" + AdmitKnownMember.String():        500,
		"secured:" + RefuseCapReached.String():        300,
		"secured:" + AdmitOutbound.String():           1000,
		"secured:" + AdmitAlreadyTracked.String():     200,
		"exempt-while-cap-reached":                    100,
		"exempt-by-held-certificate":                  100,
		"completed:" + PlaceReleased.String():         200,
		"completed:" + PlaceNonMember.String():        200,
		"completed:" + RefuseNonMemberFull.String():   50,
		"completed:" + PlaceAlreadyNonMember.String(): 20,
		"completed:" + PlaceUntracked.String():        200,
		"completed:" + RefuseNotCompleted.String():    200,
		"sweep-calls-with-removals":                   200,
		"sweep-removed-entries":                       200,
		"overdue-unauthenticated":                     200,
		"overdue-non-member":                          100,
	} {
		if cover[name] < min {
			t.Errorf("path %q taken %d times, want at least %d", name, cover[name], min)
		}
	}
	if t.Failed() || testing.Verbose() {
		t.Logf("coverage: %v", cover)
	}
}
