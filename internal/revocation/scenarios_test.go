package revocation

import (
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// A record signed by a network key, for the scenarios: a note that says
// nothing. It shows that a revocation reaches every record a network key signs
// through the one binding function.
type note struct{ Node ident.NodeID }

type noteWire struct {
	Node []byte `cbor:"1,keyasint"`
}

func noteDescriptor(accepted record.AcceptedNetworkKey, now time.Time) record.Descriptor[note] {
	return record.NewDescriptor("sidera/test/note/1",
		func(n note) (noteWire, error) { return noteWire{Node: n.Node.Bytes()}, nil },
		func(w noteWire) (note, error) {
			id, err := ident.NodeIDFromBytes(w.Node)
			return note{Node: id}, err
		},
		func(n note) []record.SignerSlot {
			return []record.SignerSlot{record.NetworkKeyOf(n.Node, accepted, now)}
		},
	)
}

// Rule (identity): after the old certificate's revocation, a peer that has seen
// it refuses the old network key, and the highest serial seen stays where it was.
func TestOldCertificateRevocationRefusesTheOldKey(t *testing.T) {
	identity, oldNet, newNet := rfcSigner(t, 1), rfcSigner(t, 2), seedSigner(t, 6)
	node := nodeOf(t, identity)
	_, cert1 := networkCertificate(t, identity, oldNet, 1, epoch, endTime)
	_, cert2 := networkCertificate(t, identity, newNet, 2, epoch, endTime)
	now := epoch.Add(time.Hour)
	revocation := mustRevocation(t, must(IssueNetworkKeyCertificateRevocation(cert1, identity)), nil)

	t.Run("before the revocation the old key belongs to the node", func(t *testing.T) {
		if got := record.NetworkKeyBelongs(NewSet().NetworkKey(cert1), node, oldNet.Public(), now); got != record.BindingOK {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("a peer that has seen only the revocation refuses the old key", func(t *testing.T) {
		set := NewSet()
		if err := set.Add(revocation); err != nil {
			t.Fatal(err)
		}
		got := record.NetworkKeyBelongs(set.NetworkKey(cert1), node, oldNet.Public(), now)
		if got != record.BindingRevoked {
			t.Fatalf("got %v, want revoked", got)
		}
		// A record the old key signs is refused with the specific reason.
		n := note{Node: node}
		r, err := record.Seal(noteDescriptor(NewSet().NetworkKey(cert1), now), n, oldNet)
		if err != nil {
			t.Fatal(err)
		}
		_, err = record.Verify(noteDescriptor(set.NetworkKey(cert1), now), r)
		var be *record.BindingError
		if !errors.Is(err, record.ErrSignerNotAllowed) || !errors.As(err, &be) || be.Binding != record.BindingRevoked {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("a peer that has seen the new certificate refuses the old key as a mismatch", func(t *testing.T) {
		set := addAll(t)
		if err := set.Add(revocation); err != nil {
			t.Fatal(err)
		}
		if got := record.NetworkKeyBelongs(set.NetworkKey(cert2), node, oldNet.Public(), now); got != record.BindingKeyMismatch {
			t.Fatalf("old key: got %v", got)
		}
		if got := record.NetworkKeyBelongs(set.NetworkKey(cert2), node, newNet.Public(), now); got != record.BindingOK {
			t.Fatalf("new key: got %v", got)
		}
	})

	t.Run("the highest serial stays: the old certificate cannot come back", func(t *testing.T) {
		seen := record.SerialSeenOf(cert2)
		set := NewSet()
		if err := set.Add(revocation); err != nil {
			t.Fatal(err)
		}
		// Revoking the old certificate does not touch the state; nothing in this
		// package takes it.
		if seen.Serial != 2 {
			t.Fatalf("serial seen %d", seen.Serial)
		}
		if got := record.DecideSerial(&seen, cert1); got != record.SerialStale {
			t.Fatalf("replayed old certificate: got %v, want stale", got)
		}
		// Revoke the newest certificate as well: the serial is still the highest
		// seen, so the old certificate is still stale and its key stays out.
		if err := set.Add(mustRevocation(t, must(IssueNetworkKeyCertificateRevocation(cert2, identity)), nil)); err != nil {
			t.Fatal(err)
		}
		if got := record.DecideSerial(&seen, cert1); got != record.SerialStale {
			t.Fatalf("after revoking the newest: got %v, want stale", got)
		}
		if got := record.NetworkKeyBelongs(set.NetworkKey(cert2), node, newNet.Public(), now); got != record.BindingRevoked {
			t.Fatalf("revoked newest: got %v", got)
		}
		if got := record.NetworkKeyBelongs(set.NetworkKey(cert1), node, oldNet.Public(), now); got != record.BindingRevoked {
			t.Fatalf("revoked oldest: got %v", got)
		}
	})
}

// Rule (identity): after an identity-key compromise and the self-revocation,
// trust denies the node, and the attacker cannot move trust to a new key.
func TestIdentityKeyCompromise(t *testing.T) {
	// The node N, whose identity key the attacker holds; the local node L; the
	// node M that trusts N; and a node the attacker invents.
	identity := rfcSigner(t, 1)
	n := nodeOf(t, identity)
	local, m, invented := nodeOf(t, seedSigner(t, 40)), nodeOf(t, seedSigner(t, 41)), nodeOf(t, seedSigner(t, 42))
	now := time.Unix(1700003600, 0).UTC()
	dig := func(b byte) (d trust.CredentialDigest) {
		for i := range d {
			d[i] = b
		}
		return
	}
	view := func(retired trust.NodeSet) trust.View {
		v := trust.View{
			Local:                      local,
			Settings:                   trust.DefaultSettings(),
			Retired:                    retired,
			Credentials:                []trust.Credential{{Digest: dig(1), A: local, B: n}, {Digest: dig(2), A: n, B: m}},
			Vouchers:                   trust.Vouchers{},
			DeclaredAcceptsInheritance: map[ident.NodeID]bool{local: true, n: true, m: true},
		}
		for _, c := range v.Credentials {
			for _, end := range []ident.NodeID{c.A, c.B} {
				v.Vouchers[trust.VoucherKey{Issuer: end, Edge: c.Digest}] = trust.Voucher{Issuer: end, Edge: c.Digest, IssuedAt: now.Add(-time.Minute), Counter: 1}
			}
		}
		return v
	}

	// Before the self-revocation spreads, the node and the node behind it are
	// trusted.
	if got := trust.Evaluate(view(nil), n, now); got.Outcome != trust.AllowedDirect {
		t.Fatalf("before: direct trust in N: %+v", got)
	}
	if got := trust.Evaluate(view(nil), m, now); got.Outcome != trust.AllowedInherited {
		t.Fatalf("before: inherited trust in M through N: %+v", got)
	}

	// The attacker, holding the identity key, issues a certificate for his own
	// network key. It is a valid record, and until the self-revocation is known
	// the node's peers accept his key: the window in which a leaked identity key is still believed.
	attackerNet := seedSigner(t, 99)
	_, attackerCert := networkCertificate(t, identity, attackerNet, 7, epoch, endTime)
	if got := record.NetworkKeyBelongs(NewSet().NetworkKey(attackerCert), n, attackerNet.Public(), now); got != record.BindingOK {
		t.Fatalf("before: attacker's key: %v", got)
	}

	// The owner publishes the self-revocation made when the key was made.
	set := addAll(t, must(IssueNodeSelfRevocation(identity)))
	retired := set.Retired()

	t.Run("trust denies the node", func(t *testing.T) {
		got := trust.Evaluate(view(retired), n, now)
		if got.Outcome != trust.Denied || got.Reason != trust.ReasonRetired {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("trust in nodes behind the retired node ends", func(t *testing.T) {
		got := trust.Evaluate(view(retired), m, now)
		if got.Outcome != trust.Denied {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("a certificate the attacker signs with the leaked key is revoked", func(t *testing.T) {
		got := record.NetworkKeyBelongs(set.NetworkKey(attackerCert), n, attackerNet.Public(), now)
		if got != record.BindingRevoked {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("the attacker cannot move trust to a new key", func(t *testing.T) {
		// A new identity is a new node ID with no credential with anybody: it
		// has to be paired from scratch: trust never migrates to a new identity.
		got := trust.Evaluate(view(retired), invented, now)
		if got.Outcome != trust.Denied || got.Reason == trust.ReasonRetired {
			t.Fatalf("an invented node: %+v", got)
		}
		// A fresh credential signed with the leaked key between N and the
		// invented node changes nothing for N.
		v := view(retired)
		v.Credentials = append(v.Credentials, trust.Credential{Digest: dig(3), A: n, B: invented})
		if got := trust.Evaluate(v, invented, now); got.Outcome != trust.Denied {
			t.Fatalf("inherited through the retired node: %+v", got)
		}
		if got := trust.Evaluate(v, n, now); got.Outcome != trust.Denied || got.Reason != trust.ReasonRetired {
			t.Fatalf("N with a fresh credential: %+v", got)
		}
	})
	t.Run("the node stays retired whatever is added later", func(t *testing.T) {
		later := addAll(t, must(IssueNodeSelfRevocation(identity)), must(IssueNetworkKeyCertificateRevocation(
			func() record.Verified[record.NetworkKeyCertificate] {
				_, c := networkCertificate(t, identity, attackerNet, 8, epoch, endTime)
				return c
			}(), identity)))
		if !later.NodeRetired(n) || len(later.Retired()) != 1 {
			t.Fatal("retirement changed")
		}
	})
}

// Rule (revocation): a flood of node-level revocations of unrelated nodes is
// classified forward-once and bounded by the cap; person and device revocations
// are always long-term, and so are those of related nodes, cap or not.
func TestFlood(t *testing.T) {
	const cap = 8
	p := Params{MaxForwardOnce: cap}
	related := seedSigner(t, 50)
	pairing := seedSigner(t, 52)
	ctx := Context{
		Local:              nodeOf(t, seedSigner(t, 51)),
		TrustGraph:         trust.NodeSet{nodeOf(t, related): {}},
		DeclarationSigners: trust.NodeSet{},
		PairingNodes:       trust.NodeSet{nodeOf(t, pairing): {}},
	}
	held := 0
	counts := map[Retention]int{}
	for i := 0; i < 200; i++ {
		identity := indexedSigner(t, i)
		var rev Revocation
		if i%2 == 0 {
			rev = mustRevocation(t, must(IssueNodeSelfRevocation(identity)), nil)
		} else {
			_, cert := networkCertificate(t, identity, seedSigner(t, 7), 1, epoch, endTime)
			rev = mustRevocation(t, must(IssueNetworkKeyCertificateRevocation(cert, identity)), nil)
		}
		got := Classify(rev, ctx, held, p)
		counts[got]++
		if got.Class == ForwardOnce {
			held++
		}
		if held > cap {
			t.Fatalf("held %d forward-once revocations, cap %d", held, cap)
		}
	}
	if counts[Retention{ForwardOnce, ReasonUnrelatedNode}] != cap || counts[Retention{Dropped, ReasonCapReached}] != 200-cap {
		t.Fatalf("counts %v", counts)
	}
	if counts[Retention{LongTerm, ReasonLocalNode}]+counts[Retention{LongTerm, ReasonTrustGraphNode}] != 0 {
		t.Fatalf("an unrelated revocation was kept long term: %v", counts)
	}
	// With the cap full, related and person-level revocations are still kept.
	_, relatedCert := networkCertificate(t, related, seedSigner(t, 7), 1, epoch, endTime)
	for _, c := range []struct {
		rule string
		rev  Revocation
		want Retention
	}{
		{"a related node's self-revocation", mustRevocation(t, must(IssueNodeSelfRevocation(related)), nil), Retention{LongTerm, ReasonTrustGraphNode}},
		{"a related node's certificate revocation", mustRevocation(t, must(IssueNetworkKeyCertificateRevocation(relatedCert, related)), nil), Retention{LongTerm, ReasonTrustGraphNode}},
		{"a pairing node's self-revocation", mustRevocation(t, must(IssueNodeSelfRevocation(pairing)), nil), Retention{LongTerm, ReasonPairingNode}},
		{"a person self-revocation", mustRevocation(t, must(IssuePersonSelfRevocation(seedSigner(t, 60))), nil), Retention{LongTerm, ReasonPersonLevel}},
		{"a device key revocation", mustRevocation(t, must(IssueDeviceKeyRevocation(seedSigner(t, 60), seedSigner(t, 61).Public())), nil), Retention{LongTerm, ReasonDeviceLevel}},
	} {
		t.Run("with the cap full: "+c.rule, func(t *testing.T) {
			if got := Classify(c.rev, ctx, held, p); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}
