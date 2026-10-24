package preauth

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// testParams are small numbers that make every limit reachable in a few steps.
// They are not proposals for the real values.
func testParams() Params {
	return Params{
		UnauthenticatedCap:   3,
		NonMemberConcurrency: 2,
		NonMemberLifetime:    40 * time.Second,
		PairingOnlineTimeout: 30 * time.Second,
		SweepInterval:        3 * time.Second,
	}
}

func newAccounting(t testing.TB) *Accounting {
	t.Helper()
	a, err := NewAccounting(testParams())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func peerKey(t testing.TB, n byte) ident.PublicKey {
	t.Helper()
	return seedSigner(t, n).Public()
}

func key(t testing.TB, n byte, addr string) ConnKey { return ConnKey{Peer: peerKey(t, n), Addr: addr} }

var t0 = time.Unix(1700000000, 0).UTC()

func at(sec float64) time.Time { return t0.Add(time.Duration(sec * float64(time.Second))) }

func TestParamsValidate(t *testing.T) {
	good := testParams()
	with := func(f func(p *Params)) Params { p := good; f(&p); return p }
	for _, row := range []struct {
		rule    string
		p       Params
		field   string
		problem ParamsProblem
	}{
		{"consistent parameters are accepted", good, "", 0},
		{"a lifetime of exactly the exchange plus the pairing online time is accepted", with(func(p *Params) { p.NonMemberLifetime = ExchangeDeadline + p.PairingOnlineTimeout }), "", 0},
		{"a lifetime one nanosecond short of the exchange plus the pairing online time is refused", with(func(p *Params) { p.NonMemberLifetime = ExchangeDeadline + p.PairingOnlineTimeout - 1 }), "NonMemberLifetime", ProblemLifetimeTooShort},
		{"a lifetime shorter than the exchange alone is refused", with(func(p *Params) { p.NonMemberLifetime = 4 * time.Second }), "NonMemberLifetime", ProblemLifetimeTooShort},
		{"a longer pairing online time raises the lifetime floor", with(func(p *Params) { p.PairingOnlineTimeout = 60 * time.Second }), "NonMemberLifetime", ProblemLifetimeTooShort},
		{"an unset cap is refused", with(func(p *Params) { p.UnauthenticatedCap = 0 }), "UnauthenticatedCap", ProblemUnset},
		{"a negative cap is refused", with(func(p *Params) { p.UnauthenticatedCap = -1 }), "UnauthenticatedCap", ProblemUnset},
		{"unset non-member concurrency is refused", with(func(p *Params) { p.NonMemberConcurrency = 0 }), "NonMemberConcurrency", ProblemUnset},
		{"an unset lifetime is refused", with(func(p *Params) { p.NonMemberLifetime = 0 }), "NonMemberLifetime", ProblemUnset},
		{"an unset pairing online time is refused", with(func(p *Params) { p.PairingOnlineTimeout = 0 }), "PairingOnlineTimeout", ProblemUnset},
		{"an unset sweep interval is refused", with(func(p *Params) { p.SweepInterval = 0 }), "SweepInterval", ProblemUnset},
		{"the zero Params is refused", Params{}, "UnauthenticatedCap", ProblemUnset},
	} {
		t.Run(row.rule, func(t *testing.T) {
			err := row.p.Validate()
			_, newErr := NewAccounting(row.p)
			if row.field == "" {
				if err != nil || newErr != nil {
					t.Fatalf("Validate = %v, NewAccounting = %v", err, newErr)
				}
				return
			}
			var pe *ParamsError
			if !errors.As(err, &pe) || pe.Field != row.field || pe.Problem != row.problem || !errors.Is(err, ErrParams) {
				t.Fatalf("Validate = %v, want field %s, problem %v", err, row.field, row.problem)
			}
			if newErr == nil {
				t.Error("NewAccounting accepted parameters that Validate refuses")
			}
		})
	}
}

func admit(t *testing.T, a *Accounting, k ConnKey, now time.Time, want AdmissionReason) {
	t.Helper()
	got := a.Secured(k, DirectionInbound, now)
	wantVerdict := VerdictAllow
	if want == RefuseCapReached || want == RefuseInvalidInput || want == RefuseDuplicateKey {
		wantVerdict = VerdictRefuse
	}
	if got.Verdict != wantVerdict || got.Reason != want {
		t.Fatalf("Secured(%s) = %v/%v, want %v/%v", k.Addr, got.Verdict, got.Reason, wantVerdict, want)
	}
}

func wantCounts(t *testing.T, a *Accounting, unauth, exempt, nonMembers int) {
	t.Helper()
	if got := a.Counts(); got != (Counts{unauth, exempt, nonMembers}) {
		t.Fatalf("counts %+v, want unauthenticated %d, exempt %d, non-members %d", got, unauth, exempt, nonMembers)
	}
}

func TestAggregateCapRefusesBeyondTheCap(t *testing.T) {
	a := newAccounting(t)
	for i := byte(1); i <= 3; i++ {
		admit(t, a, key(t, i, "a"), at(0), AdmitCounted)
	}
	wantCounts(t, a, 3, 0, 0)
	admit(t, a, key(t, 4, "a"), at(0), RefuseCapReached)
	wantCounts(t, a, 3, 0, 0)
}

func TestOutboundConnectionsAreNotAccounted(t *testing.T) {
	a := newAccounting(t)
	for i := byte(1); i <= 3; i++ {
		admit(t, a, key(t, i, "a"), at(0), AdmitCounted)
	}
	got := a.Secured(key(t, 9, "a"), DirectionOutbound, at(0))
	if got != (Admission{VerdictAllow, AdmitOutbound}) {
		t.Errorf("outbound at the cap: %v/%v", got.Verdict, got.Reason)
	}
	wantCounts(t, a, 3, 0, 0)
}

func TestAKnownMemberIsExemptFromTheCapUnderAFlood(t *testing.T) {
	a := newAccounting(t)
	member := peerKey(t, 50)
	a.SetHeld([]ident.PublicKey{member})
	for i := byte(1); i <= 3; i++ {
		admit(t, a, key(t, i, "a"), at(0), AdmitCounted)
	}
	admit(t, a, key(t, 4, "a"), at(0), RefuseCapReached) // the flood is shut out
	admit(t, a, ConnKey{member, "a"}, at(0), AdmitKnownMember)
	admit(t, a, ConnKey{member, "b"}, at(0), AdmitKnownMember) // also a second connection
	wantCounts(t, a, 3, 2, 0)
	// A stranger is still refused; the member's connections took no place.
	admit(t, a, key(t, 5, "a"), at(0), RefuseCapReached)
}

func TestAMemberLearnedFromACompletedExchangeIsExemptNextTime(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "a")
	admit(t, a, k, at(0), AdmitCounted)
	if p := a.Completed(k, StateMember, at(1)); p != (Placement{VerdictAllow, PlaceReleased}) {
		t.Fatalf("Completed = %v/%v", p.Verdict, p.Reason)
	}
	wantCounts(t, a, 0, 0, 0)
	if !a.Known(k.Peer) {
		t.Fatal("the member is not known after its exchange completed")
	}
	for i := byte(10); i < 13; i++ {
		admit(t, a, key(t, i, "a"), at(2), AdmitCounted)
	}
	admit(t, a, ConnKey{k.Peer, "b"}, at(2), AdmitKnownMember)
}

