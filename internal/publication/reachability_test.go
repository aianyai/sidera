package publication

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

var (
	addr1 = []byte("/ip4/192.0.2.1/udp/4001/quic-v1")
	addr2 = []byte("/ip6/2001:db8::1/tcp/4001")
)

var reachNow = t0.Add(10 * time.Minute)

func sampleReachability(t testing.TB) ReachabilityRecord {
	t.Helper()
	r, err := NewReachabilityRecord(3, t0.Add(time.Hour), [][]byte{addr1, addr2}, []ident.NodeID{nodeOf(t, keyRoot)}, mustDecode(t, goldenNetworkKeyCertificate))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGoldenReachability(t *testing.T) {
	r := mustDecode(t, goldenReachability)
	v, err := record.Verify(ReachabilityDescriptor(acceptedA(t), reachNow), r)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	p := v.Payload()
	if p.Counter != 3 || !p.ExpiresAt.Equal(t0.Add(time.Hour)) || p.Node() != nodeOf(t, keyA) {
		t.Errorf("payload = %+v", p)
	}
	if len(p.Addresses) != 2 || !bytes.Equal(p.Addresses[0], addr1) || !bytes.Equal(p.Addresses[1], addr2) {
		t.Errorf("addresses = %q", p.Addresses)
	}
	if len(p.Relays) != 1 || p.Relays[0] != nodeOf(t, keyRoot) {
		t.Errorf("relays = %v", p.Relays)
	}
	if p.Certificate().Digest().String() != goldenNetworkKeyCertificateDigest {
		t.Errorf("embedded certificate digest = %s", p.Certificate().Digest())
	}
	if v.Digest().String() != goldenReachabilityDigest {
		t.Errorf("digest = %s", v.Digest())
	}
	if v.Signers()[0] != signerOf(t, keyNetA).Public() {
		t.Error("not signed by the network key")
	}
}

func TestGoldenReachabilityIsWhatTheCodeWrites(t *testing.T) {
	r, err := record.Seal(ReachabilityDescriptor(acceptedA(t), reachNow), sampleReachability(t), signerOf(t, keyNetA))
	if err != nil {
		t.Fatal(err)
	}
	if got := mustEncode(t, r); !bytes.Equal(got, goldenReachability) {
		t.Errorf("reachability record differs from the vector:\n got %x\nwant %x", got, goldenReachability)
	}
	if got := mustEncode(t, networkCertRecord(t, keyA, keyNetA, 1, t0, t0.Add(30*24*time.Hour))); !bytes.Equal(got, goldenNetworkKeyCertificate) {
		t.Error("the embedded certificate differs from the vector")
	}
}

// The verifier that does not hold the node's certificate reads the embedded one
// first, decides on it with the serial rule, and verifies the record against the
// result.
func TestVerifierWithoutTheCertificateStartsFromTheEmbeddedOne(t *testing.T) {
	r := mustDecode(t, goldenReachability)
	cert, err := EmbeddedNetworkKeyCertificate(r)
	if err != nil {
		t.Fatal(err)
	}
	if got := record.DecideSerial(nil, cert); got != record.SerialAccept {
		t.Fatalf("serial verdict = %v", got)
	}
	accepted := record.AcceptedNetworkKey{Cert: ptrTo(cert.Payload()), Revocation: record.KeyRevocationClear, HighestSerial: cert.Payload().Serial}
	if _, err := record.Verify(ReachabilityDescriptor(accepted, reachNow), r); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// If the verifier already holds a newer certificate, the record that embeds
	// the old one is signed by a key that no longer belongs to the node.
	newer := networkCertRecord(t, keyA, keyNetOther, 2, t0, t0.Add(30*24*time.Hour))
	nv, err := record.Verify(record.NetworkKeyCertificateDescriptor, newer)
	if err != nil {
		t.Fatal(err)
	}
	_, err = record.Verify(ReachabilityDescriptor(record.AcceptedNetworkKey{Cert: ptrTo(nv.Payload()), Revocation: record.KeyRevocationClear, HighestSerial: nv.Payload().Serial}, reachNow), r)
	var be *record.BindingError
	if !errors.As(err, &be) || be.Binding != record.BindingKeyMismatch {
		t.Fatalf("err = %v, want a key mismatch", err)
	}
	if _, err := EmbeddedNetworkKeyCertificate(mustDecode(t, goldenDeclaration)); !errors.Is(err, record.ErrType) {
		t.Errorf("a declaration is not a reachability record: %v", err)
	}
}

func TestReachabilitySignerBinding(t *testing.T) {
	expired := acceptedA(t)
	c := *expired.Cert
	c.NotAfter = reachNow
	expired.Cert = &c
	revoked := acceptedA(t)
	revoked.Revocation = record.KeyRevocationRevoked
	unchecked := acceptedA(t)
	unchecked.Revocation = record.KeyRevocationUnchecked
	tests := []struct {
		name     string
		accepted record.AcceptedNetworkKey
		signer   int
		want     record.Binding
	}{
		{"no certificate accepted", record.AcceptedNetworkKey{}, keyNetA, record.BindingNoCertificate},
		{"certificate expired", expired, keyNetA, record.BindingExpired},
		{"certificate revoked", revoked, keyNetA, record.BindingRevoked},
		{"revocation never looked up", unchecked, keyNetA, record.BindingRevocationUnchecked},
	}
	payload := mustDecode(t, goldenReachability).Payload
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			forged, err := record.Sign(ReachabilityType, payload, signerOf(t, tc.signer))
			if err != nil {
				t.Fatal(err)
			}
			_, err = record.Verify(ReachabilityDescriptor(tc.accepted, reachNow), forged)
			requireErr(t, err, record.ErrSignerNotAllowed)
			var be *record.BindingError
			if !errors.As(err, &be) || be.Binding != tc.want {
				t.Errorf("binding = %v, want %v", be, tc.want)
			}
		})
	}
}

