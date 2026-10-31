package nodeproto

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
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

// arr takes the encodings of the items.
func (c cb) arr(items ...[]byte) []byte {
	return append(c.head(4, uint64(len(items))), bytes.Join(items, nil)...)
}

// mapOf takes alternating key and value encodings, in the order given.
func (c cb) mapOf(kv ...[]byte) []byte {
	return append(c.head(5, uint64(len(kv)/2)), bytes.Join(kv, nil)...)
}

var w cb

func unhex(tb testing.TB, s string) []byte {
	tb.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

// endpoint returns the endpoint whose bytes count up from first.
func endpoint(first byte) ident.EndpointID {
	b := make([]byte, ident.EndpointIDLen)
	for i := range b {
		b[i] = first + byte(i)
	}
	id, err := ident.EndpointIDFromBytes(b)
	if err != nil {
		panic(err)
	}
	return id
}

// The two endpoints of the golden vectors and the text form of the first.
var (
	ep1     = endpoint(0x01)
	ep2     = endpoint(0x21)
	ep1Text = "ep1qypqxpq9qcrsszg2pvxq6rs0zqear7st"
)

// goldenRecord is the smallest record that has the shape of a signed one: a
// type, a payload and one signature. Its signature does not verify, which no
// test here needs: the package reads the shape of a record and verifies nothing.
var goldenRecord = w.mapOf(
	w.uint(1), w.tstr("t"),
	w.uint(2), w.bstr([]byte{1}),
	w.uint(3), w.arr(w.mapOf(
		w.uint(1), w.mapOf(w.uint(1), w.uint(1), w.uint(2), w.bstr(bytes.Repeat([]byte{0x11}, 32))),
		w.uint(2), w.bstr(bytes.Repeat([]byte{0x22}, 64)),
	)),
)

func mustRecord(tb testing.TB) record.Record {
	tb.Helper()
	r, err := record.Decode(goldenRecord, record.DefaultMaxInput)
	if err != nil {
		tb.Fatal(err)
	}
	return r
}

// Pieces the refusal tables put together.
var (
	recBytes  = w.bstr(goldenRecord)
	ep1Bytes  = w.bstr(ep1.Bytes())
	ep2Bytes  = w.bstr(ep2.Bytes())
	zeroBytes = w.bstr(make([]byte, ident.EndpointIDLen))
)

func repeatItem(item []byte, n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = item
	}
	return out
}

func distinctEndpoints(n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		b := make([]byte, ident.EndpointIDLen)
		b[0], b[1] = byte(i), byte(i>>8)
		b[15] = 1
		out[i] = w.bstr(b)
	}
	return out
}

func testSigner(tb testing.TB) record.Signer {
	tb.Helper()
	s, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize)))
	if err != nil {
		tb.Fatal(err)
	}
	return s
}
