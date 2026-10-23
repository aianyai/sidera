package trust

import (
	"bytes"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// Node names used in the tests follow the usual four-node story: 1 is the
// first node (A), 2 the second (B), 3 the third (C), 4 the fourth (D) and 9 is
// the node being evicted or revoked (X).
const (
	nA byte = 1
	nB byte = 2
	nC byte = 3
	nD byte = 4
	nX byte = 9
)

var t0 = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func node(b byte) ident.NodeID {
	id, err := ident.NodeIDFromBytes(bytes.Repeat([]byte{b}, ident.NodeIDLen))
	if err != nil {
		panic(err)
	}
	return id
}

func dig(b byte) CredentialDigest {
	var d CredentialDigest
	for i := range d {
		d[i] = b
	}
	return d
}

// world builds a View step by step.
type world struct{ v View }

func newWorld(local byte) *world {
	return &world{v: View{
		Local:                      node(local),
		Settings:                   DefaultSettings(),
		LocallyRevoked:             NodeSet{},
		RevokedCredentials:         DigestSet{},
		Vouchers:                   Vouchers{},
		DeclaredAcceptsInheritance: map[ident.NodeID]bool{},
	}}
}

// edge adds credential d between a and b, valid at t0 and without expiry.
func (w *world) edge(d, a, b byte) *world {
	w.v.Credentials = append(w.v.Credentials, Credential{Digest: dig(d), A: node(a), B: node(b)})
	return w
}

func (w *world) expiring(d byte, notAfter time.Time) *world {
	for i := range w.v.Credentials {
		if w.v.Credentials[i].Digest == dig(d) {
			w.v.Credentials[i].NotAfter = notAfter
		}
	}
	return w
}

// vouch records the voucher of issuer for edge d, issued age before t0.
func (w *world) vouch(issuer, d byte, age time.Duration) *world {
	w.v.Vouchers[VoucherKey{node(issuer), dig(d)}] = Voucher{Issuer: node(issuer), Edge: dig(d), IssuedAt: t0.Add(-age), Counter: 1}
	return w
}

// receive hands the voucher of issuer for edge d, issued age before t0 with the
// given counter, to Vouchers.Accept at t0, as a node receiving it would. It
// panics if the voucher is rejected, so a row cannot silently test nothing.
func (w *world) receive(issuer, d byte, counter uint64, age time.Duration) *world {
	vo := Voucher{Issuer: node(issuer), Edge: dig(d), IssuedAt: t0.Add(-age), Counter: counter}
	if dec := w.v.Vouchers.Accept(vo, t0); !dec.Accepted {
		panic("voucher rejected: " + dec.Reason.String())
	}
	return w
}

// vouchAll gives both ends of every credential a fresh voucher.
func (w *world) vouchAll() *world {
	for _, c := range w.v.Credentials {
		w.vouch(nodeByte(c.A), digByte(c.Digest), time.Minute)
		w.vouch(nodeByte(c.B), digByte(c.Digest), time.Minute)
	}
	return w
}

func nodeByte(n ident.NodeID) byte    { return n.Bytes()[0] }
func digByte(d CredentialDigest) byte { return d[0] }

// acceptAll makes every node mentioned so far accept inheritance.
func (w *world) acceptAll() *world {
	w.v.Settings.AcceptsInheritance = true
	for _, c := range w.v.Credentials {
		w.v.DeclaredAcceptsInheritance[c.A] = true
		w.v.DeclaredAcceptsInheritance[c.B] = true
	}
	return w
}

func (w *world) declare(n byte, accepts bool) *world {
	w.v.DeclaredAcceptsInheritance[node(n)] = accepts
	return w
}

func (w *world) revokeCred(d byte) *world {
	w.v.RevokedCredentials[dig(d)] = struct{}{}
	return w
}

func (w *world) revokeNode(x byte) *world {
	w.v.LocallyRevoked[node(x)] = struct{}{}
	return w
}

// threeNodeChain is the standard chain A-B-C seen from A: both inheritance
// switches on, every voucher fresh.
func threeNodeChain() *world {
	return newWorld(nA).edge(1, nA, nB).edge(2, nB, nC).acceptAll().vouchAll()
}

func viaOf(ps []Path) []byte {
	var out []byte
	for _, p := range ps {
		out = append(out, nodeByte(p.Via))
	}
	return out
}

func equalBytes(a, b []byte) bool { return bytes.Equal(a, b) }

func mustRevoke(t *testing.T, v View, x byte) RevocationEffects {
	t.Helper()
	e, err := RevokeLocally(v, node(x))
	if err != nil {
		t.Fatal(err)
	}
	return e
}
