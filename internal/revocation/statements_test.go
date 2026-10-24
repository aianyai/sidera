package revocation

import (
	"bytes"
	"errors"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// goldenCase pairs a golden record with the Revocation it must verify to and
// the call that issues the same record from the keys.
type goldenCase struct {
	name    string
	bytes   []byte
	payload string
	digest  string
	signed  string
	kind    Kind
	issue   func(t *testing.T) (record.Record, error)
}

func goldenCases() []goldenCase {
	return []goldenCase{
		{"node self-revocation", goldenNodeSelfRevocation, goldenNodeSelfRevocationPayload, goldenNodeSelfRevocationDigest, goldenNodeSelfRevocationSignedBytes, KindNodeSelf,
			func(t *testing.T) (record.Record, error) { return IssueNodeSelfRevocation(rfcSigner(t, 1)) }},
		{"person self-revocation", goldenPersonSelfRevocation, goldenPersonSelfRevocationPayload, goldenPersonSelfRevocationDigest, goldenPersonSelfRevocationSignedBytes, KindPersonSelf,
			func(t *testing.T) (record.Record, error) { return IssuePersonSelfRevocation(rfcSigner(t, 3)) }},
		{"network-key certificate revocation", goldenNetworkKeyCertificateRevocation, goldenNetworkKeyCertificateRevocationPayload, goldenNetworkKeyCertificateRevocationDigest, goldenNetworkKeyCertificateRevocationSignedBytes, KindNetworkKeyCertificate,
			func(t *testing.T) (record.Record, error) {
				_, cert := goldenCertificate(t)
				return IssueNetworkKeyCertificateRevocation(cert, rfcSigner(t, 1))
			}},
		{"device key revocation", goldenDeviceKeyRevocation, goldenDeviceKeyRevocationPayload, goldenDeviceKeyRevocationDigest, goldenDeviceKeyRevocationSignedBytes, KindDeviceKey,
			func(t *testing.T) (record.Record, error) {
				return IssueDeviceKeyRevocation(rfcSigner(t, 3), rfcSigner(t, 2).Public())
			}},
	}
}

// goldenCertificate is the network-key certificate whose digest the certificate
// revocation vector names: node 1, network key 2, serial 1, the standard window.
func goldenCertificate(t testing.TB) (record.Record, record.Verified[record.NetworkKeyCertificate]) {
	return networkCertificate(t, rfcSigner(t, 1), rfcSigner(t, 2), 1, epoch, endTime)
}

// Rule: the exact bytes of every statement are fixed by the format, and
// signatures and digests computed independently verify.
func TestGoldenStatements(t *testing.T) {
	for _, g := range goldenCases() {
		t.Run(g.name, func(t *testing.T) {
			rec, err := record.Decode(g.bytes, record.DefaultMaxInput)
			if err != nil {
				t.Fatal(err)
			}
			if got := hexOf(rec.Payload); got != g.payload {
				t.Fatalf("payload %s, want %s", got, g.payload)
			}
			if got := hexOf(record.SignedBytes(rec.Type, rec.Payload)); got != g.signed {
				t.Fatalf("signed bytes %s, want %s", got, g.signed)
			}
			if got := rec.Digest().String(); got != g.digest {
				t.Fatalf("digest %s, want %s", got, g.digest)
			}
			rev, err := Verify(rec)
			if err != nil {
				t.Fatal(err)
			}
			if rev.Kind() != g.kind || rev.Digest().String() != g.digest {
				t.Fatalf("revocation %v %s", rev.Kind(), rev.Digest())
			}
			// The issue functions write exactly these bytes (Ed25519 signatures
			// are deterministic).
			issued, err := g.issue(t)
			if err != nil {
				t.Fatal(err)
			}
			enc, err := issued.Encode()
			if err != nil || !bytes.Equal(enc, g.bytes) {
				t.Fatalf("issued record differs from the golden bytes: %v", err)
			}
			again, err := rec.Encode()
			if err != nil || !bytes.Equal(again, g.bytes) {
				t.Fatalf("decoded record does not re-encode identically: %v", err)
			}
		})
	}
}

// The certificate named by the certificate-revocation vector is the one built
// from the stated fields: its digest is the one written into the vector.
func TestGoldenCertificateDigest(t *testing.T) {
	_, cert := goldenCertificate(t)
	want := "e6bdc451ac725c2be0632bcce2f18e30c5a0c3fe43265832adab4adb64ad9531"
	if cert.Digest().String() != want {
		t.Fatalf("digest %s, want %s", cert.Digest(), want)
	}
}

func TestGoldenAccessors(t *testing.T) {
	node1, person3 := nodeOf(t, rfcSigner(t, 1)), personOf(t, rfcSigner(t, 3))
	row := func(name string, r Revocation, kind Kind, node ident.NodeID, person ident.PersonID) {
		t.Run(name, func(t *testing.T) {
			if r.Kind() != kind || r.Node() != node || r.Person() != person {
				t.Fatalf("got %v node %v person %v", r.Kind(), r.Node(), r.Person())
			}
		})
	}
	verify := func(b []byte) Revocation {
		rec, err := record.Decode(b, record.DefaultMaxInput)
		if err != nil {
			t.Fatal(err)
		}
		r, err := Verify(rec)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	row("a node self-revocation names its node only", verify(goldenNodeSelfRevocation), KindNodeSelf, node1, ident.PersonID{})
	row("a person self-revocation names its person only", verify(goldenPersonSelfRevocation), KindPersonSelf, ident.NodeID{}, person3)
	row("a certificate revocation names its node only", verify(goldenNetworkKeyCertificateRevocation), KindNetworkKeyCertificate, node1, ident.PersonID{})
	row("a device revocation names its person only", verify(goldenDeviceKeyRevocation), KindDeviceKey, ident.NodeID{}, person3)
}

// Statements of every type are sealed only by the key they name, and a
// statement whose signer is not that key is refused at verification too.
func TestSignerRules(t *testing.T) {
	k1, k2, k3 := rfcSigner(t, 1), rfcSigner(t, 2), rfcSigner(t, 3)
	node1, person3 := nodeOf(t, k1), personOf(t, k3)
	_, cert := goldenCertificate(t)
	certDigest := cert.Digest()
	certPayload := cborw.mapOf(cborw.uint(1), cborw.bstr(node1.Bytes()), cborw.uint(2), cborw.uint(1), cborw.uint(3), cborw.bstr(certDigest[:]))
	nodePayload := cborw.mapOf(cborw.uint(1), cborw.bstr(node1.Bytes()))
	personPayload := cborw.mapOf(cborw.uint(1), cborw.bstr(person3.Bytes()))
	devicePayload := cborw.mapOf(cborw.uint(1), cborw.bstr(person3.Bytes()), cborw.uint(2), cborw.key(1, k2.Public().Bytes()))

	third := seedSigner(t, 9)
	rows := []struct {
		rule    string
		typ     string
		payload []byte
		signer  record.Signer
	}{
		{"a third party cannot sign a node self-revocation", NodeSelfRevocationType, nodePayload, third},
		{"a third party cannot sign a person self-revocation", PersonSelfRevocationType, personPayload, third},
		{"a third party cannot sign a certificate revocation", NetworkKeyCertificateRevocationType, certPayload, third},
		{"a third party cannot sign a device revocation", DeviceKeyRevocationType, devicePayload, third},
		{"the network key of the node cannot sign a certificate revocation: only the identity key can", NetworkKeyCertificateRevocationType, certPayload, k2},
		{"the device key cannot sign its own revocation", DeviceKeyRevocationType, devicePayload, k2},
		{"the node's identity key is not the person's root key: a node statement is not a person statement", PersonSelfRevocationType, personPayload, k1},
		{"the root key of a person cannot sign a node self-revocation", NodeSelfRevocationType, nodePayload, k3},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			rec := signedRaw(t, row.typ, row.payload, row.signer)
			_, err := Verify(rec)
			if !errors.Is(err, record.ErrSignerNotAllowed) {
				t.Fatalf("Verify: got %v, want ErrSignerNotAllowed", err)
			}
		})
	}
	// The same payloads are accepted from the right keys: the rows above fail
	// because of the signer and nothing else.
	for _, ok := range []struct {
		typ     string
		payload []byte
		signer  record.Signer
	}{
		{NodeSelfRevocationType, nodePayload, k1},
		{PersonSelfRevocationType, personPayload, k3},
		{NetworkKeyCertificateRevocationType, certPayload, k1},
		{DeviceKeyRevocationType, devicePayload, k3},
	} {
		if _, err := Verify(signedRaw(t, ok.typ, ok.payload, ok.signer)); err != nil {
			t.Fatalf("%s from the right key: %v", ok.typ, err)
		}
	}
}

