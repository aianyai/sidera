package trust

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/timing"
)

func TestValidateGracePeriod(t *testing.T) {
	if MinGracePeriod != 6*time.Minute {
		t.Fatalf("floor is %v, want issue period + push budget + clock tolerance = 6m", MinGracePeriod)
	}
	rows := []struct {
		rule string
		d    time.Duration
		err  error
	}{
		{"the default is accepted", DefaultGracePeriod, nil},
		{"the floor itself is accepted", MinGracePeriod, nil},
		{"one nanosecond below the floor is rejected", MinGracePeriod - time.Nanosecond, ErrGracePeriodTooShort},
		{"zero is rejected", 0, ErrGracePeriodTooShort},
		{"a negative value is rejected", -time.Minute, ErrGracePeriodTooShort},
		{"a long period is accepted", 24 * time.Hour, nil},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if err := ValidateGracePeriod(r.d); !errors.Is(err, r.err) {
				t.Fatalf("got %v, want %v", err, r.err)
			}
		})
	}
	if err := ValidateGracePeriod(DefaultSettings().GracePeriod); err != nil {
		t.Fatalf("default settings are invalid: %v", err)
	}
}

func TestVoucherAccept(t *testing.T) {
	v := func(counter uint64, at time.Duration) Voucher {
		return Voucher{Issuer: node(nB), Edge: dig(1), IssuedAt: t0.Add(at), Counter: counter}
	}
	rows := []struct {
		rule     string
		stored   []Voucher
		incoming Voucher
		accepted bool
		reason   VoucherReason
		keptCtr  uint64
	}{
		{"the first voucher for a slot is accepted", nil, v(1, 0), true, VoucherAccepted, 1},
		{"a greater counter replaces the stored voucher", []Voucher{v(5, 0)}, v(6, time.Minute), true, VoucherAccepted, 6},
		{"a counter that jumps ahead is accepted", []Voucher{v(5, 0)}, v(500, time.Minute), true, VoucherAccepted, 500},
		{"an equal counter is rejected", []Voucher{v(5, 0)}, v(5, time.Minute), false, VoucherCounterNotAdvancing, 5},
		{"a regressed counter is rejected even with a newer timestamp", []Voucher{v(5, 0)}, v(4, time.Minute), false, VoucherCounterNotAdvancing, 5},
		{"counter zero is acceptable as a first voucher", nil, v(0, 0), true, VoucherAccepted, 0},
		{"a greater counter wins even with an older timestamp", []Voucher{v(5, 0)}, v(6, -time.Hour), true, VoucherAccepted, 6},
		{"a voucher stamped exactly the clock tolerance ahead of the receipt time is accepted", nil, v(1, timing.ClockTolerance), true, VoucherAccepted, 1},
		{"a voucher stamped one nanosecond beyond the tolerance is rejected and stores nothing", nil, v(1, timing.ClockTolerance+time.Nanosecond), false, VoucherFromFuture, 0},
		{"a future voucher with a higher counter does not replace the stored one", []Voucher{v(5, 0)}, v(9, timing.ClockTolerance+time.Nanosecond), false, VoucherFromFuture, 5},
		{"the future check comes before the counter check", []Voucher{v(5, 0)}, v(4, time.Hour), false, VoucherFromFuture, 5},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			s := Vouchers{}
			for _, st := range r.stored {
				s.Accept(st, t0)
			}
			key := VoucherKey{node(nB), dig(1)}
			before, hadBefore := s[key]
			d := s.Accept(r.incoming, t0)
			if d.Accepted != r.accepted || d.Reason != r.reason {
				t.Fatalf("got %+v", d)
			}
			after, has := s[key]
			if !r.accepted && (has != hadBefore || after != before) {
				t.Fatalf("a rejected voucher changed the slot: %+v (present %v) -> %+v (present %v)", before, hadBefore, after, has)
			}
			if r.accepted && after != r.incoming {
				t.Fatalf("slot holds %+v, want the accepted voucher", after)
			}
			if after.Counter != r.keptCtr {
				t.Fatalf("kept counter %d, want %d", after.Counter, r.keptCtr)
			}
		})
	}

	t.Run("slots are independent per issuer and per edge", func(t *testing.T) {
		s := Vouchers{}
		s.Accept(Voucher{Issuer: node(nB), Edge: dig(1), IssuedAt: t0, Counter: 5}, t0)
		for _, other := range []Voucher{
			{Issuer: node(nC), Edge: dig(1), IssuedAt: t0, Counter: 1},
			{Issuer: node(nB), Edge: dig(2), IssuedAt: t0, Counter: 1},
		} {
			if !s.Accept(other, t0).Accepted {
				t.Fatalf("voucher %+v rejected by an unrelated slot", other)
			}
		}
	})
}

