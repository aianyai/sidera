package trust

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/timing"
)

type evalRow struct {
	rule   string
	world  func() *world
	target byte
	now    time.Time // zero means t0
	want   Outcome
	reason Reason
	via    []byte // intermediates of the reported paths, in order
}

func runEvalRows(t *testing.T, rows []evalRow) {
	t.Helper()
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			now := r.now
			if now.IsZero() {
				now = t0
			}
			got := Evaluate(r.world().v, node(r.target), now)
			if got.Outcome != r.want || got.Reason != r.reason {
				t.Fatalf("got %v/%v, want %v/%v", got.Outcome, got.Reason, r.want, r.reason)
			}
			if !equalBytes(viaOf(got.Paths), r.via) {
				t.Fatalf("paths via %v, want %v", viaOf(got.Paths), r.via)
			}
			for _, p := range got.Paths {
				if len(p.Credentials) > 2 {
					t.Fatalf("path with %d credentials", len(p.Credentials))
				}
			}
		})
	}
}

func TestEvaluateDirect(t *testing.T) {
	direct := func() *world { return newWorld(nA).edge(1, nA, nB) }
	runEvalRows(t, []evalRow{
		{"direct trust needs no vouchers and no inheritance switch", direct, nB, time.Time{}, AllowedDirect, ReasonDirect, []byte{0}},
		{"direct trust is symmetric in the credential's end order", func() *world { return newWorld(nA).edge(1, nB, nA) }, nB, time.Time{}, AllowedDirect, ReasonDirect, []byte{0}},
		{"direct trust is refused for a revoked credential", func() *world { return direct().revokeCred(1) }, nB, time.Time{}, Denied, ReasonCredentialRevoked, nil},
		{"direct trust is refused for an expired credential", func() *world { return direct().expiring(1, t0) }, nB, time.Time{}, Denied, ReasonCredentialExpired, nil},
		{"a credential is valid until the instant before NotAfter", func() *world { return direct().expiring(1, t0.Add(time.Nanosecond)) }, nB, time.Time{}, AllowedDirect, ReasonDirect, []byte{0}},
		{"direct trust is refused for a locally revoked target", func() *world { return direct().revokeNode(nB) }, nB, time.Time{}, Denied, ReasonLocallyRevoked, nil},
		{"a node never trusts itself through the evaluation", direct, nA, time.Time{}, Denied, ReasonSelf, nil},
		{"a stranger has no path", direct, nC, time.Time{}, Denied, ReasonNoPath, nil},
		{"a new pairing works after the old credential was revoked", func() *world { return direct().edge(2, nA, nB).revokeCred(1) }, nB, time.Time{}, AllowedDirect, ReasonDirect, []byte{0}},
		{"a credential between a node and itself is ignored", func() *world { return newWorld(nA).edge(1, nA, nA) }, nA, time.Time{}, Denied, ReasonSelf, nil},
	})
}