// Rule: a revocation signed by P's root key and naming Q's person is refused,
// so P can never revoke a device of Q; and a statement by P naming a device key
// that Q also uses is P's statement about P's own certificates.
func TestDeviceRevocationAppliesOnlyToTheSignersPerson(t *testing.T) {
	p, q, device := rfcSigner(t, 3), seedSigner(t, 7), rfcSigner(t, 2)
	// P's root key signs a statement naming Q.
	named := cborw.mapOf(cborw.uint(1), cborw.bstr(personOf(t, q).Bytes()), cborw.uint(2), cborw.key(1, device.Public().Bytes()))
	if _, err := Verify(signedRaw(t, DeviceKeyRevocationType, named, p)); !errors.Is(err, record.ErrSignerNotAllowed) {
		t.Fatalf("P's root revoking Q's device: got %v, want ErrSignerNotAllowed", err)
	}
	// The issue function cannot be made to name another person: it derives the
	// person from the signer.
	rec, err := IssueDeviceKeyRevocation(p, device.Public())
	rev := mustRevocation(t, rec, err)
	if rev.Person() != personOf(t, p) {
		t.Fatalf("issued for %v, want the signer's person", rev.Person())
	}
	// Sealing a payload that names Q with P's key is refused at the signer.
	if _, err := record.Seal(DeviceKeyRevocationDescriptor, DeviceKeyRevocation{Person: personOf(t, q), DeviceKey: device.Public()}, p); !errors.Is(err, record.ErrSignerNotAllowed) {
		t.Fatalf("Seal: got %v", err)
	}
}

