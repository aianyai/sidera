package trust

import (
	"reflect"
	"slices"
	"testing"
	"time"
)

// A retired node is one whose identity key published its own revocation. Its
// identity is void for good, so it carries no trust in any role.
func TestEvaluateRetired(t *testing.T) {
	// A-B-C and A-D-C: C is reachable through B or through D.
	twoRoutes := func() *world {
		return newWorld(nA).edge(1, nA, nB).edge(2, nB, nC).edge(3, nA, nD).edge(4, nD, nC).acceptAll().vouchAll()
	}
	runEvalRows(t, []evalRow{
		{"a retired direct party is refused", func() *world { return newWorld(nA).edge(1, nA, nB).retire(nB) }, nB, time.Time{}, Denied, ReasonRetired, nil},
		{"a retired target is refused when it would be reached by inheritance", func() *world { return threeNodeChain().retire(nC) }, nC, time.Time{}, Denied, ReasonRetired, nil},
		{"a retired target is refused with a revoked credential too: retirement is the answer", func() *world { return newWorld(nA).edge(1, nA, nB).revokeCred(1).retire(nB) }, nB, time.Time{}, Denied, ReasonRetired, nil},
		{"a retired target is refused with an expired credential too", func() *world { return newWorld(nA).edge(1, nA, nB).expiring(1, t0).retire(nB) }, nB, time.Time{}, Denied, ReasonRetired, nil},
		{"retirement takes precedence over local revocation in the reason", func() *world { return newWorld(nA).edge(1, nA, nB).revokeNode(nB).retire(nB) }, nB, time.Time{}, Denied, ReasonRetired, nil},
		{"a retired node is never the intermediate of an inherited path", func() *world { return threeNodeChain().retire(nB) }, nC, time.Time{}, Denied, ReasonRetired, nil},
		{"a retired intermediate is refused, not suspended, when its vouchers are stale", func() *world {
			return threeNodeChain().retire(nB).vouch(nB, 1, time.Hour).vouch(nB, 2, time.Hour)
		}, nC, time.Time{}, Denied, ReasonRetired, nil},
		{"a retired intermediate is named as retired even when a credential of its path is also revoked", func() *world { return threeNodeChain().retire(nB).revokeCred(2) }, nC, time.Time{}, Denied, ReasonRetired, nil},
		{"a retired intermediate is named as retired even when a credential of its path expired", func() *world { return threeNodeChain().retire(nB).expiring(1, t0) }, nC, time.Time{}, Denied, ReasonRetired, nil},
		{"another route around a retired intermediate still works", func() *world { return twoRoutes().retire(nB) }, nC, time.Time{}, AllowedInherited, ReasonInherited, []byte{nD}},
		{"without the retirement both routes work", twoRoutes, nC, time.Time{}, AllowedInherited, ReasonInherited, []byte{nB, nD}},
		{"a failure that could still change is reported over a retired route", func() *world { return twoRoutes().retire(nB).revokeCred(4) }, nC, time.Time{}, Denied, ReasonCredentialRevoked, nil},
		{"the retired route loses to a suspended one", func() *world { return twoRoutes().retire(nB).vouch(nD, 4, time.Hour) }, nC, time.Time{}, Suspended, ReasonVoucherStale, []byte{nD}},
		{"a retired node leaves the trust of other nodes alone", func() *world { return threeNodeChain().retire(nX) }, nB, time.Time{}, AllowedDirect, ReasonDirect, []byte{0}},
		{"a retired node elsewhere leaves inherited trust alone", func() *world { return threeNodeChain().retire(nX) }, nC, time.Time{}, AllowedInherited, ReasonInherited, []byte{nB}},
		{"an empty retired set retires nobody", func() *world { w := threeNodeChain(); w.v.Retired = NodeSet{}; return w }, nC, time.Time{}, AllowedInherited, ReasonInherited, []byte{nB}},
		{"a nil retired set retires nobody", func() *world { w := threeNodeChain(); w.v.Retired = nil; return w }, nC, time.Time{}, AllowedInherited, ReasonInherited, []byte{nB}},
		{"the local node is still itself when it is the target", func() *world { return newWorld(nA).retire(nB) }, nA, time.Time{}, Denied, ReasonSelf, nil},
	})
}

func TestReasonRetiredName(t *testing.T) {
	if got := ReasonRetired.String(); got != "retired" {
		t.Fatalf("got %q", got)
	}
	for r := range reasonNames {
		if r != ReasonRetired && r.String() == ReasonRetired.String() {
			t.Fatalf("%v shares the name of ReasonRetired", r)
		}
	}
}

