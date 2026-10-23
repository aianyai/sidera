package revocation

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// RFC 8032 section 7.1 test seeds. Their public keys and the identifiers
// derived from them appear in the golden vectors.
var rfcSeeds = map[int]string{
	1: "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60",
	2: "4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb",
	3: "c5aa8df43f9f837bedb7442f31dcb7b166d38535076f094b85ce3a2e0b4458f7",
}

func mustHexBytes(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// rfcSigner returns the signer for RFC 8032 test key n (1, 2 or 3).
func rfcSigner(t testing.TB, n int) record.Signer {
	t.Helper()
	s, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(mustHexBytes(t, rfcSeeds[n])))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// seedSigner returns a deterministic signer that is not one of the RFC keys.
func seedSigner(t testing.TB, n byte) record.Signer {
	t.Helper()
	s, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{n}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func nodeOf(t testing.TB, s record.Signer) ident.NodeID {
	t.Helper()
	id, err := ident.NodeIDFromKey(s.Public())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func personOf(t testing.TB, s record.Signer) ident.PersonID {
	t.Helper()
	id, err := ident.PersonIDFromKey(s.Public())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

var (
	epoch   = time.Unix(1700000000, 0).UTC()
	endTime = time.Unix(1702592000, 0).UTC()
)

// networkCertificate seals a network-key certificate for the node of identity,
// naming network, and returns it verified.
func networkCertificate(t testing.TB, identity, network record.Signer, serial uint64, notBefore, notAfter time.Time) (record.Record, record.Verified[record.NetworkKeyCertificate]) {
	t.Helper()
	c := record.NetworkKeyCertificate{
		Node: nodeOf(t, identity), NetworkKey: network.Public(), Serial: serial,
		NotBefore: notBefore, NotAfter: notAfter,
	}
	r, err := record.Seal(record.NetworkKeyCertificateDescriptor, c, identity)
	if err != nil {
		t.Fatal(err)
	}
	v, err := record.Verify(record.NetworkKeyCertificateDescriptor, r)
	if err != nil {
		t.Fatal(err)
	}
	return r, v
}

// deviceCertificate seals a device certificate for the person of root and returns
// it verified.
func deviceCertificate(t testing.TB, root, device record.Signer, level admin.Level) record.Verified[record.DeviceCertificate] {
	t.Helper()
	c := record.DeviceCertificate{
		Person: personOf(t, root), DeviceKey: device.Public(),
		MaxLevel: level, NotBefore: epoch, NotAfter: endTime,
	}
	r, err := record.Seal(record.DeviceCertificateDescriptor, c, root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := record.Verify(record.DeviceCertificateDescriptor, r)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// mustRevocation verifies a sealed record of any statement type; err is the
// error of the call that sealed it.
func mustRevocation(t testing.TB, r record.Record, err error) Revocation {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	rev, err := Verify(r)
	if err != nil {
		t.Fatal(err)
	}
	return rev
}

// A minimal CBOR writer for building test inputs by hand, independent of the
// encoder under test. Callers order map keys themselves, which is what lets a
// test write deliberately non-canonical input.
type cb struct{}

func (cb) head(major byte, n uint64) []byte {
	switch {
	case n < 24:
		return []byte{major<<5 | byte(n)}
	case n < 1<<8:
		return []byte{major<<5 | 24, byte(n)}
	case n < 1<<16:
		return binary.BigEndian.AppendUint16([]byte{major<<5 | 25}, uint16(n))
	case n < 1<<32:
		return binary.BigEndian.AppendUint32([]byte{major<<5 | 26}, uint32(n))
	}
	return binary.BigEndian.AppendUint64([]byte{major<<5 | 27}, n)
}
func (c cb) uint(n uint64) []byte { return c.head(0, n) }
func (c cb) bstr(b []byte) []byte { return append(c.head(2, uint64(len(b))), b...) }

// mapOf takes alternating key and value encodings, in the order given.
func (c cb) mapOf(kv ...[]byte) []byte {
	return append(c.head(5, uint64(len(kv)/2)), bytes.Join(kv, nil)...)
}
func (c cb) key(alg uint64, key []byte) []byte {
	return c.mapOf(c.uint(1), c.uint(alg), c.uint(2), c.bstr(key))
}

var cborw cb

// signedRaw builds a record of type typ over payload bytes written by hand and
// signs it with signers, skipping every rule of the statement types: it is how a
// test makes a statement that Seal would refuse.
func signedRaw(t testing.TB, typ string, payload []byte, signers ...record.Signer) record.Record {
	t.Helper()
	r, err := record.Sign(typ, payload, signers...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func hexOf(b []byte) string { return hex.EncodeToString(b) }

// indexedSigner returns a deterministic signer for index n; distinct n give
// distinct keys, none of which is a seedSigner or an RFC key.
func indexedSigner(t testing.TB, n int) record.Signer {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	binary.BigEndian.PutUint64(seed, uint64(n)+1)
	seed[ed25519.SeedSize-1] = 0xee
	s, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
