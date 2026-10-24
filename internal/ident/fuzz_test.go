package ident

import (
	"bytes"
	"testing"
)

// Whatever parses must print back to the identical text, and a parser never
// panics.
func FuzzParseID(f *testing.F) {
	for _, s := range []string{vecNode, vecPerson, vecEndpoint, vecEndpointSystem, vecNodeZero, vecNodeBech32, vecNodePadBits, vecPersonOverNodeBytes, "", "node1", "A1LQFN3A"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if id, err := ParseNodeID(s); err == nil && (id.String() != s || id.IsZero()) {
			t.Fatalf("node %q re-encodes as %q", s, id.String())
		}
		if id, err := ParsePersonID(s); err == nil && (id.String() != s || id.IsZero()) {
			t.Fatalf("person %q re-encodes as %q", s, id.String())
		}
		if id, err := ParseEndpointID(s); err == nil && (id.String() != s || id.IsZero()) {
			t.Fatalf("endpoint %q re-encodes as %q", s, id.String())
		}
	})
}

func FuzzParseAddress(f *testing.F) {
	f.Add(vecNode + "/" + vecEndpoint)
	f.Add(vecNode + "/" + vecEndpointSystem)
	f.Add("/")
	f.Add(vecNode + "/" + vecEndpoint + "/")
	f.Fuzz(func(t *testing.T, s string) {
		if a, err := ParseAddress(s); err == nil && (a.String() != s || a.IsZero()) {
			t.Fatalf("address %q re-encodes as %q", s, a.String())
		}
	})
}

func FuzzDecodeBech32m(f *testing.F) {
	f.Add(vecNode)
	f.Add("a1lqfn3a")
	f.Fuzz(func(t *testing.T, s string) {
		hrp, data, err := decodeBech32m(s)
		if err == nil && encodeBech32m(hrp, data) != s {
			t.Fatalf("%q does not re-encode to itself", s)
		}
	})
}

// Whatever is accepted as a key keeps its bytes, has an identifier and never
// panics when asked to verify.
func FuzzNewPublicKey(f *testing.F) {
	for _, s := range []string{rfc1Pub, rfc2Pub, rfc3Pub} {
		f.Add(mustKeyBytes(s))
	}
	for _, e := range smallOrderEncodings {
		f.Add(e[:])
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		k, err := NewPublicKey(AlgorithmEd25519, b)
		if err != nil {
			return
		}
		if !bytes.Equal(k.Bytes(), b) {
			t.Fatalf("accepted key changed: %x -> %x", b, k.Bytes())
		}
		if _, err := NodeIDFromKey(k); err != nil {
			t.Fatal(err)
		}
		k.Verify(b, make([]byte, 64))
	})
}

func mustKeyBytes(s string) []byte {
	k := mustKey(s)
	return k[:]
}