func TestEvaluateInherited(t *testing.T) {
	chain := threeNodeChain
	runEvalRows(t, []evalRow{
		{"inherited trust through one intermediate with fresh vouchers", chain, nC, time.Time{}, AllowedInherited, ReasonInherited, []byte{nB}},
		{"inheritance needs the local switch", func() *world { w := chain(); w.v.Settings.AcceptsInheritance = false; return w }, nC, time.Time{}, Denied, ReasonInheritanceDisabled, nil},
		{"inheritance needs the target's declared switch", func() *world { return chain().declare(nC, false) }, nC, time.Time{}, Denied, ReasonInheritanceDisabled, nil},
		{"a target without a known declaration is not assumed to accept and is told apart from one that refuses", func() *world { w := chain(); delete(w.v.DeclaredAcceptsInheritance, node(nC)); return w }, nC, time.Time{}, Denied, ReasonDeclarationUnknown, nil},
		{"the intermediate's own switch is not consulted", func() *world { return chain().declare(nB, false) }, nC, time.Time{}, AllowedInherited, ReasonInherited, []byte{nB}},
		{"inheritance never extends beyond one intermediate: A reaches C but not D", func() *world { return chain().edge(3, nC, nD).acceptAll().vouchAll() }, nD, time.Time{}, Denied, ReasonNoPath, nil},
		{"in a four-node chain the second node reaches the fourth", func() *world {
			w := newWorld(nB).edge(1, nA, nB).edge(2, nB, nC).edge(3, nC, nD).acceptAll().vouchAll()
			return w
		}, nD, time.Time{}, AllowedInherited, ReasonInherited, []byte{nC}},
		{"a locally revoked intermediate is refused", func() *world { return chain().revokeNode(nB) }, nC, time.Time{}, Denied, ReasonIntermediateLocallyRevoked, nil},
		{"a locally revoked target is refused despite a vouched path", func() *world { return chain().revokeNode(nC) }, nC, time.Time{}, Denied, ReasonLocallyRevoked, nil},
		{"a revoked first credential ends the path", func() *world { return chain().revokeCred(1) }, nC, time.Time{}, Denied, ReasonCredentialRevoked, nil},
		{"a revoked second credential ends the path", func() *world { return chain().revokeCred(2) }, nC, time.Time{}, Denied, ReasonCredentialRevoked, nil},
		{"an expired credential ends the path", func() *world { return chain().expiring(2, t0.Add(-time.Second)) }, nC, time.Time{}, Denied, ReasonCredentialExpired, nil},
		{"direct trust wins over an inherited path", func() *world { return chain().edge(3, nA, nC).vouchAll() }, nC, time.Time{}, AllowedDirect, ReasonDirect, []byte{0}},
		{"a revoked direct credential leaves an inherited path usable", func() *world { return chain().edge(3, nA, nC).revokeCred(3).vouchAll() }, nC, time.Time{}, AllowedInherited, ReasonInherited, []byte{nB}},
	})
}

func TestEvaluateMultiplePaths(t *testing.T) {
	// A trusts C through B and through D.
	twoPaths := func() *world {
		return newWorld(nA).edge(1, nA, nB).edge(2, nB, nC).edge(3, nA, nD).edge(4, nD, nC).acceptAll().vouchAll()
	}
	runEvalRows(t, []evalRow{
		{"every valid intermediate is reported", twoPaths, nC, time.Time{}, AllowedInherited, ReasonInherited, []byte{nB, nD}},
		{"any valid path suffices: one credential revoked, trust stays", func() *world { return twoPaths().revokeCred(2) }, nC, time.Time{}, AllowedInherited, ReasonInherited, []byte{nD}},
		{"any valid path suffices: one intermediate locally revoked, trust stays", func() *world { return twoPaths().revokeNode(nB) }, nC, time.Time{}, AllowedInherited, ReasonInherited, []byte{nD}},
		{"any valid path suffices: one voucher stale, trust stays", func() *world { return twoPaths().vouch(nB, 2, time.Hour) }, nC, time.Time{}, AllowedInherited, ReasonInherited, []byte{nD}},
		{"both paths revoked ends the trust", func() *world { return twoPaths().revokeCred(2).revokeCred(4) }, nC, time.Time{}, Denied, ReasonCredentialRevoked, nil},
		{"both paths stale suspends with both paths listed", func() *world { return twoPaths().vouch(nB, 2, time.Hour).vouch(nD, 4, time.Hour) }, nC, time.Time{}, Suspended, ReasonVoucherStale, []byte{nB, nD}},
		{"a stale path next to a revoked one suspends with the stale one only", func() *world { return twoPaths().vouch(nB, 2, time.Hour).revokeCred(4) }, nC, time.Time{}, Suspended, ReasonVoucherStale, []byte{nB}},
		{"the most specific denial is reported", func() *world { return twoPaths().revokeCred(2).revokeNode(nD) }, nC, time.Time{}, Denied, ReasonIntermediateLocallyRevoked, nil},
	})
}