func TestAnAuthorizedPersonIsKnownAfterItsFirstExchangeOnly(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "a")
	admit(t, a, k, at(0), AdmitCounted) // the first connection of a new device is a stranger's
	a.Completed(k, StateAuthorizedPerson, at(1))
	admit(t, a, ConnKey{k.Peer, "b"}, at(2), AdmitKnownMember)
}

func TestExchangeCompletionReleasesTheAggregateSlot(t *testing.T) {
	a := newAccounting(t)
	for i := byte(1); i <= 3; i++ {
		admit(t, a, key(t, i, "a"), at(0), AdmitCounted)
	}
	admit(t, a, key(t, 4, "a"), at(0), RefuseCapReached)
	a.Completed(key(t, 1, "a"), StateMember, at(1))
	admit(t, a, key(t, 4, "a"), at(1), AdmitCounted)
}

func TestDisconnectReleasesWhateverTheConnectionHeld(t *testing.T) {
	a := newAccounting(t)
	a.SetHeld([]ident.PublicKey{peerKey(t, 50)})
	un, ex, nm := key(t, 1, "a"), ConnKey{peerKey(t, 50), "a"}, key(t, 2, "a")
	admit(t, a, un, at(0), AdmitCounted)
	admit(t, a, ex, at(0), AdmitKnownMember)
	admit(t, a, nm, at(0), AdmitCounted)
	a.Completed(nm, StateNonMemberNode, at(1))
	wantCounts(t, a, 1, 1, 1)
	for _, k := range []ConnKey{un, ex, nm} {
		a.Disconnected(k)
	}
	wantCounts(t, a, 0, 0, 0)
	a.Disconnected(un) // a second release of the same key changes nothing
	a.Disconnected(key(t, 77, "zz"))
	wantCounts(t, a, 0, 0, 0)
}

