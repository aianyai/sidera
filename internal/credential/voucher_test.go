package credential

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

func sampleVoucher(t testing.TB) Voucher {
	t.Helper()
	return Voucher{
		Issuer:   nodeOf(t, keyA),
		Edge:     trust.CredentialDigest(mustHex(t, goldenCredentialDigest)[0:32:32]),
		IssuedAt: t0.Add(600 * time.Second),
		Counter:  7,
	}
}

var voucherNow = t0.Add(10 * time.Minute)

func TestGoldenVoucher(t *testing.T) {
	accepted := acceptedNetworkKey(t, keyA, keyNetA)
	r := mustDecode(t, goldenVoucher)
	v, err := record.Verify(VoucherDescriptor(accepted, voucherNow), r)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if v.Payload != sampleVoucher(t) {
		t.Errorf("payload = %+v", v.Payload)
	}
	if v.Digest.String() != goldenVoucherDigest {
		t.Errorf("digest = %s", v.Digest)
	}
	if v.Signers[0] != signerOf(t, keyNetA).Public() {
		t.Error("not signed by the network key")
	}
	sealed, err := record.Seal(VoucherDescriptor(accepted, voucherNow), sampleVoucher(t), signerOf(t, keyNetA))
	if err != nil {
		t.Fatal(err)
	}
	if got := mustEncode(t, sealed); !bytes.Equal(got, goldenVoucher) {
		t.Errorf("voucher differs from the vector:\n got %x\nwant %x", got, goldenVoucher)
	}
}

// The signer is bound through NetworkKeyOf, and the reason survives.
func TestVoucherSignerBinding(t *testing.T) {
	v := sampleVoucher(t)
	cert := acceptedNetworkKey(t, keyA, keyNetA)
	expired := acceptedNetworkKey(t, keyA, keyNetA)
	expired.Cert.NotAfter = t0.Add(time.Minute)
	notYet := acceptedNetworkKey(t, keyA, keyNetA)
	notYet.Cert.NotBefore = voucherNow.Add(time.Hour)
	revoked := acceptedNetworkKey(t, keyA, keyNetA)
	revoked.Revocation = record.KeyRevocationRevoked
	unchecked := acceptedNetworkKey(t, keyA, keyNetA)
	unchecked.Revocation = record.KeyRevocationUnchecked
	tests := []struct {
		name     string
		accepted record.AcceptedNetworkKey
		signer   int
		want     record.Binding
	}{
		{"the identity key signs instead of the network key", cert, keyA, record.BindingKeyMismatch},
		{"another node's network key", cert, keyNetOther, record.BindingKeyMismatch},
		{"the accepted certificate is for another node", acceptedNetworkKey(t, keyB, keyNetA), keyNetA, record.BindingNodeMismatch},
		{"no certificate is accepted", record.AcceptedNetworkKey{}, keyNetA, record.BindingNoCertificate},
		{"the certificate has expired", expired, keyNetA, record.BindingExpired},
		{"the certificate is not valid yet", notYet, keyNetA, record.BindingNotYetValid},
		{"the certificate was revoked", revoked, keyNetA, record.BindingRevoked},
		{"the revocation records were never looked up", unchecked, keyNetA, record.BindingRevocationUnchecked},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Sign by hand: Seal would refuse the same signer first.
			payload, err := record.MarshalCanonical(voucherWire{Issuer: v.Issuer.Bytes(), Edge: v.Edge[:], IssuedAt: unixT0 + 600, Counter: v.Counter})
			if err != nil {
				t.Fatal(err)
			}
			r, err := record.Sign(VoucherType, payload, signerOf(t, tc.signer))
			if err != nil {
				t.Fatal(err)
			}
			_, err = record.Verify(VoucherDescriptor(tc.accepted, voucherNow), r)
			requireErr(t, err, record.ErrSignerNotAllowed)
			var be *record.BindingError
			if !errors.As(err, &be) || be.Binding != tc.want {
				t.Errorf("binding reason = %v, want %v (err %v)", be, tc.want, err)
			}
		})
	}
}

func TestVoucherPayloadRules(t *testing.T) {
	good := voucherWire{
		Issuer: nodeOf(t, keyA).Bytes(), Edge: bytes.Repeat([]byte{9}, 32), IssuedAt: unixT0, Counter: 1,
	}
	tests := []struct {
		name   string
		mutate func(w *voucherWire)
		want   error
	}{
		{"valid", func(w *voucherWire) {}, nil},
		{"issuer unset", func(w *voucherWire) { w.Issuer = make([]byte, 32) }, ident.ErrZero},
		{"issuer too short", func(w *voucherWire) { w.Issuer = w.Issuer[:5] }, ident.ErrLength},
		{"edge unset", func(w *voucherWire) { w.Edge = make([]byte, 32) }, ErrIdentifier},
		{"edge too short", func(w *voucherWire) { w.Edge = w.Edge[:31] }, ErrIdentifier},
		{"edge missing", func(w *voucherWire) { w.Edge = nil }, ErrIdentifier},
		{"issued_at zero", func(w *voucherWire) { w.IssuedAt = 0 }, record.ErrTime},
		{"issued_at negative", func(w *voucherWire) { w.IssuedAt = -3 }, record.ErrTime},
		{"counter zero", func(w *voucherWire) { w.Counter = 0 }, record.ErrCounter},
	}
	accepted := acceptedNetworkKey(t, keyA, keyNetA)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := good
			tc.mutate(&w)
			payload, err := record.MarshalCanonical(w)
			if err != nil {
				t.Fatal(err)
			}
			r, err := record.Sign(VoucherType, payload, signerOf(t, keyNetA))
			if err != nil {
				t.Fatal(err)
			}
			_, err = record.Verify(VoucherDescriptor(accepted, voucherNow), r)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("rejected: %v", err)
				}
				return
			}
			requireErr(t, err, record.ErrPayload, tc.want)
		})
	}
}

