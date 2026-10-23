package ident

import (
	"errors"
	"testing"
)

func TestDerivationGolden(t *testing.T) {
	// Expected digests were computed with an independent implementation
	// (Python: hashlib and a varint written out by hand): SHA-256 of
	// 0x10 "sidera/id/node/1" 0x01 0x01 <key> or
	// 0x12 "sidera/id/person/1" 0x01 0x01 <key>, i.e. the domain behind its
	// length, then the algorithm byte and the 32 public-key bytes of the RFC 8032
	// test key, each behind its length.
	rows := []struct {
		rule   string
		pub    string
		node   string
		person string
	}{
		{"RFC 8032 test 1", rfc1Pub,
			"e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d",
			"ffd3a751b85d51d4970617e4928f15981acd22156f280c6ea519b4e6cf418239"},
		{"RFC 8032 test 2", rfc2Pub,
			"33ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc",
			"bc062e489c91ab06d18bd10617eb275bfa39f94beb7a883f74a0357f4e656b1e"},
		{"RFC 8032 test 3", rfc3Pub,
			"40a7fc60420d2a1d266e61f7613bc43e79ffaba9dc7324c2e14c1d4d55e8b99d",
			"0300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d"},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			k, err := NewPublicKey(AlgorithmEd25519, mustHex(t, r.pub))
			if err != nil {
				t.Fatal(err)
			}
			n, err := NodeIDFromKey(k)
			if err != nil || hexOf(n.Bytes()) != r.node {
				t.Fatalf("NodeIDFromKey = %x, %v; want %s", n.Bytes(), err, r.node)
			}
			p, err := PersonIDFromKey(k)
			if err != nil || hexOf(p.Bytes()) != r.person {
				t.Fatalf("PersonIDFromKey = %x, %v; want %s", p.Bytes(), err, r.person)
			}
		})
	}
}

func TestDerivationRules(t *testing.T) {
	k, _ := NewPublicKey(AlgorithmEd25519, mustHex(t, rfc1Pub))
	n, _ := NodeIDFromKey(k)
	p, _ := PersonIDFromKey(k)

	t.Run("node and person identifiers of one key differ", func(t *testing.T) {
		if hexOf(n.Bytes()) == hexOf(p.Bytes()) {
			t.Fatal("the node and person domains do not separate")
		}
	})
	t.Run("the algorithm tag is part of the input", func(t *testing.T) {
		other := k
		other.alg = AlgorithmP256 // not constructible through NewPublicKey; only the derivation input matters
		n2, err := NodeIDFromKey(other)
		if err != nil || hexOf(n2.Bytes()) == hexOf(n.Bytes()) {
			t.Fatalf("tag does not change the identifier: %v", err)
		}
	})
	t.Run("the zero key is rejected", func(t *testing.T) {
		if _, err := NodeIDFromKey(PublicKey{}); !errors.Is(err, ErrZero) {
			t.Fatalf("NodeIDFromKey error = %v", err)
		}
		if _, err := PersonIDFromKey(PublicKey{}); !errors.Is(err, ErrZero) {
			t.Fatalf("PersonIDFromKey error = %v", err)
		}
	})
	t.Run("a non-canonical second encoding of a key has no identifier", func(t *testing.T) {
		// y = 1 + p is the second encoding of the identity point; the point
		// is small order anyway, but the rule is that no second encoding of
		// any key reaches the derivation.
		if _, err := NewPublicKey(AlgorithmEd25519, mustHex(t, "eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f")); !errors.Is(err, ErrKeyNotCanonical) {
			t.Fatalf("error = %v", err)
		}
	})
}