// The key that signs a reachability record is the network key of the certificate
// it embeds, whatever certificate the verifier holds. A verifier that already
// holds a newer certificate would otherwise accept a record signed by the newer
// key while a verifier that reads the embedded certificate refuses it.
func TestReachabilityMustBeSignedByTheEmbeddedCertificateKey(t *testing.T) {
	// The record embeds the serial-1 certificate (network key keyNetA) and is
	// signed by the key of the serial-2 certificate (keyNetOther).
	serial2 := networkCertRecord(t, keyA, keyNetOther, 2, t0, t0.Add(30*24*time.Hour))
	v2, err := record.Verify(record.NetworkKeyCertificateDescriptor, serial2)
	if err != nil {
		t.Fatal(err)
	}
	signedBySerial2, err := record.Sign(ReachabilityType, mustDecode(t, goldenReachability).Payload, signerOf(t, keyNetOther))
	if err != nil {
		t.Fatal(err)
	}
	clear := func(c *record.NetworkKeyCertificate) record.AcceptedNetworkKey {
		return record.AcceptedNetworkKey{Cert: c, Revocation: record.KeyRevocationClear, HighestSerial: c.Serial}
	}
	verifiers := []struct {
		name     string
		accepted record.AcceptedNetworkKey
	}{
		{"holds the embedded serial-1 certificate", acceptedA(t)},
		{"holds the serial-2 certificate", clear(ptrTo(v2.Payload()))},
		{"holds no certificate", record.AcceptedNetworkKey{}},
	}
	for _, tc := range verifiers {
		t.Run(tc.name, func(t *testing.T) {
			_, err := record.Verify(ReachabilityDescriptor(tc.accepted, reachNow), signedBySerial2)
			requireErr(t, err, record.ErrSignerNotAllowed)
			requireErr(t, err, errNotTheEmbeddedKey)
		})
	}
	t.Run("signed by the identity key", func(t *testing.T) {
		forged, err := record.Sign(ReachabilityType, mustDecode(t, goldenReachability).Payload, signerOf(t, keyA))
		if err != nil {
			t.Fatal(err)
		}
		_, err = record.Verify(ReachabilityDescriptor(acceptedA(t), reachNow), forged)
		requireErr(t, err, record.ErrSignerNotAllowed, errNotTheEmbeddedKey)
	})
	t.Run("the embedded key still has to be the accepted key", func(t *testing.T) {
		// A verifier that holds the serial-2 certificate refuses a record signed
		// by the embedded serial-1 key, with the binding reason.
		ok, err := record.Sign(ReachabilityType, mustDecode(t, goldenReachability).Payload, signerOf(t, keyNetA))
		if err != nil {
			t.Fatal(err)
		}
		_, err = record.Verify(ReachabilityDescriptor(clear(ptrTo(v2.Payload())), reachNow), ok)
		var be *record.BindingError
		if !errors.As(err, &be) || be.Binding != record.BindingKeyMismatch {
			t.Fatalf("err = %v, want a key mismatch", err)
		}
	})
}

