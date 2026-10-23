package trust

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// model holds the raw records of a random scenario, independently of View, so
// that an expected verdict can be recomputed from scratch.
type model struct {
	universe  []byte
	creds     []Credential
	revoked   map[CredentialDigest]bool
	locRev    map[ident.NodeID]bool
	declared  map[ident.NodeID]bool
	vouchers  []Voucher // all vouchers ever offered, unique counter per slot
	accepts   bool
	grace     time.Duration
	now       time.Time
	localByte byte
}

func genModel(r *rand.Rand) model {
	m := model{
		revoked:  map[CredentialDigest]bool{},
		locRev:   map[ident.NodeID]bool{},
		declared: map[ident.NodeID]bool{},
		accepts:  r.IntN(5) != 0,
		grace:    MinGracePeriod + time.Duration(r.IntN(10))*time.Minute,
	}
	m.now = t0.Add(time.Duration(r.IntN(16)) * time.Minute)
	size := 3 + r.IntN(5)
	for i := 1; i <= size; i++ {
		m.universe = append(m.universe, byte(i))
		m.declared[node(byte(i))] = r.IntN(6) != 0
	}
	m.localByte = m.universe[r.IntN(size)]
	d := byte(0)
	for _, a := range m.universe {
		for _, b := range m.universe {
			if a < b && r.IntN(100) < 40 {
				d++
				c := Credential{Digest: dig(d), A: node(a), B: node(b)}
				switch r.IntN(8) {
				case 0:
					c.NotAfter = t0.Add(-time.Minute)
				case 1:
					c.NotAfter = t0.Add(time.Hour)
				}
				m.creds = append(m.creds, c)
				if r.IntN(8) == 0 {
					m.revoked[c.Digest] = true
				}
				for _, issuer := range []byte{a, b} {
					for k, versions := uint64(1), r.IntN(3); int(k) <= versions; k++ {
						age := time.Duration(r.IntN(14)-3) * time.Minute
						m.vouchers = append(m.vouchers, Voucher{Issuer: node(issuer), Edge: c.Digest, IssuedAt: t0.Add(-age), Counter: k})
					}
				}
			}
		}
	}
	for _, n := range m.universe {
		if r.IntN(20) == 0 {
			m.locRev[node(n)] = true
		}
	}
	return m
}

// event is one record becoming known to a node.
type event func(*View)

func (m model) events() []event {
	var evs []event
	for _, c := range m.creds {
		evs = append(evs, func(v *View) { v.Credentials = append(v.Credentials, c) })
	}
	for d := range m.revoked {
		evs = append(evs, func(v *View) { v.RevokedCredentials[d] = struct{}{} })
	}
	for n := range m.locRev {
		evs = append(evs, func(v *View) { v.LocallyRevoked[n] = struct{}{} })
	}
	for n, b := range m.declared {
		evs = append(evs, func(v *View) { v.DeclaredAcceptsInheritance[n] = b })
	}
	for _, vo := range m.vouchers {
		evs = append(evs, func(v *View) { v.Vouchers.Accept(vo, t0) })
	}
	return evs
}

func (m model) emptyView(local byte) View {
	return newWorld(local).withSettings(Settings{AcceptsInheritance: m.accepts, GracePeriod: m.grace}).v
}

func (w *world) withSettings(s Settings) *world { w.v.Settings = s; return w }

func (m model) build(local byte, evs []event) View {
	v := m.emptyView(local)
	for _, e := range evs {
		e(&v)
	}
	return v
}

func (m model) credOK(c Credential) bool {
	return !m.revoked[c.Digest] && (c.NotAfter.IsZero() || m.now.Before(c.NotAfter))
}

func linked(c Credential, x, y ident.NodeID) bool {
	return (c.A == x && c.B == y) || (c.A == y && c.B == x)
}

// voucherOK judges the latest voucher of the slot: the one with the highest
// counter among those not stamped beyond the tolerance ahead of t0, the moment
// they all reach the node. The rest are rejected on receipt and never kept.
func (m model) voucherOK(issuer ident.NodeID, edge CredentialDigest) bool {
	var best *Voucher
	for i, vo := range m.vouchers {
		if vo.IssuedAt.After(t0.Add(ClockTolerance)) {
			continue
		}
		if vo.Issuer == issuer && vo.Edge == edge && (best == nil || vo.Counter > best.Counter) {
			best = &m.vouchers[i]
		}
	}
	return best != nil && !best.IssuedAt.After(m.now.Add(ClockTolerance)) && m.now.Sub(best.IssuedAt) <= m.grace
}

// expected recomputes the verdict outcome and the paths from the raw records.
func (m model) expected(local, target ident.NodeID, accepts bool) (Outcome, []string) {
	if local == target || m.locRev[target] {
		return Denied, nil
	}
	var direct []string
	for _, c := range m.creds {
		if linked(c, local, target) && m.credOK(c) {
			direct = append(direct, fmt.Sprint("direct:", c.Digest))
		}
	}
	if len(direct) > 0 {
		slices.Sort(direct)
		return AllowedDirect, direct
	}
	var good, pending []string
	for _, mb := range m.universe {
		via := node(mb)
		if via == local || via == target || m.locRev[via] || !accepts || !m.declared[target] {
			continue
		}
		for _, c1 := range m.creds {
			for _, c2 := range m.creds {
				if !linked(c1, local, via) || !linked(c2, via, target) || !m.credOK(c1) || !m.credOK(c2) {
					continue
				}
				p := fmt.Sprint(mb, ":", c1.Digest, c2.Digest)
				if m.voucherOK(via, c1.Digest) && m.voucherOK(via, c2.Digest) {
					good = append(good, p)
				} else {
					pending = append(pending, p)
				}
			}
		}
	}
	slices.Sort(good)
	slices.Sort(pending)
	switch {
	case len(good) > 0:
		return AllowedInherited, good
	case len(pending) > 0:
		return Suspended, pending
	}
	return Denied, nil
}