// The handshake reports a hole-punched connection as inbound and the connection
// later reports outbound. Every event after Secured names the connection by its
// key alone, so the entry is found and released whichever direction the
// connection later claims.
func TestTheKeyHasNoDirection(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "relay-punched")
	admit(t, a, k, at(0), AdmitCounted)
	if !a.Connected(k, at(0)) {
		t.Fatal("the connection that the handshake called inbound is not found when it is established")
	}
	a.Disconnected(k)
	wantCounts(t, a, 0, 0, 0)
}

func refuseDuplicate(t *testing.T, a *Accounting, k ConnKey, now time.Time) {
	t.Helper()
	admit(t, a, k, now, RefuseDuplicateKey)
}

// The gater is asked once per connection, so a second call for a tracked key is
// a second connection with the same peer and address. Admitting it would let it
// share the first one's entry and outlive the books once the first ended.
func TestASecondInboundConnectionOnATrackedKeyIsRefused(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "a")
	admit(t, a, k, at(0), AdmitCounted)
	refuseDuplicate(t, a, k, at(1))
	wantCounts(t, a, 1, 0, 0) // the refusal changed nothing
	// The entry is still the first connection's: its deadline runs, and it is
	// released when that connection ends.
	a.Connected(k, at(1))
	if got := a.Overdue(at(6)); len(got) != 1 || got[0] != k {
		t.Fatalf("the first connection lost its deadline: %v", got)
	}
	a.Disconnected(k)
	wantCounts(t, a, 0, 0, 0)
	// With the first connection gone the key is free again.
	admit(t, a, k, at(7), AdmitCounted)
}

func TestADuplicateKeyIsRefusedBeforeTheCapIsLookedAt(t *testing.T) {
	a := newAccounting(t)
	for i := byte(1); i <= 3; i++ {
		admit(t, a, key(t, i, "a"), at(0), AdmitCounted)
	}
	refuseDuplicate(t, a, key(t, 1, "a"), at(0))
	admit(t, a, key(t, 4, "a"), at(0), RefuseCapReached)
	wantCounts(t, a, 3, 0, 0)
}

// Known members keep their exemption from the cap, and their first connection
// is tracked like any other. The exempt path only creates an entry for a key
// that has none, so a duplicate can neither count an entry twice nor replace it
// and lose the first connection's count.
func TestADuplicateKeyOfAKnownMemberIsRefusedWithoutLeakingAnEntry(t *testing.T) {
	a := newAccounting(t)
	m := ConnKey{peerKey(t, 50), "a"}
	a.SetHeld([]ident.PublicKey{m.Peer})
	admit(t, a, m, at(0), AdmitKnownMember)
	refuseDuplicate(t, a, m, at(1))
	refuseDuplicate(t, a, m, at(2))
	wantCounts(t, a, 0, 1, 0)
	a.Disconnected(m)
	wantCounts(t, a, 0, 0, 0)
	admit(t, a, m, at(3), AdmitKnownMember) // the key is free once its connection ended
	wantCounts(t, a, 0, 1, 0)
}