func TestEvaluateVouchers(t *testing.T) {
	grace := DefaultGracePeriod
	runEvalRows(t, []evalRow{
		{"a voucher exactly one grace period old is valid", func() *world { return threeNodeChain().vouch(nB, 2, grace) }, nC, time.Time{}, AllowedInherited, ReasonInherited, []byte{nB}},
		{"a voucher older than the grace period suspends", func() *world { return threeNodeChain().vouch(nB, 2, grace+time.Nanosecond) }, nC, time.Time{}, Suspended, ReasonVoucherStale, []byte{nB}},
		{"a stale voucher on the first edge suspends", func() *world { return threeNodeChain().vouch(nB, 1, grace+time.Second) }, nC, time.Time{}, Suspended, ReasonVoucherStale, []byte{nB}},
		{"a voucher stamped within the clock tolerance ahead is valid", func() *world { return threeNodeChain().vouch(nB, 2, -timing.ClockTolerance) }, nC, time.Time{}, AllowedInherited, ReasonInherited, []byte{nB}},
		{"a voucher stamped beyond the clock tolerance ahead suspends, as after the verifier's clock moved back since receipt", func() *world { return threeNodeChain().vouch(nB, 2, -timing.ClockTolerance-time.Nanosecond) }, nC, time.Time{}, Suspended, ReasonVoucherFuture, []byte{nB}},
		{"a missing voucher suspends", func() *world { w := threeNodeChain(); delete(w.v.Vouchers, VoucherKey{node(nB), dig(2)}); return w }, nC, time.Time{}, Suspended, ReasonVoucherMissing, []byte{nB}},
		{"a voucher from the local node does not stand in for the intermediate's", func() *world {
			w := threeNodeChain()
			delete(w.v.Vouchers, VoucherKey{node(nB), dig(1)})
			return w.vouch(nA, 1, time.Minute)
		}, nC, time.Time{}, Suspended, ReasonVoucherMissing, []byte{nB}},
		{"a voucher for a revoked credential's successor is not carried over", func() *world {
			// The pairing was renewed under a new digest; B only vouched for the old one.
			return threeNodeChain().edge(3, nA, nB).revokeCred(1)
		}, nC, time.Time{}, Suspended, ReasonVoucherMissing, []byte{nB}},
		{"a suspended path recovers when a fresh voucher with a higher counter arrives", func() *world {
			return threeNodeChain().vouch(nB, 2, time.Hour).receive(nB, 2, 2, time.Minute)
		}, nC, time.Time{}, AllowedInherited, ReasonInherited, []byte{nB}},
		{"the voucher ages out as time passes", threeNodeChain, nC, t0.Add(grace), Suspended, ReasonVoucherStale, []byte{nB}},
		{"direct trust ignores stale vouchers", func() *world { return threeNodeChain().edge(3, nA, nC).vouch(nB, 2, time.Hour) }, nC, time.Time{}, AllowedDirect, ReasonDirect, []byte{0}},
		{"a permanent denial wins over a missing voucher on the same path", func() *world {
			w := threeNodeChain().revokeNode(nB)
			delete(w.v.Vouchers, VoucherKey{node(nB), dig(2)})
			return w
		}, nC, time.Time{}, Denied, ReasonIntermediateLocallyRevoked, nil},
	})
}