func goodReachabilityWire(t testing.TB) reachabilityWire {
	t.Helper()
	w, err := reachabilityToWire(sampleReachability(t))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func verifyReachabilityWire(t *testing.T, w reachabilityWire) error {
	t.Helper()
	payload, err := record.MarshalCanonical(w)
	if err != nil {
		t.Fatal(err)
	}
	r, err := record.Sign(ReachabilityType, payload, signerOf(t, keyNetA))
	if err != nil {
		t.Fatal(err)
	}
	_, err = record.Verify(ReachabilityDescriptor(acceptedA(t), reachNow), r)
	return err
}

func TestReachabilityPayloadRules(t *testing.T) {
	forgedCert, err := record.Sign(record.NetworkKeyCertificateType, mustDecode(t, goldenNetworkKeyCertificate).Payload, signerOf(t, keyOther))
	if err != nil {
		t.Fatal(err)
	}
	deviceCert, err := record.Seal(record.DeviceCertificateDescriptor, record.DeviceCertificate{
		Person: personOf(t, keyRoot), DeviceKey: signerOf(t, keyOther).Public(), MaxLevel: 2, NotBefore: t0, NotAfter: t0.Add(time.Hour),
	}, signerOf(t, keyRoot))
	if err != nil {
		t.Fatal(err)
	}
	many := func(n int, size int) [][]byte {
		var out [][]byte
		for i := 0; i < n; i++ {
			out = append(out, bytes.Repeat([]byte{byte(i + 1)}, size))
		}
		return out
	}
	relays := func(n int) [][]byte {
		var out [][]byte
		for i := 0; i < n; i++ {
			out = append(out, bytes.Repeat([]byte{byte(i + 1)}, 32))
		}
		return out
	}
	tests := []struct {
		name   string
		mutate func(w *reachabilityWire)
		want   error
	}{
		{"valid", func(w *reachabilityWire) {}, nil},
		{"no addresses and no relays", func(w *reachabilityWire) { w.Addresses, w.Relays = [][]byte{}, [][]byte{} }, nil},
		{"counter zero", func(w *reachabilityWire) { w.Counter = 0 }, record.ErrCounter},
		{"expires_at zero", func(w *reachabilityWire) { w.ExpiresAt = 0 }, record.ErrTime},
		{"expires_at negative", func(w *reachabilityWire) { w.ExpiresAt = -9 }, record.ErrTime},
		{"expires_at beyond the wire range", func(w *reachabilityWire) { w.ExpiresAt = record.MaxUnixSeconds + 1 }, record.ErrTime},
		{"certificate missing", func(w *reachabilityWire) { w.Certificate = nil }, ErrCertificate},
		{"certificate is garbage", func(w *reachabilityWire) { w.Certificate = []byte{9, 9} }, ErrCertificate},
		{"certificate not signed by the node's identity key", func(w *reachabilityWire) { w.Certificate = mustEncode(t, forgedCert) }, ErrCertificate},
		{"embedded record is not a network-key certificate", func(w *reachabilityWire) { w.Certificate = mustEncode(t, deviceCert) }, ErrCertificate},
		{"address list at the limit", func(w *reachabilityWire) { w.Addresses = many(MaxAddresses, 4) }, nil},
		{"too many addresses", func(w *reachabilityWire) { w.Addresses = many(MaxAddresses+1, 4) }, ErrAddress},
		{"empty address", func(w *reachabilityWire) { w.Addresses = [][]byte{{}} }, ErrAddress},
		{"address at the length limit", func(w *reachabilityWire) { w.Addresses = many(1, MaxAddressLen) }, nil},
		{"address over the length limit", func(w *reachabilityWire) { w.Addresses = many(1, MaxAddressLen+1) }, ErrAddress},
		{"relay list at the limit", func(w *reachabilityWire) { w.Relays = relays(MaxRelays) }, nil},
		{"too many relays", func(w *reachabilityWire) { w.Relays = relays(MaxRelays + 1) }, ErrRelay},
		{"same relay listed twice", func(w *reachabilityWire) { r := relays(2); w.Relays = [][]byte{r[0], r[1], r[0]} }, ErrRelay},
		{"relay unset", func(w *reachabilityWire) { w.Relays = [][]byte{make([]byte, 32)} }, ErrRelay},
		{"relay of the wrong length", func(w *reachabilityWire) { w.Relays = [][]byte{{1, 2, 3}} }, ErrRelay},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := goodReachabilityWire(t)
			tc.mutate(&w)
			err := verifyReachabilityWire(t, w)
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

func TestNewReachabilityRecordRefusesBadCertificates(t *testing.T) {
	forged, err := record.Sign(record.NetworkKeyCertificateType, mustDecode(t, goldenNetworkKeyCertificate).Payload, signerOf(t, keyOther))
	if err != nil {
		t.Fatal(err)
	}
	for name, cert := range map[string]record.Record{"zero record": {}, "signed by another key": forged, "a reachability record": mustDecode(t, goldenReachability)} {
		t.Run(name, func(t *testing.T) {
			_, err := NewReachabilityRecord(1, t0.Add(time.Hour), nil, nil, cert)
			requireErr(t, err, ErrCertificate)
		})
	}
}

func TestSealRefusesInvalidReachabilityRecords(t *testing.T) {
	accepted := acceptedA(t)
	tests := []struct {
		name   string
		mutate func(r *ReachabilityRecord)
		want   error
	}{
		{"counter unset", func(r *ReachabilityRecord) { r.Counter = 0 }, record.ErrCounter},
		{"no expiry", func(r *ReachabilityRecord) { r.ExpiresAt = time.Time{} }, record.ErrTime},
		{"empty address", func(r *ReachabilityRecord) { r.Addresses = [][]byte{nil} }, ErrAddress},
		{"unset relay", func(r *ReachabilityRecord) { r.Relays = []ident.NodeID{{}} }, ErrRelay},
		{"no certificate", func(r *ReachabilityRecord) { r.CertificateRecord = record.Record{} }, ErrCertificate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := sampleReachability(t)
			tc.mutate(&r)
			_, err := record.Seal(ReachabilityDescriptor(accepted, reachNow), r, signerOf(t, keyNetA))
			requireErr(t, err, tc.want)
		})
	}
}

func TestReachabilityExpiry(t *testing.T) {
	r := sampleReachability(t)
	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"long before", t0, false},
		{"one second before", r.ExpiresAt.Add(-time.Second), false},
		{"at the instant", r.ExpiresAt, true},
		{"one second after", r.ExpiresAt.Add(time.Second), true},
		{"long after", r.ExpiresAt.Add(100 * time.Hour), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.ExpiredAt(tc.now); got != tc.want {
				t.Errorf("ExpiredAt = %v, want %v", got, tc.want)
			}
		})
	}
	if !(ReachabilityRecord{}).ExpiredAt(t0) {
		t.Error("a record without an expiry time must count as expired")
	}
}