// A duplicate of a non-member's key cannot take over the non-member's place or
// outlive its lifetime.
func TestADuplicateKeyOfANonMemberIsRefused(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "a")
	admit(t, a, k, at(0), AdmitCounted)
	a.Connected(k, at(0))
	a.Completed(k, StateNonMemberNode, at(1))
	refuseDuplicate(t, a, k, at(2))
	wantCounts(t, a, 0, 0, 1)
	if got := a.Overdue(at(40)); len(got) != 1 || got[0] != k {
		t.Errorf("the non-member lifetime does not run: %v", got)
	}
}

// A connection rejected after the handshake produces no event, so its entry
// stays until the sweep, and a retry of the same key is refused until then.
func TestADuplicateKeyIsRefusedUntilTheSweepForgetsALostEntry(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "a")
	admit(t, a, k, at(0), AdmitCounted)
	refuseDuplicate(t, a, k, at(2.9))
	a.Sweep(at(3))
	admit(t, a, k, at(3), AdmitCounted)
	wantCounts(t, a, 1, 0, 0)
}

func TestAKeyIsFreeAgainOnceItsConnectionBecameAMember(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "a")
	admit(t, a, k, at(0), AdmitCounted)
	a.Completed(k, StateMember, at(1))
	admit(t, a, k, at(2), AdmitKnownMember)
	wantCounts(t, a, 0, 1, 0)
}

func TestAnOutboundConnectionOnATrackedKeyIsNotADuplicate(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "a")
	admit(t, a, k, at(0), AdmitCounted)
	if got := a.Secured(k, DirectionOutbound, at(1)); got != (Admission{VerdictAllow, AdmitOutbound}) {
		t.Errorf("outbound on a tracked key: %v/%v", got.Verdict, got.Reason)
	}
	wantCounts(t, a, 1, 0, 0)
}

func TestNonMemberConcurrencyLimit(t *testing.T) {
	a := newAccounting(t)
	ks := []ConnKey{key(t, 1, "a"), key(t, 2, "a"), key(t, 3, "a")}
	for _, k := range ks {
		admit(t, a, k, at(0), AdmitCounted)
	}
	for i, k := range ks[:2] {
		if p := a.Completed(k, StateNonMemberNode, at(1)); p != (Placement{VerdictAllow, PlaceNonMember}) {
			t.Fatalf("non-member %d: %v/%v", i, p.Verdict, p.Reason)
		}
	}
	wantCounts(t, a, 1, 0, 2)
	if p := a.Completed(ks[2], StateNonMemberNode, at(1)); p != (Placement{VerdictRefuse, RefuseNonMemberFull}) {
		t.Fatalf("beyond the limit: %v/%v", p.Verdict, p.Reason)
	}
	wantCounts(t, a, 0, 0, 2) // the refused one gave up its aggregate slot too
	// Deciding the same non-member again changes nothing.
	if p := a.Completed(ks[0], StateNonMemberNode, at(2)); p != (Placement{VerdictAllow, PlaceAlreadyNonMember}) {
		t.Fatalf("again: %v/%v", p.Verdict, p.Reason)
	}
	wantCounts(t, a, 0, 0, 2)
}

func TestANonMemberThatBecomesAMemberFreesItsPlace(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "a")
	admit(t, a, k, at(0), AdmitCounted)
	a.Completed(k, StateNonMemberNode, at(1))
	wantCounts(t, a, 0, 0, 1)
	if p := a.Completed(k, StateMember, at(10)); p != (Placement{VerdictAllow, PlaceReleased}) {
		t.Fatalf("pairing made it a member: %v/%v", p.Verdict, p.Reason)
	}
	wantCounts(t, a, 0, 0, 0)
	if !a.Known(k.Peer) {
		t.Error("the new member is not known")
	}
}

