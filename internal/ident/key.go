package ident

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"math/big"
)

// Algorithm tags a public key with its signature algorithm. The numeric values
// are part of the identifier derivation and of the record wire format, so they
// never change meaning once assigned.
type Algorithm uint8

const (
	// AlgorithmNone is the zero value: no algorithm. It is never valid.
	AlgorithmNone Algorithm = 0
	// AlgorithmEd25519 is Ed25519 with the 32-byte canonical point encoding.
	AlgorithmEd25519 Algorithm = 1
	// AlgorithmP256 is reserved for hardware-backed P-256 keys. No code accepts
	// it yet: NewPublicKey rejects it, so a record that carries it fails
	// closed instead of being read as some other algorithm.
	AlgorithmP256 Algorithm = 2
)

// Ed25519KeyLen is the length of an Ed25519 public key.
const Ed25519KeyLen = ed25519.PublicKeySize

// Errors returned by NewPublicKey and by the derivations. Match them with
// errors.Is.
var (
	// ErrAlgorithm reports an algorithm tag that is unknown, reserved or unset.
	ErrAlgorithm = errors.New("unsupported key algorithm")
	// ErrKeyLength reports a key of the wrong length for its algorithm.
	ErrKeyLength = errors.New("wrong key length")
	// ErrKeyNotCanonical reports an encoding that is not the unique canonical
	// form of a curve point, or a point that is not on the curve at all.
	ErrKeyNotCanonical = errors.New("non-canonical or invalid public key")
	// ErrKeySmallOrder reports a point of small order. Such a key verifies
	// signatures for many messages and is never a legitimate identity.
	ErrKeySmallOrder = errors.New("small-order public key")
)

// PublicKey is a validated public key together with its algorithm tag. It is
// comparable and usable as a map key; the zero value means "unset" and is never
// valid. The only way to obtain a non-zero PublicKey is NewPublicKey, so every
// non-zero value is in its unique canonical form and has passed the checks for
// its algorithm.
//
// The type lives here, and not in the record package, because identifier
// derivation takes a PublicKey and ident must not depend on record. The
// derivation is only well defined for canonical keys, which makes the
// validation part of what an identifier means.
type PublicKey struct {
	alg Algorithm
	b   [Ed25519KeyLen]byte
}

// NewPublicKey validates b as a public key of algorithm alg. Only
// AlgorithmEd25519 is accepted. The encoding must be the canonical encoding of
// a point on the curve (a second encoding of the same key would yield a second
// identifier) and the point must not have small order.
func NewPublicKey(alg Algorithm, b []byte) (PublicKey, error) {
	if alg != AlgorithmEd25519 {
		return PublicKey{}, fmt.Errorf("public key: %w: tag %d", ErrAlgorithm, alg)
	}
	if len(b) != Ed25519KeyLen {
		return PublicKey{}, fmt.Errorf("public key: %w: got %d bytes, want %d", ErrKeyLength, len(b), Ed25519KeyLen)
	}
	if err := checkEd25519(b); err != nil {
		return PublicKey{}, fmt.Errorf("public key: %w", err)
	}
	k := PublicKey{alg: alg}
	copy(k.b[:], b)
	return k, nil
}

// Algorithm returns the key's algorithm tag; AlgorithmNone for the zero value.
func (k PublicKey) Algorithm() Algorithm { return k.alg }

// Bytes returns a copy of the canonical key bytes; nil for the zero value.
func (k PublicKey) Bytes() []byte {
	if k.IsZero() {
		return nil
	}
	return append([]byte(nil), k.b[:]...)
}

// IsZero reports whether k is the unset zero value.
func (k PublicKey) IsZero() bool { return k.alg == AlgorithmNone }

// Verify reports whether sig is a valid signature of msg under k. It is false
// for the zero key and for a signature that is not in canonical form (the
// scalar part must be reduced), so a third party who holds a valid signature
// cannot derive a second valid encoding of it. That is all it promises about
// uniqueness: the signer can produce several different valid signatures of one
// message (a different nonce gives a different R), so nothing may identify a
// signed statement by its signature; records are identified by their digest,
// which does not cover signatures.
func (k PublicKey) Verify(msg, sig []byte) bool {
	if k.alg != AlgorithmEd25519 || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(k.b[:]), msg, sig)
}