// The record verifies at a time when it has expired: expiry is the decision
// function's, so that a caller can still see why it was dropped.
func TestVerificationDoesNotJudgeExpiry(t *testing.T) {
	late := t0.Add(2 * time.Hour) // after expires_at, before the certificate ends
	v, err := record.Verify(ReachabilityDescriptor(acceptedA(t), late), mustDecode(t, goldenReachability))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !v.Payload().ExpiredAt(late) {
		t.Error("the record should be expired")
	}
	if got := DecideReachability(nil, v, late); got != VerdictExpired {
		t.Errorf("verdict = %v, want expired", got)
	}
}

func TestReachabilityVerifyRejects(t *testing.T) {
	desc := ReachabilityDescriptor(acceptedA(t), reachNow)
	payload := mustDecode(t, goldenReachability).Payload
	for name, tc := range map[string]struct {
		typ     string
		signers []int
		want    error
	}{
		"extra signature":   {ReachabilityType, []int{keyNetA, keyA}, record.ErrSignerNotAllowed},
		"wrong type string": {"sidera/reachability-record/2", []int{keyNetA}, record.ErrType},
		"a declaration":     {DeclarationType, []int{keyNetA}, record.ErrType},
	} {
		t.Run(name, func(t *testing.T) {
			var ss []record.Signer
			for _, n := range tc.signers {
				ss = append(ss, signerOf(t, n))
			}
			r, err := record.Sign(tc.typ, payload, ss...)
			if err != nil {
				t.Fatal(err)
			}
			_, err = record.Verify(desc, r)
			requireErr(t, err, tc.want)
		})
	}
}

