package credential

import (
	"bytes"
	"math/rand"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// cborHead writes the head of a CBOR item of the given major type, with the
// shortest argument. It is the test's own encoder, written apart from the
// package's, to serve as the oracle for payload bytes.
func cborHead(major byte, n uint64) []byte {
	switch {
	case n < 24:
		return []byte{major<<5 | byte(n)}
	case n < 1<<8:
		return []byte{major<<5 | 24, byte(n)}
	case n < 1<<16:
		return []byte{major<<5 | 25, byte(n >> 8), byte(n)}
	case n < 1<<32:
		return []byte{major<<5 | 26, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
	return []byte{major<<5 | 27, byte(n >> 56), byte(n >> 48), byte(n >> 40), byte(n >> 32), byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
}

func cborBytes(b []byte) []byte { return append(cborHead(2, uint64(len(b))), b...) }

// oracleCredentialPayload writes the credential payload from the format
// description: a map of integer keys 1..5 in ascending order, key 4 only when the
// credential ends, the two node IDs in ascending byte order.
func oracleCredentialPayload(x, y []byte, created, notAfter int64, nonce []byte) []byte {
	lo, hi := x, y
	if bytes.Compare(lo, hi) > 0 {
		lo, hi = hi, lo
	}
	n := 4
	if notAfter != 0 {
		n = 5
	}
	out := cborHead(5, uint64(n))
	out = append(out, 0x01)
	out = append(out, cborBytes(lo)...)
	out = append(out, 0x02)
	out = append(out, cborBytes(hi)...)
	out = append(out, 0x03)
	out = append(out, cborHead(0, uint64(created))...)
	if notAfter != 0 {
		out = append(out, 0x04)
		out = append(out, cborHead(0, uint64(notAfter))...)
	}
	out = append(out, 0x05)
	return append(out, cborBytes(nonce)...)
}

// TestCredentialPayloadAgainstAnOracle: for random inputs, in either argument
// order, the payload is the oracle's bytes, both sides compute the same bytes,
// and the record signed in the pairing order verifies with the digest the oracle
// computes.
func TestCredentialPayloadAgainstAnOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(20231023))
	cases := map[string]int{}
	for i := 0; i < 400; i++ {
		var a, b ident.NodeID
		var raw [2][32]byte
		for j := range raw {
			rng.Read(raw[j][:])
			raw[j][0] |= 1 // never all zero
		}
		a, _ = ident.NodeIDFromBytes(raw[0][:])
		b, _ = ident.NodeIDFromBytes(raw[1][:])
		var nonce Nonce
		rng.Read(nonce[:])
		nonce[0] |= 1
		// Times at every integer width of the encoding.
		widths := []int64{1 << 7, 1 << 15, 1 << 31, 1 << 36}
		created := 1 + rng.Int63n(widths[rng.Intn(len(widths))])
		var notAfter int64
		if rng.Intn(2) == 0 {
			notAfter = created + 1 + rng.Int63n(1<<32)
			cases["with end"]++
		} else {
			cases["without end"]++
		}
		var na time.Time
		if notAfter != 0 {
			na = time.Unix(notAfter, 0)
		}
		want := oracleCredentialPayload(a.Bytes(), b.Bytes(), created, notAfter, nonce[:])
		c1 := New(a, b, time.Unix(created, 0), na, nonce)
		c2 := New(b, a, time.Unix(created, 0), na, nonce)
		p1, err1 := c1.Payload()
		p2, err2 := c2.Payload()
		if err1 != nil || err2 != nil {
			t.Fatalf("payload: %v %v", err1, err2)
		}
		if !bytes.Equal(p1, want) || !bytes.Equal(p2, want) {
			t.Fatalf("case %d: payload differs from the oracle\n got %x\nwant %x", i, p1, want)
		}
		if bytes.Compare(a.Bytes(), b.Bytes()) < 0 {
			cases["initiator has the smaller ID"]++
		} else {
			cases["initiator has the larger ID"]++
		}
		d, err := c1.Digest()
		if err != nil || [32]byte(d) != independentDigest(Type, want) {
			t.Fatalf("digest differs from the oracle: %v", err)
		}
	}
	for _, k := range []string{"with end", "without end", "initiator has the smaller ID", "initiator has the larger ID"} {
		if cases[k] < 100 {
			t.Errorf("only %d cases for %q", cases[k], k)
		}
	}
}