// Rules of the payloads, each refused at verification even when the right key
// signed them.
func TestPayloadRules(t *testing.T) {
	k1, k2, k3 := rfcSigner(t, 1), rfcSigner(t, 2), rfcSigner(t, 3)
	node1, person3 := nodeOf(t, k1), personOf(t, k3)
	zeroID := bytes.Repeat([]byte{0}, ident.NodeIDLen)
	_, cert := goldenCertificate(t)
	certDigest := cert.Digest()
	dev := cborw.key(1, k2.Public().Bytes())
	node := func(extra ...[]byte) []byte {
		return cborw.mapOf(append([][]byte{cborw.uint(1), cborw.bstr(node1.Bytes())}, extra...)...)
	}
	person := func(extra ...[]byte) []byte {
		return cborw.mapOf(append([][]byte{cborw.uint(1), cborw.bstr(person3.Bytes())}, extra...)...)
	}
	certRev := func(nodeID []byte, serial uint64, digest []byte) []byte {
		return cborw.mapOf(cborw.uint(1), cborw.bstr(nodeID), cborw.uint(2), cborw.uint(serial), cborw.uint(3), cborw.bstr(digest))
	}
	devRev := func(personID []byte, key []byte) []byte {
		return cborw.mapOf(cborw.uint(1), cborw.bstr(personID), cborw.uint(2), key)
	}
	smallOrder := cborw.key(1, append([]byte{1}, bytes.Repeat([]byte{0}, 31)...)) // the identity point
	// The root key's own public key as the device key.
	rootAsDevice := cborw.key(1, k3.Public().Bytes())

	rows := []struct {
		rule    string
		typ     string
		payload []byte
		signer  record.Signer
		want    error
	}{
		{"a node self-revocation with the zero node is refused", NodeSelfRevocationType, cborw.mapOf(cborw.uint(1), cborw.bstr(zeroID)), k1, record.ErrPayload},
		{"a node self-revocation with a short node ID is refused", NodeSelfRevocationType, cborw.mapOf(cborw.uint(1), cborw.bstr(node1.Bytes()[:31])), k1, record.ErrPayload},
		{"a node self-revocation without the node is refused", NodeSelfRevocationType, cborw.mapOf(), k1, record.ErrNotCanonical},
		{"a node self-revocation with an unknown field is refused", NodeSelfRevocationType, node(cborw.uint(2), cborw.uint(1700000000)), k1, record.ErrMalformed},
		{"an explicit zero in a field the statement does not have is refused: no optional field exists", NodeSelfRevocationType, node(cborw.uint(2), cborw.uint(0)), k1, record.ErrMalformed},
		{"a person self-revocation with the zero person is refused", PersonSelfRevocationType, cborw.mapOf(cborw.uint(1), cborw.bstr(zeroID)), k3, record.ErrPayload},
		{"a person self-revocation with an unknown field is refused", PersonSelfRevocationType, person(cborw.uint(2), cborw.uint(0)), k3, record.ErrMalformed},
		{"a certificate revocation with serial 0 is refused", NetworkKeyCertificateRevocationType, certRev(node1.Bytes(), 0, certDigest[:]), k1, record.ErrPayload},
		{"a certificate revocation with the zero digest is refused", NetworkKeyCertificateRevocationType, certRev(node1.Bytes(), 1, make([]byte, 32)), k1, record.ErrPayload},
		{"a certificate revocation with a short digest is refused", NetworkKeyCertificateRevocationType, certRev(node1.Bytes(), 1, certDigest[:31]), k1, record.ErrPayload},
		{"a certificate revocation with a long digest is refused", NetworkKeyCertificateRevocationType, certRev(node1.Bytes(), 1, append(certDigest[:], 0)), k1, record.ErrPayload},
		{"a certificate revocation with the zero node is refused", NetworkKeyCertificateRevocationType, certRev(zeroID, 1, certDigest[:]), k1, record.ErrPayload},
		{"a certificate revocation without the digest is refused", NetworkKeyCertificateRevocationType, cborw.mapOf(cborw.uint(1), cborw.bstr(node1.Bytes()), cborw.uint(2), cborw.uint(1)), k1, record.ErrNotCanonical},
		{"a certificate revocation with a non-shortest serial is refused", NetworkKeyCertificateRevocationType,
			append(append([]byte{0xa3, 0x01}, cborw.bstr(node1.Bytes())...), append([]byte{0x02, 0x18, 0x01, 0x03}, cborw.bstr(certDigest[:])...)...), k1, record.ErrNotCanonical},
		{"a certificate revocation with its keys out of order is refused", NetworkKeyCertificateRevocationType,
			append(append([]byte{0xa3, 0x02, 0x01, 0x01}, cborw.bstr(node1.Bytes())...), append([]byte{0x03}, cborw.bstr(certDigest[:])...)...), k1, record.ErrNotCanonical},
		{"a device revocation with the zero person is refused", DeviceKeyRevocationType, devRev(zeroID, dev), k3, record.ErrPayload},
		{"a device revocation without the device key is refused", DeviceKeyRevocationType, cborw.mapOf(cborw.uint(1), cborw.bstr(person3.Bytes())), k3, record.ErrNotCanonical},
		{"a device revocation with a small-order device key is refused", DeviceKeyRevocationType, devRev(person3.Bytes(), smallOrder), k3, record.ErrPayload},
		{"a device revocation with an unknown algorithm is refused", DeviceKeyRevocationType, devRev(person3.Bytes(), cborw.key(9, k2.Public().Bytes())), k3, record.ErrPayload},
		{"a device revocation naming the person's own root key is refused", DeviceKeyRevocationType, devRev(person3.Bytes(), rootAsDevice), k3, record.ErrRoleSeparation},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			_, err := Verify(signedRaw(t, row.typ, row.payload, row.signer))
			if !errors.Is(err, row.want) {
				t.Fatalf("got %v, want %v", err, row.want)
			}
		})
	}
	// Sealing refuses the same values, so this package never writes a statement
	// a receiver refuses.
	t.Run("sealing refuses the zero values too", func(t *testing.T) {
		if _, err := record.Seal(NodeSelfRevocationDescriptor, NodeSelfRevocation{}, k1); err == nil {
			t.Fatal("zero node self-revocation sealed")
		}
		if _, err := record.Seal(NetworkKeyCertificateRevocationDescriptor, NetworkKeyCertificateRevocation{Node: node1, Serial: 0, Certificate: cert.Digest()}, k1); err == nil {
			t.Fatal("serial 0 sealed")
		}
		if _, err := record.Seal(NetworkKeyCertificateRevocationDescriptor, NetworkKeyCertificateRevocation{Node: node1, Serial: 1}, k1); err == nil {
			t.Fatal("zero digest sealed")
		}
		if _, err := record.Seal(DeviceKeyRevocationDescriptor, DeviceKeyRevocation{Person: person3}, k3); err == nil {
			t.Fatal("zero device key sealed")
		}
		if _, err := IssueDeviceKeyRevocation(k3, k3.Public()); !errors.Is(err, record.ErrRoleSeparation) {
			t.Fatalf("the root key as the device key: got %v", err)
		}
	})
}

