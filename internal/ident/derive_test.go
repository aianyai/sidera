package ident

import (
	"errors"
	"testing"
)

func TestDerivationGolden(t *testing.T) {
	// Expected digests were computed with an independent implementation:
	// SHA-256 over "sidera/id/node/1" or "sidera/id/person/1", the byte 0x01,
	// then the 32 public-key bytes of the RFC 8032 test key.
	rows := []struct {
		rule   string
		pub    string
		node   string
		person string
	}{
		{"RFC 8032 test 1", rfc1Pub,
			"8bfff7394412d3b94d49843e935680113278c5fae441e989308bde061be4f382",
			"b7682ca92c60a7645e80ab9860e5bc845bbcaca1b948176f8d54c6e55298550d"},
		{"RFC 8032 test 2", rfc2Pub,
			"eb6e331ecb9a006226db8afdd19181735baf296bc86102b4fbe7e2188ddb2b89",
			"e66b6306236292998a0e7eea8ebad5cc7899f5fe2a07e5f439a30a0f82eef878"},
		{"RFC 8032 test 3", rfc3Pub,
			"e4ac6f50e5cb87d4f05f69db0639fd901095cb2f7c3969a4fac6f3e3552c4cbd",
			"69889e61c6d596f9dbd04509e9e2b3ad10995b59ea01e2ba8c36364ccc2667e2"},
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