// The limits are about strangers holding connections, whatever they present: a
// person without a standing takes one of the same places as a node that is not
// a member.
func TestNonMemberPersonsAndNodesShareTheNonMemberLimits(t *testing.T) {
	a := newAccounting(t)
	ks := []ConnKey{key(t, 1, "a"), key(t, 2, "a"), key(t, 3, "a")}
	for _, k := range ks {
		admit(t, a, k, at(0), AdmitCounted)
	}
	if p := a.Completed(ks[0], StateNonMemberPerson, at(1)); p != (Placement{VerdictAllow, PlaceNonMember}) {
		t.Fatalf("person: %v/%v", p.Verdict, p.Reason)
	}
	if p := a.Completed(ks[1], StateNonMemberNode, at(1)); p != (Placement{VerdictAllow, PlaceNonMember}) {
		t.Fatalf("node: %v/%v", p.Verdict, p.Reason)
	}
	wantCounts(t, a, 1, 0, 2)
	if p := a.Completed(ks[2], StateNonMemberPerson, at(1)); p != (Placement{VerdictRefuse, RefuseNonMemberFull}) {
		t.Fatalf("person beyond the limit: %v/%v", p.Verdict, p.Reason)
	}
	a.Connected(ks[0], at(1))
	if got := a.Overdue(at(41)); len(got) != 1 || got[0] != ks[0] {
		t.Errorf("a non-member person is bound by the non-member lifetime: %v", got)
	}
}

func TestANonMemberPersonTakesAwayWhatWasKnown(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "a")
	admit(t, a, k, at(0), AdmitCounted)
	a.Completed(k, StateAuthorizedPerson, at(1))
	k2 := ConnKey{k.Peer, "b"}
	admit(t, a, k2, at(2), AdmitKnownMember)
	a.Completed(k2, StateNonMemberPerson, at(3)) // the person was removed from the list
	if a.Known(k.Peer) {
		t.Error("a person decided as a non-member is still known")
	}
}

func TestANonMemberDecisionTakesAwayWhatWasKnown(t *testing.T) {
	a := newAccounting(t)
	member := peerKey(t, 50)
	a.SetHeld([]ident.PublicKey{member}) // from a certificate the node holds
	k := ConnKey{member, "a"}
	admit(t, a, k, at(0), AdmitKnownMember)
	a.Completed(k, StateNonMemberNode, at(1)) // its trust was revoked meanwhile
	if a.Known(member) {
		t.Error("a peer decided as a non-member is still known as a member")
	}
	wantCounts(t, a, 0, 0, 1)
}

// A refused exchange means the peer's certificate is not a member's: the
// exemption an earlier exchange gave the peer ends with it, whichever source
// made it known.
func TestARefusedExchangeWithdrawsTheExemption(t *testing.T) {
	for _, source := range []string{"learned from an exchange", "held certificate"} {
		t.Run(source, func(t *testing.T) {
			a := newAccounting(t)
			peer, other := peerKey(t, 50), peerKey(t, 51)
			if source == "held certificate" {
				a.SetHeld([]ident.PublicKey{peer, other})
			} else {
				for _, p := range []ident.PublicKey{peer, other} {
					a.Completed(ConnKey{p, "x"}, StateMember, at(0))
				}
			}
			k := ConnKey{peer, "a"}
			admit(t, a, k, at(1), AdmitKnownMember)
			if p := a.Completed(k, StateUnauthenticated, at(2)); p != (Placement{VerdictRefuse, RefuseNotCompleted}) {
				t.Fatalf("Completed = %v/%v", p.Verdict, p.Reason)
			}
			if a.Known(peer) {
				t.Error("a peer whose exchange was refused is still known")
			}
			if !a.Known(other) {
				t.Error("a refused exchange of one peer made another unknown")
			}
			wantCounts(t, a, 0, 1, 0) // the entry stays until the connection ends
			a.Disconnected(k)
			// The next connection is a stranger's and counts against the cap.
			for i := byte(1); i <= 3; i++ {
				admit(t, a, key(t, i, "a"), at(3), AdmitCounted)
			}
			admit(t, a, ConnKey{peer, "b"}, at(3), RefuseCapReached)
			admit(t, a, ConnKey{other, "b"}, at(3), AdmitKnownMember)
		})
	}
}