func pathStrings(v Verdict) []string {
	var out []string
	for _, p := range v.Paths {
		if len(p.Credentials) == 1 {
			out = append(out, fmt.Sprint("direct:", p.Credentials[0]))
		} else {
			out = append(out, fmt.Sprint(nodeByte(p.Via), ":", p.Credentials[0], p.Credentials[1]))
		}
	}
	slices.Sort(out)
	return out
}

const propertyRuns = 1000

// Whatever order the records arrive in, the derived verdicts equal a
// recomputation from the raw records, and no path exceeds two edges.
func TestPropertyVerdictsMatchRecomputation(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for run := 0; run < propertyRuns; run++ {
		m := genModel(r)
		evs := m.events()
		canonical := m.build(m.localByte, evs)
		shuffled := slices.Clone(evs)
		r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		other := m.build(m.localByte, shuffled)

		for _, tb := range m.universe {
			target := node(tb)
			a, b := Evaluate(canonical, target, m.now), Evaluate(other, target, m.now)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("run %d target %d: order changed the verdict: %+v vs %+v", run, tb, a, b)
			}
			wantOutcome, wantPaths := m.expected(node(m.localByte), target, m.accepts)
			if a.Outcome != wantOutcome || !slices.Equal(pathStrings(a), wantPaths) {
				t.Fatalf("run %d target %d: got %v %v, want %v %v", run, tb, a.Outcome, pathStrings(a), wantOutcome, wantPaths)
			}
			for _, p := range a.Paths {
				if len(p.Credentials) < 1 || len(p.Credentials) > 2 {
					t.Fatalf("run %d: path with %d credentials", run, len(p.Credentials))
				}
			}
			if (a.Outcome == Denied) != (len(a.Paths) == 0) {
				t.Fatalf("run %d target %d: %v with %d paths", run, tb, a.Outcome, len(a.Paths))
			}
		}
	}
}

// Revoking a credential or a node can only take trust away.
func TestPropertyRevocationNeverGrantsTrust(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for run := 0; run < propertyRuns; run++ {
		m := genModel(r)
		before := m.build(m.localByte, m.events())
		after := m.build(m.localByte, m.events())
		if len(m.creds) > 0 {
			after.RevokedCredentials[m.creds[r.IntN(len(m.creds))].Digest] = struct{}{}
		}
		after.LocallyRevoked[node(m.universe[r.IntN(len(m.universe))])] = struct{}{}
		// A directive revokes one more edge of the local node.
		for _, c := range after.credentialsOf(after.Local) {
			if r.IntN(2) == 0 {
				for _, d := range ApplyDirective(after, EdgeRevocationDirective{Credential: c.Digest, A: c.A, B: c.B}).Effects.RevokeCredentials {
					after.RevokedCredentials[d] = struct{}{}
				}
			}
		}
		for _, tb := range m.universe {
			b, a := Evaluate(before, node(tb), m.now), Evaluate(after, node(tb), m.now)
			if a.Outcome.Allowed() && !b.Outcome.Allowed() {
				t.Fatalf("run %d target %d: revocation turned %v into %v", run, tb, b.Outcome, a.Outcome)
			}
			if a.Outcome == Suspended && b.Outcome == Denied {
				t.Fatalf("run %d target %d: revocation turned denied into suspended", run, tb)
			}
		}
	}
}

// When two nodes see the same records and no local revocations, each one's
// verdict about the other agrees, including when the two inheritance switches
// differ.
func TestPropertyTwoSidesAgree(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for run := 0; run < propertyRuns; run++ {
		m := genModel(r)
		m.locRev = map[ident.NodeID]bool{}
		evs := m.events()
		for _, ab := range m.universe {
			for _, cb := range m.universe {
				if ab >= cb {
					continue
				}
				a, c := node(ab), node(cb)
				va, vc := m.build(ab, evs), m.build(cb, evs)
				va.Settings.AcceptsInheritance = m.declared[a]
				vc.Settings.AcceptsInheritance = m.declared[c]
				ac, ca := Evaluate(va, c, m.now), Evaluate(vc, a, m.now)
				if ac.Outcome != ca.Outcome {
					t.Fatalf("run %d: %d about %d is %v but %d about %d is %v", run, ab, cb, ac.Outcome, cb, ab, ca.Outcome)
				}
				if got, want := viaSet(ac), viaSet(ca); !slices.Equal(got, want) {
					t.Fatalf("run %d: %d and %d disagree on intermediates: %v vs %v", run, ab, cb, got, want)
				}
			}
		}
	}
}

func viaSet(v Verdict) []byte {
	out := viaOf(v.Paths)
	slices.Sort(out)
	return out
}