// Ed25519 point arithmetic is only needed to decode a point, so it is done with
// math/big: the inputs are public, speed does not matter, and the standard
// library does not expose its stricter decoder.
var (
	fieldP = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
	// curveD = -121665/121666 mod p, the twisted Edwards constant.
	curveD = func() *big.Int {
		inv := new(big.Int).ModInverse(big.NewInt(121666), fieldP)
		d := new(big.Int).Mul(big.NewInt(-121665), inv)
		return d.Mod(d, fieldP)
	}()
	// sqrtMinusOne = 2^((p-1)/4) mod p.
	sqrtMinusOne = new(big.Int).Exp(big.NewInt(2), new(big.Int).Rsh(new(big.Int).Sub(fieldP, big.NewInt(1)), 2), fieldP)
	// sqrtExponent = (p+3)/8, the square-root exponent for p = 5 mod 8.
	sqrtExponent = new(big.Int).Rsh(new(big.Int).Add(fieldP, big.NewInt(3)), 3)
)

// smallOrderEncodings are the canonical encodings of the eight points of the
// torsion subgroup (orders 1, 2, 4, 8). Non-canonical encodings of the same
// points are rejected earlier, by the canonical check.
var smallOrderEncodings = [8][Ed25519KeyLen]byte{
	mustKey("0000000000000000000000000000000000000000000000000000000000000000"),
	mustKey("0000000000000000000000000000000000000000000000000000000000000080"),
	mustKey("0100000000000000000000000000000000000000000000000000000000000000"),
	mustKey("26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05"),
	mustKey("26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85"),
	mustKey("c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a"),
	mustKey("c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa"),
	mustKey("ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f"),
}

// checkEd25519 implements the decoding rules of RFC 8032 section 5.1.3 for a
// public key: y must be reduced, the point must be on the curve, and the
// encoding of x = 0 must not carry the sign bit. A key that passes has exactly
// one valid encoding. Small-order points are refused last.
//
// Mixed-order points (a point of the prime-order subgroup plus a non-zero point
// of the small torsion subgroup) pass: they are valid curve points with a
// canonical encoding, and rejecting them would take a prime-order subgroup check
// that costs a scalar multiplication per key for no gain. One secret scalar can
// therefore stand behind up to eight different public keys, and so behind eight
// identifiers. That gives nobody anything: an attacker can create as many
// unrelated identities as they like by generating keys, a signature made for one
// of the eight keys is not valid for another (the challenge hash covers the
// public key's bytes), and a third party can neither
// forge a signature nor produce a second encoding of one (the scalar of a
// signature is checked to be reduced).
func checkEd25519(b []byte) error {
	var le [Ed25519KeyLen]byte
	copy(le[:], b)
	sign := le[31] >> 7
	le[31] &= 0x7f
	y := new(big.Int).SetBytes(reverse(le[:]))
	if y.Cmp(fieldP) >= 0 {
		return ErrKeyNotCanonical
	}
	// x^2 = (y^2 - 1) / (d*y^2 + 1)
	y2 := new(big.Int).Mul(y, y)
	y2.Mod(y2, fieldP)
	u := new(big.Int).Sub(y2, big.NewInt(1))
	u.Mod(u, fieldP)
	v := new(big.Int).Mul(curveD, y2)
	v.Add(v, big.NewInt(1))
	v.Mod(v, fieldP)
	// v is never zero: v = 0 would need y^2 = -1/d, and -1/d is not a square
	// modulo p (d is not a square, -1 is a square because p = 1 mod 4, and the
	// inverse of a non-square is a non-square). ModInverse therefore always
	// succeeds; a test checks the claim.
	vInv := new(big.Int).ModInverse(v, fieldP)
	x2 := new(big.Int).Mul(u, vInv)
	x2.Mod(x2, fieldP)
	x := new(big.Int).Exp(x2, sqrtExponent, fieldP)
	check := new(big.Int).Mul(x, x)
	check.Mod(check, fieldP)
	if check.Cmp(x2) != 0 {
		x.Mul(x, sqrtMinusOne)
		x.Mod(x, fieldP)
		check.Mul(x, x)
		check.Mod(check, fieldP)
		if check.Cmp(x2) != 0 {
			return ErrKeyNotCanonical
		}
	}
	if x.Sign() == 0 && sign == 1 {
		return ErrKeyNotCanonical
	}
	for i := range smallOrderEncodings {
		if string(b) == string(smallOrderEncodings[i][:]) {
			return ErrKeySmallOrder
		}
	}
	return nil
}

func reverse(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[len(b)-1-i] = b[i]
	}
	return out
}

func mustKey(hexStr string) [Ed25519KeyLen]byte {
	var out [Ed25519KeyLen]byte
	if len(hexStr) != 2*Ed25519KeyLen {
		panic("ident: bad built-in key constant")
	}
	for i := range out {
		out[i] = hexNibble(hexStr[2*i])<<4 | hexNibble(hexStr[2*i+1])
	}
	return out
}

func hexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	panic("ident: bad hex digit in built-in constant")
}