func TestAnInvalidCompletedCallDoesNotWithdrawTheExemption(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "a")
	a.Completed(k, StateMember, at(0))
	a.Completed(k, StateUnauthenticated, time.Time{})
	a.Completed(ConnKey{Peer: k.Peer}, StateUnauthenticated, at(1))
	if !a.Known(k.Peer) {
		t.Error("a call that could not be judged forgot the peer")
	}
}

func TestForgetEndsTheKnownStandingFromEitherSource(t *testing.T) {
	a := newAccounting(t)
	learned, held, untouched := key(t, 1, "a"), peerKey(t, 2), peerKey(t, 3)
	a.Completed(learned, StateAuthorizedPerson, at(0))
	a.SetHeld([]ident.PublicKey{held, learned.Peer, untouched})
	a.Forget(learned.Peer)
	a.Forget(held)
	a.Forget(peerKey(t, 99)) // an unknown peer: nothing happens
	a.Forget(ident.PublicKey{})
	if a.Known(learned.Peer) || a.Known(held) {
		t.Error("a forgotten peer is still known")
	}
	if !a.Known(untouched) {
		t.Error("Forget touched another peer")
	}
	admit(t, a, ConnKey{learned.Peer, "b"}, at(1), AdmitCounted)
	// The held set is the caller's to refresh: SetHeld lists the peer again when
	// its certificate still belongs to a member.
	a.SetHeld([]ident.PublicKey{held})
	if !a.Known(held) {
		t.Error("SetHeld did not list the peer again")
	}
}

func TestCompletedWithoutAnEntryOnlyLearns(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "outbound")
	if p := a.Completed(k, StateMember, at(0)); p != (Placement{VerdictAllow, PlaceUntracked}) {
		t.Fatalf("member: %v/%v", p.Verdict, p.Reason)
	}
	if !a.Known(k.Peer) {
		t.Error("an untracked member is not learned")
	}
	if p := a.Completed(k, StateNonMemberNode, at(0)); p != (Placement{VerdictAllow, PlaceUntracked}) {
		t.Fatalf("non-member: %v/%v", p.Verdict, p.Reason)
	}
	wantCounts(t, a, 0, 0, 0)
}

func TestCompletedRefusesWhatItCannotPlace(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "a")
	admit(t, a, k, at(0), AdmitCounted)
	for _, row := range []struct {
		rule string
		k    ConnKey
		s    State
		now  time.Time
		want Placement
	}{
		{"an unauthenticated state is not a completed exchange", k, StateUnauthenticated, at(1), Placement{VerdictRefuse, RefuseNotCompleted}},
		{"an unset state", k, StateUnset, at(1), Placement{VerdictRefuse, RefusePlacementInput}},
		{"an unset key", ConnKey{}, StateMember, at(1), Placement{VerdictRefuse, RefusePlacementInput}},
		{"an unset address", ConnKey{Peer: k.Peer}, StateMember, at(1), Placement{VerdictRefuse, RefusePlacementInput}},
		{"an unset time", k, StateMember, time.Time{}, Placement{VerdictRefuse, RefusePlacementInput}},
	} {
		t.Run(row.rule, func(t *testing.T) {
			if got := a.Completed(row.k, row.s, row.now); got != row.want {
				t.Errorf("got %v/%v", got.Verdict, got.Reason)
			}
			wantCounts(t, a, 1, 0, 0)
			if a.Known(k.Peer) {
				t.Error("a refused call changed what is known")
			}
		})
	}
}

