package keys

import (
	"crypto/ed25519"
	"fmt"
	"io"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// privateKey is an Ed25519 private key with the identifiers derived from it. It
// is held by pointer and it prints as a fixed text naming only its node: no fmt
// verb shows the key, so a log line or an error that carries one by mistake leaks
// nothing. The printing methods have value receivers, so that a copy of the key,
// which fmt would otherwise print field by field, prints as no secret either.
type privateKey struct {
	priv ed25519.PrivateKey
	pub  ident.PublicKey
	node ident.NodeID // the node the key would be the identity key of
}

func (k privateKey) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, k.String()) }
func (k privateKey) String() string             { return "keys.privateKey(" + k.node.String() + ")" }
func (k privateKey) GoString() string           { return k.String() }

// keyFromSeed builds the key of a 32-byte seed.
func keyFromSeed(seed [32]byte) (*privateKey, error) {
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub, err := ident.NewPublicKey(ident.AlgorithmEd25519, priv.Public().(ed25519.PublicKey))
	if err != nil {
		return nil, err
	}
	node, err := ident.NodeIDFromKey(pub)
	if err != nil {
		return nil, err
	}
	return &privateKey{priv: priv, pub: pub, node: node}, nil
}

// generateKey draws a seed from rd, and from nothing else. The standard library
// generators are not used: ed25519.GenerateKey(nil) reads the system source, and
// since Go 1.26 the generators of crypto/ecdh and others ignore the reader they
// are given. Reading the seed here and building the key with NewKeyFromSeed
// makes the reader the only source, so a test that injects one fixes the key.
func generateKey(rd io.Reader) (*privateKey, error) {
	var seed [32]byte
	if _, err := io.ReadFull(rd, seed[:]); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRandom, err)
	}
	return keyFromSeed(seed)
}

// seed returns the 32-byte seed the key was built from.
func (k *privateKey) seed() [32]byte {
	var s [32]byte
	copy(s[:], k.priv.Seed())
	return s
}

// signer returns the key as a record.Signer that prints as no secret.
func (k *privateKey) signer() record.Signer { return &keySigner{key: k} }

type keySigner struct{ key *privateKey }

func (s *keySigner) Public() ident.PublicKey { return s.key.pub }
func (s *keySigner) Sign(msg []byte) ([]byte, error) {
	return ed25519.Sign(s.key.priv, msg), nil
}
func (s keySigner) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, s.String()) }
func (s keySigner) String() string             { return "keys.Signer(" + s.key.node.String() + ")" }
func (s keySigner) GoString() string           { return s.String() }