func TestVoucherVerifyRejects(t *testing.T) {
	accepted := acceptedNetworkKey(t, keyA, keyNetA)
	d := VoucherDescriptor(accepted, voucherNow)
	payload := mustDecode(t, goldenVoucher).Payload
	signed := func(typ string, signers ...int) record.Record {
		var ss []record.Signer
		for _, n := range signers {
			ss = append(ss, signerOf(t, n))
		}
		r, err := record.Sign(typ, payload, ss...)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	tests := []struct {
		name string
		r    record.Record
		want error
	}{
		{"extra signature", signed(VoucherType, keyNetA, keyA), record.ErrSignerNotAllowed},
		{"wrong type string", signed("sidera/online-voucher/2", keyNetA), record.ErrType},
		{"a credential type", signed(Type, keyNetA), record.ErrType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := record.Verify(d, tc.r)
			requireErr(t, err, tc.want)
		})
	}
	t.Run("no signature", func(t *testing.T) {
		r := mustDecode(t, goldenVoucher)
		r.Signatures = nil
		if _, err := record.Verify(d, r); err == nil {
			t.Error("a voucher without a signature verified")
		}
	})
}

func verifiedVoucher(t testing.TB, v Voucher, issuerNode, netSeed int) record.Verified[Voucher] {
	t.Helper()
	d := VoucherDescriptor(acceptedNetworkKey(t, issuerNode, netSeed), voucherNow)
	r, err := record.Seal(d, v, signerOf(t, netSeed))
	if err != nil {
		t.Fatal(err)
	}
	out, err := record.Verify(d, r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCheckVoucher(t *testing.T) {
	cred := Facts(verifiedCredential(t, sampleCredential(t)))
	base := sampleVoucher(t)
	byA := verifiedVoucher(t, base, keyA, keyNetA)
	byB := verifiedVoucher(t, Voucher{Issuer: nodeOf(t, keyB), Edge: base.Edge, IssuedAt: base.IssuedAt, Counter: 1}, keyB, keyNetB)
	byOther := verifiedVoucher(t, Voucher{Issuer: nodeOf(t, keyOther), Edge: base.Edge, IssuedAt: base.IssuedAt, Counter: 1}, keyOther, keyNetOther)
	otherEdge := base
	otherEdge.Edge[0] ^= 1
	tests := []struct {
		name string
		v    record.Verified[Voucher]
		cred trust.Credential
		want VoucherReason
	}{
		{"issuer is end b of the credential", byA, cred, VoucherOK},
		{"issuer is end a of the credential", byB, cred, VoucherOK},
		{"issuer is not an end", byOther, cred, VoucherIssuerNotAnEnd},
		{"voucher names another edge", verifiedVoucher(t, otherEdge, keyA, keyNetA), cred, VoucherWrongEdge},
		{"credential digest unset", byA, trust.Credential{A: cred.A, B: cred.B}, VoucherInvalid},
		{"zero verified voucher", record.Verified[Voucher]{}, trust.Credential{}, VoucherInvalid},
		{"zero verified voucher with a credential", record.Verified[Voucher]{}, cred, VoucherInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := CheckVoucher(tc.v, tc.cred)
			if reason != tc.want {
				t.Fatalf("reason = %v, want %v", reason, tc.want)
			}
			if reason != VoucherOK && got != (trust.Voucher{}) {
				t.Errorf("a refused voucher returned %+v", got)
			}
			if reason == VoucherOK {
				p := tc.v.Payload
				if want := (trust.Voucher{Issuer: p.Issuer, Edge: p.Edge, IssuedAt: p.IssuedAt, Counter: p.Counter}); got != want {
					t.Errorf("voucher = %+v, want %+v", got, want)
				}
			}
		})
	}
}

// The issuer is read before verification, only to choose the context. It is
// whatever the sender wrote, so a forged record yields it too.
func TestVoucherIssuerReadsTheClaimedIssuer(t *testing.T) {
	got, err := VoucherIssuer(mustDecode(t, goldenVoucher))
	if err != nil || got != nodeOf(t, keyA) {
		t.Fatalf("VoucherIssuer = %v, %v", got, err)
	}
	forged, err := record.Sign(VoucherType, mustDecode(t, goldenVoucher).Payload, signerOf(t, keyNetOther))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := VoucherIssuer(forged); err != nil || got != nodeOf(t, keyA) {
		t.Errorf("a forged voucher claims %v, %v", got, err)
	}
	if _, err := record.Verify(VoucherDescriptor(acceptedNetworkKey(t, keyA, keyNetA), voucherNow), forged); err == nil {
		t.Error("the forged voucher verifies")
	}
}

func TestVoucherIssuerRefuses(t *testing.T) {
	_, err := VoucherIssuer(mustDecode(t, goldenRevocation))
	requireErr(t, err, record.ErrType)
	bad := mustDecode(t, goldenVoucher)
	bad.Payload = append([]byte{}, bad.Payload...)
	bad.Payload[0] ^= 0xff
	if _, err := VoucherIssuer(bad); err == nil {
		t.Error("a payload that does not decode was read")
	}
}