func TestSecuredRefusesWhatItCannotJudge(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "a")
	for _, row := range []struct {
		rule string
		k    ConnKey
		dir  Direction
		now  time.Time
	}{
		{"an unset direction", k, DirectionUnset, at(0)},
		{"an unknown direction", k, Direction(7), at(0)},
		{"an unset key", ConnKey{}, DirectionInbound, at(0)},
		{"an unset address", ConnKey{Peer: k.Peer}, DirectionInbound, at(0)},
		{"an unset time", k, DirectionInbound, time.Time{}},
	} {
		t.Run(row.rule, func(t *testing.T) {
			if got := a.Secured(row.k, row.dir, row.now); got != (Admission{VerdictRefuse, RefuseInvalidInput}) {
				t.Errorf("got %v/%v", got.Verdict, got.Reason)
			}
			wantCounts(t, a, 0, 0, 0)
		})
	}
}

// A connection rejected after the handshake produces no Connected and no
// Disconnected. Without the sweep its entry counts against the cap for good.
func TestSweepForgetsConnectionsNeverSeenConnected(t *testing.T) {
	a := newAccounting(t)
	lost, live := key(t, 1, "a"), key(t, 2, "a")
	admit(t, a, lost, at(0), AdmitCounted)
	admit(t, a, live, at(0), AdmitCounted)
	a.Connected(live, at(0.5))
	if got := a.Sweep(at(2.999)); len(got) != 0 {
		t.Fatalf("swept before the interval: %v", got)
	}
	got := a.Sweep(at(3))
	if len(got) != 1 || got[0] != lost {
		t.Fatalf("swept %v, want only the connection never seen connected", got)
	}
	wantCounts(t, a, 1, 0, 0)
	if got := a.Sweep(at(100)); len(got) != 0 {
		t.Errorf("a connected entry was swept: %v", got)
	}
	// The cap is free again for strangers.
	admit(t, a, key(t, 3, "a"), at(100), AdmitCounted)
	admit(t, a, key(t, 4, "a"), at(100), AdmitCounted)
	admit(t, a, key(t, 5, "a"), at(100), RefuseCapReached)
}

func TestSweepAlsoFreesExemptEntries(t *testing.T) {
	a := newAccounting(t)
	m := peerKey(t, 50)
	a.SetHeld([]ident.PublicKey{m})
	admit(t, a, ConnKey{m, "a"}, at(0), AdmitKnownMember)
	a.Sweep(at(3))
	wantCounts(t, a, 0, 0, 0)
}

func TestConnectedForAnUnknownKeyIsReported(t *testing.T) {
	a := newAccounting(t)
	if a.Connected(key(t, 1, "a"), at(0)) {
		t.Error("an untracked key was reported as tracked")
	}
	k := key(t, 2, "a")
	admit(t, a, k, at(0), AdmitCounted)
	if a.Connected(k, time.Time{}) {
		t.Error("an unset time was accepted")
	}
}

func TestOverdueUnauthenticatedAfterTheExchangeDeadline(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "a")
	admit(t, a, k, at(0), AdmitCounted)
	a.Connected(k, at(1))
	if got := a.Overdue(at(5.999)); len(got) != 0 {
		t.Fatalf("overdue before the deadline: %v", got)
	}
	if got := a.Overdue(at(6)); len(got) != 1 || got[0] != k {
		t.Fatalf("overdue at the deadline: %v", got)
	}
	if got := a.Overdue(at(6)); len(got) != 1 {
		t.Errorf("Overdue changed the state: %v", got)
	}
	a.Disconnected(k)
	if got := a.Overdue(at(60)); len(got) != 0 {
		t.Errorf("a released connection is still overdue: %v", got)
	}
}