// Rule: the type string selects the statement. A record of another type, with
// the same payload shape, never verifies as a statement it was not made as.
func TestTypeStrings(t *testing.T) {
	k1, k3 := rfcSigner(t, 1), rfcSigner(t, 3)
	rec, err := IssueNodeSelfRevocation(k1)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("a descriptor refuses a record of another type", func(t *testing.T) {
		if _, err := record.Verify(PersonSelfRevocationDescriptor, rec); !errors.Is(err, record.ErrType) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("an unknown type is refused by Verify", func(t *testing.T) {
		other := rec
		other.Type = "sidera/node-self-revocation/2"
		if _, err := Verify(other); !errors.Is(err, record.ErrType) {
			t.Fatalf("got %v", err)
		}
		other.Type = record.DeviceCertificateType
		if _, err := Verify(other); !errors.Is(err, record.ErrType) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a changed type string breaks the signature", func(t *testing.T) {
		moved := rec
		moved.Type = PersonSelfRevocationType
		if _, err := record.Verify(PersonSelfRevocationDescriptor, moved); err == nil {
			t.Fatal("verified under another type")
		}
	})
	t.Run("a certificate is not a revocation", func(t *testing.T) {
		_, cert := goldenCertificate(t)
		certRec, err := record.Seal(record.NetworkKeyCertificateDescriptor, cert.Payload(), k1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(certRec); !errors.Is(err, record.ErrType) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("the same fields under two types give two digests", func(t *testing.T) {
		// A node ID and a person ID have the same shape; the person statement of
		// the same bytes is another record.
		a := record.DigestOf(NodeSelfRevocationType, rec.Payload)
		b := record.DigestOf(PersonSelfRevocationType, rec.Payload)
		if a == b {
			t.Fatal("digests equal")
		}
		if _, err := Verify(signedRaw(t, PersonSelfRevocationType, rec.Payload, k1)); !errors.Is(err, record.ErrSignerNotAllowed) {
			t.Fatalf("node bytes as a person statement: got %v", err)
		}
		_ = k3
	})
}

// Rule: nothing about a statement can change unnoticed. Every bit of every
// golden record is protected: a flipped bit makes the record fail to decode, or
// fail to verify, or verify to something else only if it hits a field that is
// not part of the meaning (there is none).
func TestEveryBitIsProtected(t *testing.T) {
	for _, g := range goldenCases() {
		t.Run(g.name, func(t *testing.T) {
			for i := range g.bytes {
				for bit := 0; bit < 8; bit++ {
					mutated := append([]byte(nil), g.bytes...)
					mutated[i] ^= 1 << bit
					rec, err := record.Decode(mutated, record.DefaultMaxInput)
					if err != nil {
						continue
					}
					if _, err := Verify(rec); err == nil {
						t.Fatalf("byte %d bit %d: a tampered record verified", i, bit)
					}
				}
			}
		})
	}
}

// A truncated record or one with extra bytes never decodes into a statement.
func TestTruncatedAndPaddedRecords(t *testing.T) {
	for _, g := range goldenCases() {
		for _, cut := range []int{1, 7, len(g.bytes) / 2, len(g.bytes) - 1} {
			if _, err := record.Decode(g.bytes[:cut], record.DefaultMaxInput); err == nil {
				t.Fatalf("%s: %d bytes decoded", g.name, cut)
			}
		}
		if _, err := record.Decode(append(append([]byte(nil), g.bytes...), 0), record.DefaultMaxInput); err == nil {
			t.Fatalf("%s: trailing byte decoded", g.name)
		}
	}
}

// A statement carries exactly one signature: its signer. A record with the
// signer twice, with an extra signer, or unsigned, is refused.
func TestSignatureShapes(t *testing.T) {
	k1, k2 := rfcSigner(t, 1), rfcSigner(t, 2)
	node1 := nodeOf(t, k1)
	payload := cborw.mapOf(cborw.uint(1), cborw.bstr(node1.Bytes()))
	t.Run("an extra signer is refused: a signature outside a slot", func(t *testing.T) {
		rec := signedRaw(t, NodeSelfRevocationType, payload, k1, k2)
		if _, err := Verify(rec); !errors.Is(err, record.ErrSignerNotAllowed) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("no signature is refused", func(t *testing.T) {
		rec := record.Record{Type: NodeSelfRevocationType, Payload: payload}
		if _, err := Verify(rec); !errors.Is(err, record.ErrSignatureCount) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a statement only a third party signed is not a revocation of anything", func(t *testing.T) {
		rec := signedRaw(t, NodeSelfRevocationType, payload, k2)
		rev, err := Verify(rec)
		if err == nil || rev != (Revocation{}) {
			t.Fatalf("got %v %v", rev, err)
		}
	})
}

// Of builds a Revocation from a verified statement only; the one Verified that
// code outside package record can write is the zero value, and it is refused.
func TestRevocationOfZeroVerifiedIsInvalid(t *testing.T) {
	set := NewSet()
	rows := []struct {
		rule string
		rev  Revocation
	}{
		{"the zero Revocation is invalid", Revocation{}},
		{"a zero node statement is invalid", Of(record.Verified[NodeSelfRevocation]{})},
		{"a zero person statement is invalid", Of(record.Verified[PersonSelfRevocation]{})},
		{"a zero certificate statement is invalid", Of(record.Verified[NetworkKeyCertificateRevocation]{})},
		{"a zero device statement is invalid", Of(record.Verified[DeviceKeyRevocation]{})},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			if err := set.Add(row.rev); !errors.Is(err, ErrInvalidRevocation) {
				t.Fatalf("Add: got %v", err)
			}
			if got := Classify(row.rev, Context{}, 0, Params{MaxForwardOnce: 5}); got != (Retention{Dropped, ReasonInvalidInput}) {
				t.Fatalf("Classify: got %v", got)
			}
		})
	}
}
