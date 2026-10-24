package publication

import (
	"math/rand"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// sealedKey identifies one sealed record the decision tests ask for.
type sealedKey struct {
	reachability bool
	idSeed       int
	counter      uint64
	variant      byte
	expiresAt    time.Time
}

// sealedRecords caches the verified records of the decision tests: the
// randomized test asks for the same record many times, and sealing each time
// would only repeat the signatures.
var sealedRecords = map[sealedKey]any{}

// networkSeedOf is the seed of the network key of the node whose identity key
// has seed idSeed, for the nodes the decision tests make up.
func networkSeedOf(idSeed int) int { return idSeed + 0x80 }

// acceptedFor is the certificate a verifier holds for the node whose identity
// key has seed idSeed: the node certifies network key networkSeedOf(idSeed) for
// 30 days from t0.
func acceptedFor(t testing.TB, idSeed int) record.AcceptedNetworkKey {
	t.Helper()
	cert, err := record.Verify(record.NetworkKeyCertificateDescriptor, networkCertRecord(t, idSeed, networkSeedOf(idSeed), 1, t0, t0.Add(30*24*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	c := cert.Payload()
	return record.AcceptedNetworkKey{Cert: &c, Revocation: record.KeyRevocationClear}
}

// verifiedDeclaration seals a declaration of the node with identity key idSeed
// and verifies it. Two declarations with the same counter and variant are the
// same record; another variant is another content (a different boot nonce).
func verifiedDeclaration(t testing.TB, idSeed int, counter uint64, variant byte) record.Verified[Declaration] {
	t.Helper()
	k := sealedKey{idSeed: idSeed, counter: counter, variant: variant}
	if v, ok := sealedRecords[k]; ok {
		return v.(record.Verified[Declaration])
	}
	d := sampleDeclaration(t)
	d.Node, d.Counter, d.BootNonce = nodeOf(t, idSeed), counter, bootNonce(variant)
	desc := DeclarationDescriptor(acceptedFor(t, idSeed), declNow)
	r, err := record.Seal(desc, d, signerOf(t, networkSeedOf(idSeed)))
	if err != nil {
		t.Fatal(err)
	}
	v, err := record.Verify(desc, r)
	if err != nil {
		t.Fatal(err)
	}
	sealedRecords[k] = v
	return v
}

// verifiedReachability seals a reachability record of the node with identity
// key idSeed and verifies it. The variant stands for the content (one address).
func verifiedReachability(t testing.TB, idSeed int, counter uint64, variant byte, expires time.Time) record.Verified[ReachabilityRecord] {
	t.Helper()
	k := sealedKey{reachability: true, idSeed: idSeed, counter: counter, variant: variant, expiresAt: expires}
	if v, ok := sealedRecords[k]; ok {
		return v.(record.Verified[ReachabilityRecord])
	}
	cert := networkCertRecord(t, idSeed, networkSeedOf(idSeed), 1, t0, t0.Add(30*24*time.Hour))
	p, err := NewReachabilityRecord(counter, expires, [][]byte{{variant}}, nil, cert)
	if err != nil {
		t.Fatal(err)
	}
	desc := ReachabilityDescriptor(acceptedFor(t, idSeed), declNow)
	r, err := record.Seal(desc, p, signerOf(t, networkSeedOf(idSeed)))
	if err != nil {
		t.Fatal(err)
	}
	v, err := record.Verify(desc, r)
	if err != nil {
		t.Fatal(err)
	}
	sealedRecords[k] = v
	return v
}

func TestDecideDeclaration(t *testing.T) {
	a, b := keyA, keyOther
	seen5 := DeclarationSeenOf(verifiedDeclaration(t, a, 5, 1))
	tests := []struct {
		name     string
		seen     *DeclarationSeen
		incoming record.Verified[Declaration]
		want     Verdict
	}{
		{"nothing seen yet", nil, verifiedDeclaration(t, a, 1, 1), VerdictAccept},
		{"higher counter", &seen5, verifiedDeclaration(t, a, 6, 1), VerdictAccept},
		{"counter far higher", &seen5, verifiedDeclaration(t, a, 1<<40, 1), VerdictAccept},
		{"lower counter", &seen5, verifiedDeclaration(t, a, 4, 1), VerdictStale},
		{"lowest counter", &seen5, verifiedDeclaration(t, a, 1, 2), VerdictStale},
		{"the very declaration seen", &seen5, verifiedDeclaration(t, a, 5, 1), VerdictStale},
		{"same counter, different content", &seen5, verifiedDeclaration(t, a, 5, 2), VerdictConflict},
		{"a declaration of another node", &seen5, verifiedDeclaration(t, b, 9, 1), VerdictWrongNode},
		{"zero value", nil, record.Verified[Declaration]{}, VerdictInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DecideDeclaration(tc.seen, tc.incoming); got != tc.want {
				t.Errorf("verdict = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDecideReachability(t *testing.T) {
	a, b := keyA, keyOther
	now := t0.Add(10 * time.Minute)
	live := now.Add(time.Hour)
	seen5 := ReachabilitySeenOf(verifiedReachability(t, a, 5, 1, live))
	tests := []struct {
		name     string
		seen     *ReachabilitySeen
		incoming record.Verified[ReachabilityRecord]
		want     Verdict
	}{
		{"nothing seen yet", nil, verifiedReachability(t, a, 1, 1, live), VerdictAccept},
		{"higher counter", &seen5, verifiedReachability(t, a, 6, 1, live), VerdictAccept},
		{"lower counter", &seen5, verifiedReachability(t, a, 4, 1, live), VerdictStale},
		{"the very record seen", &seen5, verifiedReachability(t, a, 5, 1, live), VerdictStale},
		{"same counter, different content", &seen5, verifiedReachability(t, a, 5, 2, live), VerdictConflict},
		{"another node", &seen5, verifiedReachability(t, b, 9, 1, live), VerdictWrongNode},
		{"zero value", nil, record.Verified[ReachabilityRecord]{}, VerdictInvalid},
		{"higher counter but expired", &seen5, verifiedReachability(t, a, 6, 1, now.Add(-time.Second)), VerdictExpired},
		{"higher counter expiring at this instant", &seen5, verifiedReachability(t, a, 6, 1, now), VerdictExpired},
		{"first record already expired", nil, verifiedReachability(t, a, 1, 1, now.Add(-time.Hour)), VerdictExpired},
		{"higher counter one second before expiry", &seen5, verifiedReachability(t, a, 6, 1, now.Add(time.Second)), VerdictAccept},
		{"stale beats expired", &seen5, verifiedReachability(t, a, 4, 1, now.Add(-time.Hour)), VerdictStale},
		{"conflict beats expired", &seen5, verifiedReachability(t, a, 5, 2, now.Add(-time.Hour)), VerdictConflict},
		{"wrong node beats expired", &seen5, verifiedReachability(t, b, 9, 1, now.Add(-time.Hour)), VerdictWrongNode},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DecideReachability(tc.seen, tc.incoming, now); got != tc.want {
				t.Errorf("verdict = %v, want %v", got, tc.want)
			}
		})
	}
}

// arrival is one record reaching a node: which node it is about, its counter,
// and a variant that stands for its content (two arrivals with the same counter
// and variant are the same record).
type arrival struct {
	node    int
	counter uint64
	variant byte
	expired bool
}

// oracle replays arrivals in order the way the rule is stated in prose, with its
// own bookkeeping: a node's records accepted so far are kept in a list, and an
// arrival is judged against the best of them.
type oracle struct {
	accepted map[int][]arrival
}

func (o *oracle) judge(a arrival, expiryApplies bool) Verdict {
	best := uint64(0)
	var bestVariant byte
	for _, x := range o.accepted[a.node] {
		if x.counter > best {
			best, bestVariant = x.counter, x.variant
		}
	}
	switch {
	case best == 0 || a.counter > best:
		if expiryApplies && a.expired {
			return VerdictExpired
		}
		o.accepted[a.node] = append(o.accepted[a.node], a)
		return VerdictAccept
	case a.counter < best:
		return VerdictStale
	case a.variant == bestVariant:
		return VerdictStale
	}
	return VerdictConflict
}

// TestHighestCounterUnderRandomArrivalOrders feeds the same set of records in
// many random orders to the decision functions and checks every verdict against
// the oracle, and that the state at the end is the highest counter whatever the
// order was.
func TestHighestCounterUnderRandomArrivalOrders(t *testing.T) {
	rng := rand.New(rand.NewSource(20231023))
	nodes := []int{keyA, keyOther, keyRoot}
	now := t0.Add(time.Hour)
	counts := map[Verdict]int{}

	for trial := 0; trial < 300; trial++ {
		// A set of distinct records over three nodes, counters 1..6, one or two
		// variants per counter, some already expired; then duplicated arrivals.
		var set []arrival
		for n := range nodes {
			for c := uint64(1); c <= 6; c++ {
				if rng.Intn(3) == 0 {
					continue
				}
				for v := byte(1); v <= byte(1+rng.Intn(2)); v++ {
					set = append(set, arrival{node: n, counter: c, variant: v, expired: rng.Intn(4) == 0})
				}
			}
		}
		arrivals := append([]arrival(nil), set...)
		for i := 0; i < len(set)/3; i++ {
			arrivals = append(arrivals, set[rng.Intn(len(set))])
		}
		rng.Shuffle(len(arrivals), func(i, j int) { arrivals[i], arrivals[j] = arrivals[j], arrivals[i] })

		for _, kind := range []string{"declaration", "reachability"} {
			o := &oracle{accepted: map[int][]arrival{}}
			declSeen := map[int]*DeclarationSeen{}
			reachSeen := map[int]*ReachabilitySeen{}
			for _, a := range arrivals {
				var got Verdict
				var accepted func()
				node := nodes[a.node]
				// An expiry time is part of the content, so it is fixed by the
				// variant of a record, not drawn again for each arrival.
				if kind == "declaration" {
					v := verifiedDeclaration(t, node, a.counter, a.variant)
					got = DecideDeclaration(declSeen[a.node], v)
					accepted = func() { s := DeclarationSeenOf(v); declSeen[a.node] = &s }
				} else {
					exp := now.Add(time.Hour)
					if a.expired {
						exp = now.Add(-time.Hour)
					}
					v := verifiedReachability(t, node, a.counter, a.variant, exp)
					got = DecideReachability(reachSeen[a.node], v, now)
					accepted = func() { s := ReachabilitySeenOf(v); reachSeen[a.node] = &s }
				}
				want := o.judge(a, kind == "reachability")
				if got != want {
					t.Fatalf("trial %d %s: %+v: verdict %v, oracle %v", trial, kind, a, got, want)
				}
				counts[got]++
				if got == VerdictAccept {
					accepted()
				}
			}
			// The state at the end is the highest counter accepted, whatever the order.
			for n := range nodes {
				best := uint64(0)
				for _, x := range o.accepted[n] {
					if x.counter > best {
						best = x.counter
					}
				}
				var have uint64
				if kind == "declaration" && declSeen[n] != nil {
					have = declSeen[n].Counter
				}
				if kind == "reachability" && reachSeen[n] != nil {
					have = reachSeen[n].Counter
				}
				if have != best {
					t.Fatalf("trial %d %s node %d: state %d, oracle %d", trial, kind, n, have, best)
				}
				if kind == "declaration" {
					max := uint64(0)
					for _, x := range set {
						if x.node == n && x.counter > max {
							max = x.counter
						}
					}
					if have != max {
						t.Fatalf("trial %d: declaration state %d is not the highest counter %d offered", trial, have, max)
					}
				}
			}
		}
	}
	for _, v := range []Verdict{VerdictAccept, VerdictStale, VerdictConflict, VerdictExpired} {
		if counts[v] < 500 {
			t.Errorf("verdict %v happened only %d times", v, counts[v])
		}
	}
}

// TestDeclarationInheritanceThroughTrust: a verified declaration's switch is the
// entry of View.DeclaredAcceptsInheritance, and only the latest declaration's
// switch counts.
func TestDeclarationInheritanceThroughTrust(t *testing.T) {
	local, m := nodeOf(t, keyRoot), nodeOf(t, keyOther)
	target := nodeOf(t, keyA)
	d1 := sampleDeclaration(t)
	d1.AcceptsInheritance = true
	d2 := d1
	d2.Counter = 6
	d2.AcceptsInheritance = false
	desc := DeclarationDescriptor(acceptedA(t), declNow)
	verified := func(d Declaration) record.Verified[Declaration] {
		r, err := record.Seal(desc, d, signerOf(t, keyNetA))
		if err != nil {
			t.Fatal(err)
		}
		v, err := record.Verify(desc, mustDecode(t, mustEncode(t, r)))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	v1, v2 := verified(d1), verified(d2)

	credLM := trust.Credential{Digest: trust.CredentialDigest{1}, A: local, B: m}
	credMT := trust.Credential{Digest: trust.CredentialDigest{2}, A: m, B: target}
	now := declNow
	vouchers := trust.Vouchers{}
	for _, e := range []trust.CredentialDigest{credLM.Digest, credMT.Digest} {
		vouchers.Accept(trust.Voucher{Issuer: m, Edge: e, IssuedAt: now.Add(-time.Minute), Counter: 1}, now)
	}
	view := func(fact map[ident.NodeID]bool) trust.View {
		return trust.View{
			Local: local, Settings: trust.DefaultSettings(), Credentials: []trust.Credential{credLM, credMT},
			Vouchers: vouchers, DeclaredAcceptsInheritance: fact,
		}
	}

	// No declaration known: inheritance is not assumed.
	if got := trust.Evaluate(view(nil), target, now); got.Outcome != trust.Denied || got.Reason != trust.ReasonDeclarationUnknown {
		t.Fatalf("no declaration: %v %v", got.Outcome, got.Reason)
	}
	seen := (*DeclarationSeen)(nil)
	facts := map[ident.NodeID]bool{}
	apply := func(v record.Verified[Declaration]) Verdict {
		verdict := DecideDeclaration(seen, v)
		if verdict == VerdictAccept {
			s := DeclarationSeenOf(v)
			seen = &s
			n, on := v.Payload().DeclaredInheritance()
			facts[n] = on
		}
		return verdict
	}
	if apply(v1) != VerdictAccept {
		t.Fatal("first declaration refused")
	}
	if got := trust.Evaluate(view(facts), target, now); got.Outcome != trust.AllowedInherited {
		t.Fatalf("switch on: %v %v", got.Outcome, got.Reason)
	}
	if apply(v2) != VerdictAccept {
		t.Fatal("newer declaration refused")
	}
	if got := trust.Evaluate(view(facts), target, now); got.Outcome != trust.Denied || got.Reason != trust.ReasonInheritanceDisabled {
		t.Fatalf("switch off: %v %v", got.Outcome, got.Reason)
	}
	// The older declaration arriving late does not turn the switch back on.
	if apply(v1) != VerdictStale {
		t.Fatal("older declaration not stale")
	}
	if got := trust.Evaluate(view(facts), target, now); got.Outcome != trust.Denied {
		t.Fatalf("after the stale declaration: %v %v", got.Outcome, got.Reason)
	}
}