func TestOverdueCountsFromTheConnectionNotFromTheHandshake(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "a")
	admit(t, a, k, at(0), AdmitCounted)
	if got := a.Overdue(at(60)); len(got) != 0 {
		t.Errorf("a connection never seen connected has no deadline of its own (the sweep handles it): %v", got)
	}
	a.Connected(k, at(10))
	a.Connected(k, at(12)) // a second report does not restart the clock
	if got := a.Overdue(at(15)); len(got) != 1 {
		t.Errorf("deadline moved by a second Connected: %v", got)
	}
}

func TestOverdueKnownMembersHaveTheDeadlineToo(t *testing.T) {
	a := newAccounting(t)
	m := ConnKey{peerKey(t, 50), "a"}
	a.SetHeld([]ident.PublicKey{m.Peer})
	admit(t, a, m, at(0), AdmitKnownMember)
	a.Connected(m, at(0))
	if got := a.Overdue(at(5)); len(got) != 1 {
		t.Errorf("an exempt connection that does not exchange stays open: %v", got)
	}
}

func TestOverdueNonMemberAfterItsLifetime(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "a")
	admit(t, a, k, at(0), AdmitCounted)
	a.Connected(k, at(0))
	a.Completed(k, StateNonMemberNode, at(2))
	if got := a.Overdue(at(39.999)); len(got) != 0 {
		t.Fatalf("a non-member is overdue before its lifetime, counted from the connection: %v", got)
	}
	if got := a.Overdue(at(40)); len(got) != 1 || got[0] != k {
		t.Fatalf("overdue at the lifetime: %v", got)
	}
}

func TestAMemberIsNeverOverdue(t *testing.T) {
	a := newAccounting(t)
	k := key(t, 1, "a")
	admit(t, a, k, at(0), AdmitCounted)
	a.Connected(k, at(0))
	a.Completed(k, StateMember, at(1))
	if got := a.Overdue(at(1000)); len(got) != 0 {
		t.Errorf("%v", got)
	}
}

func TestOverdueAndSweepComeInAFixedOrder(t *testing.T) {
	for run := 0; run < 5; run++ {
		a := newAccounting(t)
		for _, n := range []byte{3, 1, 2} {
			k := key(t, n, fmt.Sprintf("addr%d", n))
			admit(t, a, k, at(0), AdmitCounted)
			a.Connected(k, at(0))
		}
		got := a.Overdue(at(5))
		if len(got) != 3 {
			t.Fatalf("%v", got)
		}
		for i := 1; i < len(got); i++ {
			if !got[i-1].less(got[i]) {
				t.Fatalf("not in ascending order: %v", got)
			}
		}
	}
}

func TestSetHeldReplacesTheHeldMembersAndKeepsTheLearnedOnes(t *testing.T) {
	a := newAccounting(t)
	learned, h1, h2 := key(t, 1, "a"), peerKey(t, 2), peerKey(t, 3)
	admit(t, a, learned, at(0), AdmitCounted)
	a.Completed(learned, StateMember, at(1))
	a.SetHeld([]ident.PublicKey{h1})
	for p, want := range map[ident.PublicKey]bool{learned.Peer: true, h1: true, h2: false} {
		if a.Known(p) != want {
			t.Errorf("after the first SetHeld: Known = %v, want %v", a.Known(p), want)
		}
	}
	a.SetHeld([]ident.PublicKey{h2})
	for p, want := range map[ident.PublicKey]bool{learned.Peer: true, h1: false, h2: true} {
		if a.Known(p) != want {
			t.Errorf("after the second SetHeld: Known = %v, want %v", a.Known(p), want)
		}
	}
	a.SetHeld(nil)
	if a.Known(h2) || !a.Known(learned.Peer) {
		t.Error("SetHeld(nil) must drop the held members only")
	}
}

func TestSetHeldIgnoresTheZeroKey(t *testing.T) {
	a := newAccounting(t)
	a.SetHeld([]ident.PublicKey{{}})
	if a.Known(ident.PublicKey{}) {
		t.Error("the zero key is known")
	}
}