func TestEvaluateScenarios(t *testing.T) {
	// A trusts B directly; B trusts C and D; the B-C credential is revoked.
	branching := func() *world {
		return newWorld(nA).edge(1, nA, nB).edge(2, nB, nC).edge(3, nB, nD).acceptAll().vouchAll().revokeCred(2)
	}
	runEvalRows(t, []evalRow{
		{"A-B-C: both sides accept inheritance, A and C reach each other", threeNodeChain, nC, time.Time{}, AllowedInherited, ReasonInherited, []byte{nB}},
		{"A-B-C seen from C: the reverse direction agrees", func() *world {
			return newWorld(nC).edge(1, nA, nB).edge(2, nB, nC).acceptAll().vouchAll()
		}, nA, time.Time{}, AllowedInherited, ReasonInherited, []byte{nB}},
		{"switch off on A: A refuses C", func() *world {
			w := threeNodeChain()
			w.v.Settings.AcceptsInheritance = false
			w.declare(nA, false)
			return w
		}, nC, time.Time{}, Denied, ReasonInheritanceDisabled, nil},
		{"switch off on A seen from C: C refuses A as well", func() *world {
			return newWorld(nC).edge(1, nA, nB).edge(2, nB, nC).acceptAll().declare(nA, false).vouchAll()
		}, nA, time.Time{}, Denied, ReasonInheritanceDisabled, nil},
		{"triangle: after A locally revokes B, B cannot regain A's trust through C", func() *world {
			return newWorld(nA).edge(1, nA, nB).edge(2, nB, nC).edge(3, nA, nC).acceptAll().vouchAll().revokeNode(nB).revokeCred(1)
		}, nB, time.Time{}, Denied, ReasonLocallyRevoked, nil},
		{"triangle: A's other party C is unaffected by revoking B", func() *world {
			return newWorld(nA).edge(1, nA, nB).edge(2, nB, nC).edge(3, nA, nC).acceptAll().vouchAll().revokeNode(nB).revokeCred(1)
		}, nC, time.Time{}, AllowedDirect, ReasonDirect, []byte{0}},
		{"A revokes X locally: M introduced by X is refused by A", func() *world {
			return newWorld(nA).edge(1, nA, nX).edge(2, nX, nB).acceptAll().vouchAll().revokeNode(nX)
		}, nB, time.Time{}, Denied, ReasonIntermediateLocallyRevoked, nil},
		{"C reaches X through A before A's revocation propagates and while vouchers are fresh", func() *world {
			return newWorld(nC).edge(1, nC, nA).edge(2, nA, nX).acceptAll().vouchAll()
		}, nX, time.Time{}, AllowedInherited, ReasonInherited, []byte{nA}},
		{"A revoked direct party X: once A's voucher for the edge ages out, C cannot reach X", func() *world {
			return newWorld(nC).edge(1, nC, nA).edge(2, nA, nX).acceptAll().vouchAll().vouch(nA, 2, DefaultGracePeriod+time.Second)
		}, nX, time.Time{}, Suspended, ReasonVoucherStale, []byte{nA}},
		{"A revoked direct party X: once the revocation reaches C, C cannot reach X", func() *world {
			return newWorld(nC).edge(1, nC, nA).edge(2, nA, nX).acceptAll().vouchAll().revokeCred(2)
		}, nX, time.Time{}, Denied, ReasonCredentialRevoked, nil},
		{"A-B-C-D: A reaches C", func() *world {
			return newWorld(nA).edge(1, nA, nB).edge(2, nB, nC).edge(3, nC, nD).acceptAll().vouchAll()
		}, nC, time.Time{}, AllowedInherited, ReasonInherited, []byte{nB}},
		{"A-B-C-D: A does not reach D", func() *world {
			return newWorld(nA).edge(1, nA, nB).edge(2, nB, nC).edge(3, nC, nD).acceptAll().vouchAll()
		}, nD, time.Time{}, Denied, ReasonNoPath, nil},
		{"outsider holding no credential gets nothing", func() *world { return threeNodeChain() }, nX, time.Time{}, Denied, ReasonNoPath, nil},
		{"B-C revoked: A no longer reaches C", branching, nC, time.Time{}, Denied, ReasonCredentialRevoked, nil},
		{"B-C revoked: A still reaches D through B", branching, nD, time.Time{}, AllowedInherited, ReasonInherited, []byte{nB}},
		{"B-C revoked: A still trusts B directly", branching, nB, time.Time{}, AllowedDirect, ReasonDirect, []byte{0}},
	})

	t.Run("intermediate offline past the grace period suspends, then recovers", func(t *testing.T) {
		w := threeNodeChain().vouch(nB, 1, 11*time.Minute).vouch(nB, 2, 11*time.Minute)
		if got := Evaluate(w.v, node(nC), t0); got.Outcome != Suspended || got.Reason != ReasonVoucherStale {
			t.Fatalf("while offline: got %v/%v", got.Outcome, got.Reason)
		}
		w.receive(nB, 1, 2, 0).receive(nB, 2, 2, 0)
		if got := Evaluate(w.v, node(nC), t0); got.Outcome != AllowedInherited {
			t.Fatalf("after fresh vouchers: got %v/%v", got.Outcome, got.Reason)
		}
	})
}