func TestVouchableEdgesRetired(t *testing.T) {
	both := func() *world { return newWorld(nA).edge(1, nA, nB).edge(2, nA, nC) }
	rows := []struct {
		rule  string
		world func() *world
		want  []CredentialDigest
	}{
		{"no voucher is issued for an edge whose other end is retired", func() *world { return both().retire(nB) }, digests(2)},
		{"no voucher is issued even while the credential is not revoked yet", func() *world { return both().retire(nC) }, digests(1)},
		{"every edge to a retired party is skipped, not just one", func() *world { return both().edge(3, nA, nB).retire(nB) }, digests(2)},
		{"a retired node that is not a party changes nothing", func() *world { return both().retire(nX) }, digests(1, 2)},
		{"an empty retired set changes nothing", func() *world { w := both(); w.v.Retired = NodeSet{}; return w }, digests(1, 2)},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := VouchableEdges(r.world().v, t0); !slices.Equal(got, r.want) {
				t.Fatalf("got %v, want %v", got, r.want)
			}
		})
	}
}

func TestIssuanceGateRetired(t *testing.T) {
	base := func() *world { return newWorld(nA).edge(1, nA, nB).edge(2, nA, nC) }
	starting := func() IssuanceState {
		return IssuanceState{Starting: true, Synced: NodeSet{}, Unreachable: NodeSet{}}
	}
	t.Run("a retired party is not waited for", func(t *testing.T) {
		got := IssuanceGate(base().retire(nB).v, starting(), t0)
		if got.Allowed || got.Reason != IssuanceAwaitingSync || !slices.Equal(got.WaitingFor, nodes(nC)) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("the gate opens once the remaining parties synced, however unreachable the retired one is", func(t *testing.T) {
		s := starting()
		s.Synced[node(nC)] = struct{}{}
		got := IssuanceGate(base().retire(nB).v, s, t0)
		if !got.Allowed || got.Reason != IssuanceOpen {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("pending directives still hold the gate when every party is retired", func(t *testing.T) {
		s := starting()
		s.PendingDirectives = 1
		got := IssuanceGate(base().retire(nB).retire(nC).v, s, t0)
		if got.Allowed || got.Reason != IssuanceAwaitingDirectives {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestEvictionProgressRetired(t *testing.T) {
	// A is local. X has parties A, B and C; D hangs off B. E (byte 7) is a
	// party of C only.
	world := func() *world {
		return newWorld(nA).edge(1, nA, nB).edge(2, nB, nX).edge(3, nA, nX).edge(4, nB, nD).edge(5, nA, nC).edge(6, nC, nX).edge(7, nC, 7).acceptAll().vouchAll()
	}
	t.Run("a retired target is complete with nothing outstanding or dependent", func(t *testing.T) {
		e := EvictionProgress(world().retire(nX).v, node(nX), t0)
		if !e.Complete || len(e.Unrevoked) != 0 || len(e.Dependent) != 0 {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("a retired party of the target is not waited for", func(t *testing.T) {
		e := EvictionProgress(world().retire(nB).v, node(nX), t0)
		var parties []byte
		for _, p := range e.Unrevoked {
			parties = append(parties, nodeByte(p.Party))
		}
		if e.Complete || !slices.Equal(parties, []byte{nA, nC}) {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("a target whose only path ran through a retired node is complete", func(t *testing.T) {
		// D is reachable from A only through B; B is retired.
		e := EvictionProgress(world().retire(nB).v, node(nD), t0)
		if !e.Complete || len(e.Unrevoked) != 0 || len(e.Dependent) != 0 {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("a target with a party besides the retired one stays incomplete and names that party", func(t *testing.T) {
		e := EvictionProgress(world().retire(nC).v, node(nX), t0)
		var parties []byte
		for _, p := range e.Unrevoked {
			parties = append(parties, nodeByte(p.Party))
		}
		if e.Complete || !slices.Equal(parties, []byte{nA, nB}) {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("dependent paths through or to a retired node are dropped", func(t *testing.T) {
		e := EvictionProgress(world().retire(nB).v, node(nX), t0)
		for _, p := range e.Dependent {
			if p.Via == node(nB) || p.To == node(nB) {
				t.Fatalf("path on a retired node: %+v", p)
			}
		}
		found := false
		for _, p := range e.Dependent {
			found = found || (p.Via == node(nC) && p.To == node(nX))
		}
		if !found {
			t.Fatalf("the path through C vanished: %+v", e.Dependent)
		}
	})
	t.Run("a retired node elsewhere changes nothing", func(t *testing.T) {
		if got, want := EvictionProgress(world().retire(7).v, node(nX), t0), EvictionProgress(world().v, node(nX), t0); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})
}

// Retirement leaves these answers as they are. Each row states why.
func TestRetiredLeavesOtherDecisionsUnchanged(t *testing.T) {
	t.Run("RevokeLocally still adds the node, stops vouching and revokes the credential: revoking is hygiene and harmless", func(t *testing.T) {
		w := threeNodeChain()
		want := mustRevoke(t, w.v, nB)
		got := mustRevoke(t, w.retire(nB).v, nB)
		if !reflect.DeepEqual(got, want) || len(got.RevokeCredentials) != 1 {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})
	t.Run("ApplyDirective still revokes an edge to a retired node: a directive only revokes, and the credential should not outlive its party", func(t *testing.T) {
		d := EdgeRevocationDirective{Credential: dig(1), A: node(nA), B: node(nB)}
		w := threeNodeChain()
		want := ApplyDirective(w.v, d)
		got := ApplyDirective(w.retire(nB).v, d)
		if !reflect.DeepEqual(got, want) || got.Disposition != DirectiveApply {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})
	t.Run("a directive for an edge between two other nodes is still not for this node", func(t *testing.T) {
		d := EdgeRevocationDirective{Credential: dig(2), A: node(nB), B: node(nC)}
		if got := ApplyDirective(threeNodeChain().retire(nB).v, d); got.Disposition != DirectiveNotForThisNode {
			t.Fatalf("got %+v", got)
		}
	})
}

// The local node's own entry in the retired set is never read: a retired local
// identity is refused by the caller, and every decision here is about the other
// nodes. With the entry present each function answers exactly as without it.
func TestRetiredLocalNodeIsNeverRead(t *testing.T) {
	// A is local with direct parties B, X and an expired one (Y, byte 8); C
	// hangs off B and D off X, so A reaches C and D by inheritance.
	build := func() *world {
		return newWorld(nA).edge(1, nA, nB).edge(2, nB, nC).edge(3, nA, nX).edge(4, nX, nD).edge(5, nA, 8).expiring(5, t0.Add(-time.Minute)).acceptAll().vouchAll()
	}
	// The second base retires another node as well, so the rows also show that
	// the local entry does not disturb a view that does retire someone.
	bases := []struct {
		name string
		base func() *world
	}{
		{"nobody else retired", build},
		{"another node retired", func() *world { return build().retire(nC) }},
	}
	starting := IssuanceState{Starting: true, Synced: NodeSet{}, Unreachable: NodeSet{}}
	for _, b := range bases {
		t.Run(b.name, func(t *testing.T) {
			plain, withLocal := b.base().v, b.base().retire(nA).v
			if _, ok := withLocal.Retired[node(nA)]; !ok {
				t.Fatal("the local node is not in the retired set")
			}
			for _, target := range []byte{nA, nB, nC, nD, nX, 8, 7} {
				if got, want := Evaluate(withLocal, node(target), t0), Evaluate(plain, node(target), t0); !reflect.DeepEqual(got, want) {
					t.Errorf("Evaluate %d: got %+v, want %+v", target, got, want)
				}
				// The local node as the target and as a party (X and B hold a
				// credential with it) are both in this loop.
				if got, want := EvictionProgress(withLocal, node(target), t0), EvictionProgress(plain, node(target), t0); !reflect.DeepEqual(got, want) {
					t.Errorf("EvictionProgress %d: got %+v, want %+v", target, got, want)
				}
				if got, want := ClearLocallyRevoked(withLocal, node(target), t0), ClearLocallyRevoked(plain, node(target), t0); got != want {
					t.Errorf("ClearLocallyRevoked %d: got %+v, want %+v", target, got, want)
				}
				if got, want := NewEdgeAudience(withLocal, node(nA), node(target), t0), NewEdgeAudience(plain, node(nA), node(target), t0); !slices.Equal(got, want) {
					t.Errorf("NewEdgeAudience %d: got %v, want %v", target, got, want)
				}
			}
			if got, want := VouchableEdges(withLocal, t0), VouchableEdges(plain, t0); !slices.Equal(got, want) {
				t.Errorf("VouchableEdges: got %v, want %v", got, want)
			}
			if got, want := IssuanceGate(withLocal, starting, t0), IssuanceGate(plain, starting, t0); !reflect.DeepEqual(got, want) {
				t.Errorf("IssuanceGate: got %+v, want %+v", got, want)
			}
		})
	}
	// Expected values written out, so the rows above cannot agree on a wrong
	// answer: the local node keeps deciding as before.
	t.Run("the local node still trusts its direct party and reaches the inherited one", func(t *testing.T) {
		v := build().retire(nA).v
		if got := Evaluate(v, node(nB), t0); got.Outcome != AllowedDirect {
			t.Fatalf("direct party: %+v", got)
		}
		if got := Evaluate(v, node(nC), t0); got.Outcome != AllowedInherited {
			t.Fatalf("inherited: %+v", got)
		}
	})
	t.Run("the local node is a party X still waits for, and the target of its own eviction", func(t *testing.T) {
		v := build().retire(nA).v
		var parties []byte
		for _, p := range EvictionProgress(v, node(nX), t0).Unrevoked {
			parties = append(parties, nodeByte(p.Party))
		}
		if !slices.Equal(parties, []byte{nA, nD}) {
			t.Fatalf("parties of X: %v", parties)
		}
		if e := EvictionProgress(v, node(nA), t0); e.Complete || len(e.Unrevoked) != 2 {
			t.Fatalf("eviction of the local node: %+v", e)
		}
	})
	t.Run("vouchers are still issued and the gate still waits", func(t *testing.T) {
		v := build().retire(nA).v
		if got := VouchableEdges(v, t0); !slices.Equal(got, digests(1, 3)) {
			t.Fatalf("vouchable: %v", got)
		}
		if got := IssuanceGate(v, starting, t0); got.Allowed || !slices.Equal(got.WaitingFor, nodes(nB, nX)) {
			t.Fatalf("gate: %+v", got)
		}
	})
}

// Clearing the entry of a retired node restores nothing and a new pairing does
// not help: the identity is void, so only a new node ID can join.
func TestClearLocallyRevokedRetired(t *testing.T) {
	rows := []struct {
		rule  string
		world func() *world
		want  ClearEffect
	}{
		{"a retired node with a usable credential is reported retired", func() *world { return newWorld(nA).edge(1, nA, nB).revokeNode(nB).retire(nB) }, ClearEffect{WasRevoked: true, Retired: true}},
		{"a retired node with a revoked credential needs no new pairing: pairing again would not bring it back", func() *world { return newWorld(nA).edge(1, nA, nB).revokeCred(1).revokeNode(nB).retire(nB) }, ClearEffect{WasRevoked: true, Retired: true}},
		{"a retired node with an expired credential needs no new pairing", func() *world { return newWorld(nA).edge(1, nA, nB).expiring(1, t0).revokeNode(nB).retire(nB) }, ClearEffect{WasRevoked: true, Retired: true}},
		{"a retired node that was never revoked locally is still reported retired", func() *world { return newWorld(nA).edge(1, nA, nB).revokeCred(1).retire(nB) }, ClearEffect{Retired: true}},
		{"a retired node without any credential is reported retired", func() *world { return newWorld(nA).revokeNode(nB).retire(nB) }, ClearEffect{WasRevoked: true, Retired: true}},
		{"a node that is not retired still needs a new pairing after its credential was revoked", func() *world { return newWorld(nA).edge(1, nA, nB).revokeCred(1).revokeNode(nB).retire(nC) }, ClearEffect{WasRevoked: true, NewPairingRequired: true}},
		{"a node that is not retired with a usable credential needs nothing", func() *world { return newWorld(nA).edge(1, nA, nB).revokeNode(nB).retire(nC) }, ClearEffect{WasRevoked: true}},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := ClearLocallyRevoked(r.world().v, node(nB), t0); got != r.want {
				t.Fatalf("got %+v, want %+v", got, r.want)
			}
		})
	}
}

// A retired node is not told about a new edge, but the other parties of a
// retired end still are: an edge with a retired end means a stolen identity key
// is signing new credentials, which they must hear.
func TestNewEdgeAudienceRetired(t *testing.T) {
	// The new edge is B-D. B has parties A and C, D has parties C and E (byte 7).
	base := func() *world {
		return newWorld(nA).edge(1, nA, nB).edge(2, nB, nC).edge(3, nC, nD).edge(4, nD, 7)
	}
	rows := []struct {
		rule  string
		world func() *world
		want  []byte
	}{
		{"without retirement everyone around the edge is told", base, []byte{nA, nB, nC, nD, 7}},
		{"a retired end b is absent and the other parties of b are present", func() *world { return base().retire(nD) }, []byte{nA, nB, nC, 7}},
		{"a retired end a is absent and the other parties of a are present", func() *world { return base().retire(nB) }, []byte{nA, nC, nD, 7}},
		{"with both ends retired their parties are still told", func() *world { return base().retire(nB).retire(nD) }, []byte{nA, nC, 7}},
		{"a retired party of an end is absent", func() *world { return base().retire(nC) }, []byte{nA, nB, nD, 7}},
		{"a retired node elsewhere changes nothing", func() *world { return base().retire(nX) }, []byte{nA, nB, nC, nD, 7}},
		{"a retired end with a revoked credential tells that party nothing: only usable credentials count", func() *world { return base().revokeCred(1).retire(nB) }, []byte{nC, nD, 7}},
		{"an empty retired set changes nothing", func() *world { w := base(); w.v.Retired = NodeSet{}; return w }, []byte{nA, nB, nC, nD, 7}},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := NewEdgeAudience(r.world().v, node(nB), node(nD), t0); !slices.Equal(got, nodes(r.want...)) {
				t.Fatalf("got %v, want %v", got, nodes(r.want...))
			}
		})
	}
}