func nodes(bs ...byte) []ident.NodeID {
	var out []ident.NodeID
	for _, b := range bs {
		out = append(out, node(b))
	}
	return out
}

func digests(bs ...byte) []CredentialDigest {
	var out []CredentialDigest
	for _, b := range bs {
		out = append(out, dig(b))
	}
	return out
}

func TestRevokeLocally(t *testing.T) {
	t.Run("revoking a direct party also stops vouching and revokes the credential", func(t *testing.T) {
		e := mustRevoke(t, threeNodeChain().v, nB)
		if e.Node != node(nB) || e.AlreadyRevoked {
			t.Fatalf("%+v", e)
		}
		if !slices.Equal(e.StopVouching, digests(1)) || !slices.Equal(e.RevokeCredentials, digests(1)) {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("revoking a node that is not a direct party only adds it to the set", func(t *testing.T) {
		e := mustRevoke(t, threeNodeChain().v, nC)
		if e.Node != node(nC) || len(e.StopVouching) != 0 || len(e.RevokeCredentials) != 0 {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("credentials with the party that are already revoked are not revoked again", func(t *testing.T) {
		w := newWorld(nA).edge(1, nA, nB).edge(2, nA, nB).revokeCred(1)
		e := mustRevoke(t, w.v, nB)
		if !slices.Equal(e.RevokeCredentials, digests(2)) {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("credentials between other nodes are not touched", func(t *testing.T) {
		e := mustRevoke(t, threeNodeChain().v, nB)
		if slices.Contains(e.RevokeCredentials, dig(2)) {
			t.Fatalf("revoked a credential the local node is not party to: %+v", e)
		}
	})
	t.Run("revoking a node already in the set reports it", func(t *testing.T) {
		e := mustRevoke(t, threeNodeChain().revokeNode(nB).v, nB)
		if !e.AlreadyRevoked {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("a node cannot revoke itself", func(t *testing.T) {
		if _, err := RevokeLocally(threeNodeChain().v, node(nA)); !errors.Is(err, ErrRevokeSelf) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("after applying the effects the party and the nodes that depend on it lose access", func(t *testing.T) {
		w := threeNodeChain()
		e := mustRevoke(t, w.v, nB)
		w.v.LocallyRevoked[e.Node] = struct{}{}
		for _, d := range e.RevokeCredentials {
			w.v.RevokedCredentials[d] = struct{}{}
		}
		for _, target := range []byte{nB, nC} {
			if got := Evaluate(w.v, node(target), t0); got.Outcome.Allowed() {
				t.Fatalf("target %d still allowed: %v", target, got.Reason)
			}
		}
	})
}

func TestClearLocallyRevoked(t *testing.T) {
	rows := []struct {
		rule       string
		world      func() *world
		target     byte
		wasRevoked bool
		newPairing bool
	}{
		{"clearing with a usable credential left restores trust, no new pairing", func() *world { return newWorld(nA).edge(1, nA, nB).revokeNode(nB) }, nB, true, false},
		{"clearing after the credential was revoked needs a new pairing", func() *world { return newWorld(nA).edge(1, nA, nB).revokeNode(nB).revokeCred(1) }, nB, true, true},
		{"clearing after the credential expired needs a new pairing", func() *world { return newWorld(nA).edge(1, nA, nB).expiring(1, t0).revokeNode(nB) }, nB, true, true},
		{"a fresh credential next to a revoked one needs no new pairing", func() *world { return newWorld(nA).edge(1, nA, nB).edge(2, nA, nB).revokeCred(1).revokeNode(nB) }, nB, true, false},
		{"a node that was only inherited needs no pairing", func() *world { return threeNodeChain().revokeNode(nC) }, nC, true, false},
		{"clearing an absent entry is reported as such", func() *world { return newWorld(nA).edge(1, nA, nB) }, nB, false, false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			e := ClearLocallyRevoked(r.world().v, node(r.target), t0)
			if e.WasRevoked != r.wasRevoked || e.NewPairingRequired != r.newPairing {
				t.Fatalf("%+v", e)
			}
		})
	}
}

func TestVouchableEdges(t *testing.T) {
	rows := []struct {
		rule  string
		world func() *world
		want  []CredentialDigest
	}{
		{"every usable direct edge is vouched for", func() *world { return newWorld(nA).edge(1, nA, nB).edge(2, nA, nC) }, digests(1, 2)},
		{"edges of other nodes are not vouched for by the local node", func() *world { return newWorld(nA).edge(1, nA, nB).edge(2, nB, nC) }, digests(1)},
		{"a revoked edge is not vouched for", func() *world { return newWorld(nA).edge(1, nA, nB).edge(2, nA, nC).revokeCred(1) }, digests(2)},
		{"an expired edge is not vouched for", func() *world { return newWorld(nA).edge(1, nA, nB).expiring(1, t0) }, nil},
		{"a locally revoked party is not vouched for even before the credential is revoked", func() *world { return newWorld(nA).edge(1, nA, nB).edge(2, nA, nC).revokeNode(nB) }, digests(2)},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := VouchableEdges(r.world().v, t0); !slices.Equal(got, r.want) {
				t.Fatalf("got %v, want %v", got, r.want)
			}
		})
	}
}

func TestIssuanceGate(t *testing.T) {
	g := DefaultGracePeriod
	// A has direct parties B and C; D is a party of B only.
	base := func() *world { return newWorld(nA).edge(1, nA, nB).edge(2, nA, nC).edge(3, nB, nD) }
	rows := []struct {
		rule    string
		world   func() *world // nil means base
		mutate  func(*IssuanceState)
		allowed bool
		reason  IssuanceReason
		waiting []ident.NodeID
	}{
		{"running and out of contact no longer than the grace period issues at once", nil, func(s *IssuanceState) { s.OfflineFor = 30 * time.Second }, true, IssuanceOpen, nil},
		{"out of contact for exactly the grace period is not yet gated", nil, func(s *IssuanceState) { s.OfflineFor = g }, true, IssuanceOpen, nil},
		{"out of contact longer than the grace period waits for every direct party that is not unreachable", nil, func(s *IssuanceState) { s.OfflineFor = g + time.Second }, false, IssuanceAwaitingSync, nodes(nB, nC)},
		{"the grace period is the node's own setting", func() *world { w := base(); w.v.Settings.GracePeriod = time.Hour; return w }, func(s *IssuanceState) { s.OfflineFor = 30 * time.Minute }, true, IssuanceOpen, nil},
		{"just started, even after a one-minute stop, syncs first", nil, func(s *IssuanceState) { s.Starting = true; s.OfflineFor = time.Minute }, false, IssuanceAwaitingSync, nodes(nB, nC)},
		{"a node restored from backup waits for every direct party that is not unreachable", nil, func(s *IssuanceState) { s.RestoredFromBackup = true }, false, IssuanceAwaitingSync, nodes(nB, nC)},
		{"partial sync still waits and names the missing party", nil, func(s *IssuanceState) {
			s.RestoredFromBackup = true
			s.Synced[node(nB)] = struct{}{}
		}, false, IssuanceAwaitingSync, nodes(nC)},
		{"one synced and one unreachable party opens the gate", nil, func(s *IssuanceState) {
			s.Starting = true
			s.Synced[node(nB)] = struct{}{}
			s.Unreachable[node(nC)] = struct{}{}
		}, true, IssuanceOpen, nil},
		{"all parties unreachable and no directive pending opens the gate", nil, func(s *IssuanceState) {
			s.Starting = true
			s.Unreachable[node(nB)], s.Unreachable[node(nC)] = struct{}{}, struct{}{}
		}, true, IssuanceOpen, nil},
		{"all parties unreachable with a directive pending waits for directives", nil, func(s *IssuanceState) {
			s.Starting = true
			s.Unreachable[node(nB)], s.Unreachable[node(nC)] = struct{}{}, struct{}{}
			s.PendingDirectives = 1
		}, false, IssuanceAwaitingDirectives, nil},
		{"a party neither synced nor unreachable is still waited for", nil, func(s *IssuanceState) {
			s.Starting = true
			s.Unreachable[node(nB)] = struct{}{}
		}, false, IssuanceAwaitingSync, nodes(nC)},
		{"synced with all parties but a directive pending still waits", nil, func(s *IssuanceState) {
			s.OfflineFor = 2 * g
			s.Synced[node(nB)], s.Synced[node(nC)] = struct{}{}, struct{}{}
			s.PendingDirectives = 1
		}, false, IssuanceAwaitingDirectives, nil},
		{"synced with all parties and no directive pending opens the gate", nil, func(s *IssuanceState) {
			s.OfflineFor = 2 * g
			s.Synced[node(nB)], s.Synced[node(nC)] = struct{}{}, struct{}{}
		}, true, IssuanceOpen, nil},
		{"a gated node without direct parties only waits for directives", func() *world { return newWorld(nA) }, func(s *IssuanceState) {
			s.RestoredFromBackup = true
			s.PendingDirectives = 2
		}, false, IssuanceAwaitingDirectives, nil},
		{"a continuously running node ignores pending directives", nil, func(s *IssuanceState) { s.PendingDirectives = 3 }, true, IssuanceOpen, nil},
		{"a party with only a revoked credential is not waited for", func() *world { return base().revokeCred(2) }, func(s *IssuanceState) { s.Starting = true }, false, IssuanceAwaitingSync, nodes(nB)},
		{"a party with only an expired credential is not waited for", func() *world { return base().expiring(2, t0) }, func(s *IssuanceState) { s.Starting = true }, false, IssuanceAwaitingSync, nodes(nB)},
		{"a party with several credentials is named once", func() *world { return base().edge(4, nC, nA) }, func(s *IssuanceState) { s.Starting = true }, false, IssuanceAwaitingSync, nodes(nB, nC)},
		{"parties of other nodes are not the node's direct parties", nil, func(s *IssuanceState) {
			s.Starting = true
			s.Synced[node(nB)], s.Synced[node(nC)] = struct{}{}, struct{}{}
		}, true, IssuanceOpen, nil},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			mk := base
			if r.world != nil {
				mk = r.world
			}
			s := IssuanceState{Synced: NodeSet{}, Unreachable: NodeSet{}}
			r.mutate(&s)
			got := IssuanceGate(mk().v, s, t0)
			if got.Allowed != r.allowed || got.Reason != r.reason || !slices.Equal(got.WaitingFor, r.waiting) {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestEvictionProgress(t *testing.T) {
	// A is local. X has direct parties B, C and A itself; D hangs off B.
	world := func() *world {
		return newWorld(nA).edge(1, nA, nB).edge(2, nB, nX).edge(3, nA, nX).edge(4, nB, nD).edge(5, nA, nC).edge(6, nC, nX).acceptAll().vouchAll()
	}
	partyIDs := func(e Eviction) []ident.NodeID {
		var out []ident.NodeID
		for _, p := range e.Unrevoked {
			out = append(out, p.Party)
		}
		return out
	}
	t.Run("lists every direct party whose credential with X is not revoked, and is incomplete", func(t *testing.T) {
		e := EvictionProgress(world().v, node(nX), t0)
		if e.Complete || !slices.Equal(partyIDs(e), nodes(nA, nB, nC)) {
			t.Fatalf("%+v", e)
		}
		if !slices.Equal(e.Unrevoked[1].Credentials, digests(2)) {
			t.Fatalf("%+v", e.Unrevoked)
		}
	})
	t.Run("a party that revoked drops off the list", func(t *testing.T) {
		e := EvictionProgress(world().revokeCred(2).v, node(nX), t0)
		if !slices.Equal(partyIDs(e), nodes(nA, nC)) {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("complete once every party revoked", func(t *testing.T) {
		e := EvictionProgress(world().revokeCred(2).revokeCred(3).revokeCred(6).v, node(nX), t0)
		if !e.Complete || len(e.Unrevoked) != 0 {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("one sleeping party keeps the eviction incomplete and is named", func(t *testing.T) {
		// B and C are done; A's own credential is still outstanding, as would be a sleeping party's.
		e := EvictionProgress(world().revokeCred(2).revokeCred(6).v, node(nX), t0)
		if e.Complete || !slices.Equal(partyIDs(e), nodes(nA)) {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("an expired credential no longer counts as outstanding", func(t *testing.T) {
		e := EvictionProgress(world().expiring(2, t0).v, node(nX), t0)
		if !slices.Equal(partyIDs(e), nodes(nA, nC)) {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("dependent paths are those through X or ending at X", func(t *testing.T) {
		e := EvictionProgress(world().v, node(nX), t0)
		var got [][2]byte
		for _, p := range e.Dependent {
			got = append(got, [2]byte{nodeByte(p.Via), nodeByte(p.To)})
		}
		want := [][2]byte{{nB, nX}, {nC, nX}, {nX, nB}, {nX, nC}}
		slices.SortFunc(got, func(a, b [2]byte) int { return int(a[0])*256 + int(a[1]) - int(b[0])*256 - int(b[1]) })
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
	t.Run("a path through another node to D does not depend on X", func(t *testing.T) {
		e := EvictionProgress(world().v, node(nX), t0)
		for _, p := range e.Dependent {
			if p.To == node(nD) {
				t.Fatalf("path to D listed: %+v", p)
			}
		}
	})
	t.Run("a revoked credential removes the paths that rest on it", func(t *testing.T) {
		e := EvictionProgress(world().revokeCred(2).v, node(nX), t0)
		for _, p := range e.Dependent {
			if slices.Contains(p.Credentials, dig(2)) {
				t.Fatalf("path on a revoked credential: %+v", p)
			}
		}
	})
	t.Run("a path whose voucher aged out still counts as dependent", func(t *testing.T) {
		e := EvictionProgress(world().vouch(nB, 2, time.Hour).v, node(nX), t0)
		found := false
		for _, p := range e.Dependent {
			found = found || (p.Via == node(nB) && p.To == node(nX))
		}
		if !found {
			t.Fatalf("%+v", e.Dependent)
		}
	})
}

func TestNewEdgeAudience(t *testing.T) {
	// B has parties A and C; X has party D; the new edge is B-X. E is unrelated.
	mk := func() *world {
		return newWorld(nA).edge(1, nA, nB).edge(2, nB, nC).edge(3, nX, nD).edge(4, nA, 7)
	}
	rows := []struct {
		rule  string
		world func() *world
		want  []ident.NodeID
	}{
		{"both ends and their direct parties are told", mk, nodes(nA, nB, nC, nD, nX)},
		{"nodes that are only parties of the local node are not told", mk, nodes(nA, nB, nC, nD, nX)},
		{"a revoked credential's party is not told", func() *world { return mk().revokeCred(2) }, nodes(nA, nB, nD, nX)},
		{"the audience is ordered and has no duplicates", func() *world { return mk().edge(5, nA, nX) }, nodes(nA, nB, nC, nD, nX)},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := NewEdgeAudience(r.world().v, node(nB), node(nX), t0); !slices.Equal(got, r.want) {
				t.Fatalf("got %v, want %v", got, r.want)
			}
		})
	}
}

func TestApplyDirective(t *testing.T) {
	d := EdgeRevocationDirective{Credential: dig(1), A: node(nA), B: node(nB)}
	rows := []struct {
		rule  string
		view  View
		dir   EdgeRevocationDirective
		want  DirectiveDisposition
		count int
	}{
		{"an edge that involves the local node as first end is revoked", newWorld(nA).v, d, DirectiveApply, 1},
		{"an edge that involves the local node as second end is revoked", newWorld(nB).v, d, DirectiveApply, 1},
		{"an edge between two other nodes is not for this node", newWorld(nC).v, d, DirectiveNotForThisNode, 0},
		{"a credential that is already revoked needs nothing", newWorld(nA).revokeCred(1).v, d, DirectiveAlreadyEffective, 0},
		{"a directive for a credential the node has not seen still applies", newWorld(nA).v, d, DirectiveApply, 1},
		{"a directive naming the credential's ends in the other order applies", newWorld(nA).edge(1, nA, nB).v, EdgeRevocationDirective{Credential: dig(1), A: node(nB), B: node(nA)}, DirectiveApply, 1},
		{"a directive whose claimed ends differ from the real credential is not executed", newWorld(nA).edge(1, nA, nB).v, EdgeRevocationDirective{Credential: dig(1), A: node(nA), B: node(nC)}, DirectiveMismatch, 0},
		{"a directive that claims the local node on a credential of two other nodes is not executed", newWorld(nC).edge(1, nA, nB).v, EdgeRevocationDirective{Credential: dig(1), A: node(nC), B: node(nB)}, DirectiveMismatch, 0},
		{"a mismatching directive is refused even for an already revoked credential", newWorld(nA).edge(1, nA, nB).revokeCred(1).v, EdgeRevocationDirective{Credential: dig(1), A: node(nA), B: node(nC)}, DirectiveMismatch, 0},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			got := ApplyDirective(r.view, r.dir)
			if got.Disposition != r.want || len(got.Effects.RevokeCredentials) != r.count || len(got.Effects.StopVouching) != r.count {
				t.Fatalf("%+v", got)
			}
			if r.count == 1 && (got.Effects.RevokeCredentials[0] != dig(1) || got.Effects.StopVouching[0] != dig(1)) {
				t.Fatalf("%+v", got)
			}
		})
	}
}