func TestSealRefusesDuplicateRelays(t *testing.T) {
	relay := nodeOf(t, keyRoot)
	rec, err := NewReachabilityRecord(1, t0.Add(time.Hour), [][]byte{{1}}, []ident.NodeID{relay, relay}, mustDecode(t, goldenNetworkKeyCertificate))
	if err != nil {
		t.Fatal(err)
	}
	_, err = record.Seal(ReachabilityDescriptor(acceptedA(t), reachNow), rec, signerOf(t, keyNetA))
	requireErr(t, err, record.ErrPayload, ErrRelay)
}

// The accessor and the embedded record cannot disagree: the certificate is only
// ever derived from CertificateRecord.
func TestCertificateAccessorIsDerivedFromTheEmbeddedRecord(t *testing.T) {
	v, err := record.Verify(ReachabilityDescriptor(acceptedA(t), reachNow), mustDecode(t, goldenReachability))
	if err != nil {
		t.Fatal(err)
	}
	want, err := record.Verify(record.NetworkKeyCertificateDescriptor, v.Payload().CertificateRecord)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Payload().Certificate(); got.Digest() != want.Digest() || got.Payload() != want.Payload() {
		t.Errorf("Certificate() = %+v, want %+v", got, want)
	}
	if (ReachabilityRecord{}).Certificate().Digest() != (record.Digest{}) {
		t.Error("a zero record has a certificate")
	}
}

// Without a derived certificate a literal record names no signer, so it cannot be
// sealed by accident.
func TestSealRefusesRecordWithoutDerivedCertificate(t *testing.T) {
	lit := ReachabilityRecord{Counter: 1, ExpiresAt: t0.Add(time.Hour), CertificateRecord: mustDecode(t, goldenNetworkKeyCertificate)}
	if _, err := record.Seal(ReachabilityDescriptor(acceptedA(t), reachNow), lit, signerOf(t, keyNetA)); err == nil {
		t.Fatal("sealed a record whose certificate was never derived")
	}
}