// A voucher is stamped by the issuer's clock and judged by the verifier's, so a
// fast issuer clock must not extend the issuer's reach beyond grace plus the
// clock tolerance counted from the moment the issuer went offline. A voucher
// stamped beyond the tolerance is rejected on receipt, so it can never become
// valid later.
func TestEvaluateIssuerClockSkew(t *testing.T) {
	grace := DefaultGracePeriod
	offline := t0 // the intermediate's last voucher was received at this moment
	rows := []struct {
		rule         string
		skew         time.Duration
		at           time.Duration // verifier's time after the issuer went offline
		wantAccepted bool
		want         Outcome
		wantReason   Reason
	}{
		{"issuer clock exactly at the tolerance ahead: still valid at grace plus tolerance", timing.ClockTolerance, grace + timing.ClockTolerance, true, AllowedInherited, ReasonInherited},
		{"issuer clock exactly at the tolerance ahead: suspended right after grace plus tolerance", timing.ClockTolerance, grace + timing.ClockTolerance + time.Nanosecond, true, Suspended, ReasonVoucherStale},
		{"issuer clock correct: suspended right after the grace period", 0, grace + time.Nanosecond, true, Suspended, ReasonVoucherStale},
		{"issuer clock one hour fast: the voucher is rejected and the intermediate is suspended from the start", time.Hour, 0, false, Suspended, ReasonVoucherMissing},
		{"issuer clock one hour fast: still suspended an hour later, when the stamp has come into range", time.Hour, time.Hour, false, Suspended, ReasonVoucherMissing},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			// The last vouchers were stamped by the issuer's clock, which runs skew ahead.
			w := newWorld(nA).edge(1, nA, nB).edge(2, nB, nC).acceptAll()
			for _, d := range []byte{1, 2} {
				vo := Voucher{Issuer: node(nB), Edge: dig(d), IssuedAt: offline.Add(r.skew), Counter: 9}
				if got := w.v.Vouchers.Accept(vo, offline).Accepted; got != r.wantAccepted {
					t.Fatalf("Accept returned %v, want %v", got, r.wantAccepted)
				}
			}
			got := Evaluate(w.v, node(nC), offline.Add(r.at))
			if got.Outcome != r.want || got.Reason != r.wantReason {
				t.Fatalf("got %v/%v, want %v/%v", got.Outcome, got.Reason, r.want, r.wantReason)
			}
		})
	}
}

// With every node online for an hour, vouchers issued every 3 minutes and
// pushed within the 1 minute budget never leave a gap that suspends inherited
// trust under the default grace period.
func TestEvaluateStaysAllowedWhileOnline(t *testing.T) {
	w := newWorld(nA).edge(1, nA, nB).edge(2, nB, nC).acceptAll()
	var counter uint64
	for minute := 0; minute <= 60; minute++ {
		now := t0.Add(time.Duration(minute) * time.Minute)
		// A voucher issued at minute 3k reaches the verifier one minute later.
		if minute%3 == 1 {
			counter++
			for _, d := range []byte{1, 2} {
				w.v.Vouchers.Accept(Voucher{Issuer: node(nB), Edge: dig(d), IssuedAt: now.Add(-time.Minute), Counter: counter}, now)
			}
		}
		if minute < 1 {
			continue
		}
		if got := Evaluate(w.v, node(nC), now); got.Outcome != AllowedInherited {
			t.Fatalf("minute %d: %v/%v", minute, got.Outcome, got.Reason)
		}
	}
}
