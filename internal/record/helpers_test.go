package record

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
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
func rfcSigner(t testing.TB, n int) Signer {
	t.Helper()
	s, err := NewEd25519Signer(ed25519.NewKeyFromSeed(mustHexBytes(t, rfcSeeds[n])))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func rfcPriv(t testing.TB, n int) ed25519.PrivateKey {
	t.Helper()
	return ed25519.NewKeyFromSeed(mustHexBytes(t, rfcSeeds[n]))
}

// seedSigner returns a deterministic signer that is not one of the RFC keys.
func seedSigner(t testing.TB, n byte) Signer {
	t.Helper()
	seed := bytes.Repeat([]byte{n}, ed25519.SeedSize)
	s, err := NewEd25519Signer(ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func nodeOf(t testing.TB, s Signer) ident.NodeID {
	t.Helper()
	id, err := ident.NodeIDFromKey(s.Public())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func personOf(t testing.TB, s Signer) ident.PersonID {
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
func (c cb) tstr(s string) []byte { return append(c.head(3, uint64(len(s))), s...) }
func (c cb) arr(items ...[]byte) []byte {
	return append(c.head(4, uint64(len(items))), bytes.Join(items, nil)...)
}

// mapOf takes alternating key and value encodings, in the order given.
func (c cb) mapOf(kv ...[]byte) []byte {
	return append(c.head(5, uint64(len(kv)/2)), bytes.Join(kv, nil)...)
}
func (c cb) key(alg uint64, key []byte) []byte {
	return c.mapOf(c.uint(1), c.uint(alg), c.uint(2), c.bstr(key))
}

var cborw cb

// envelope writes a record envelope with the keys in canonical order.
func envelope(typ string, payload []byte, sigItems ...[]byte) []byte {
	return cborw.mapOf(cborw.uint(1), cborw.tstr(typ), cborw.uint(2), cborw.bstr(payload), cborw.uint(3), cborw.arr(sigItems...))
}

func sigItem(pub, sig []byte) []byte {
	return cborw.mapOf(cborw.uint(1), cborw.key(1, pub), cborw.uint(2), cborw.bstr(sig))
}
