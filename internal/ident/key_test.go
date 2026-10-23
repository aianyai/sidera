package ident

import (
	"crypto/ed25519"
	"errors"
	"math/big"
	"math/rand"
	"strings"
	"testing"
)

// RFC 8032 section 7.1 test keys (seed -> public key).
const (
	rfc1Pub = "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"
	rfc2Pub = "3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c"
	rfc3Pub = "fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025"
)

func TestNewPublicKey(t *testing.T) {
	p := "ed" + strings.Repeat("ff", 30) + "7f"
	rows := []struct {
		rule string
		alg  Algorithm
		hex  string
		want error
	}{
		{"RFC 8032 test 1 key is accepted", AlgorithmEd25519, rfc1Pub, nil},
		{"RFC 8032 test 2 key is accepted", AlgorithmEd25519, rfc2Pub, nil},
		{"RFC 8032 test 3 key is accepted", AlgorithmEd25519, rfc3Pub, nil},
		{"unset algorithm is rejected", AlgorithmNone, rfc1Pub, ErrAlgorithm},
		{"reserved P-256 tag is rejected", AlgorithmP256, rfc1Pub, ErrAlgorithm},
		{"unknown algorithm tag is rejected", 3, rfc1Pub, ErrAlgorithm},
		{"short key is rejected", AlgorithmEd25519, rfc1Pub[:62], ErrKeyLength},
		{"long key is rejected", AlgorithmEd25519, rfc1Pub + "00", ErrKeyLength},
		{"empty key is rejected", AlgorithmEd25519, "", ErrKeyLength},
		{"y equal to p (non-canonical zero) is rejected", AlgorithmEd25519, p, ErrKeyNotCanonical},
		{"y equal to p+1 (non-canonical identity) is rejected", AlgorithmEd25519, "ee" + p[2:], ErrKeyNotCanonical},
		{"x = 0 with the sign bit set is rejected", AlgorithmEd25519, "0100000000000000000000000000000000000000000000000000000000000080", ErrKeyNotCanonical},
		{"y = p-1 with the sign bit set is rejected", AlgorithmEd25519, "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", ErrKeyNotCanonical},
		{"a y with no curve point is rejected", AlgorithmEd25519, "0200000000000000000000000000000000000000000000000000000000000000", ErrKeyNotCanonical},
		{"identity point is rejected as small order", AlgorithmEd25519, "0100000000000000000000000000000000000000000000000000000000000000", ErrKeySmallOrder},
		{"order-2 point is rejected as small order", AlgorithmEd25519, "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", ErrKeySmallOrder},
		{"order-4 point is rejected as small order", AlgorithmEd25519, "0000000000000000000000000000000000000000000000000000000000000000", ErrKeySmallOrder},
		{"order-4 point with the sign bit is rejected as small order", AlgorithmEd25519, "0000000000000000000000000000000000000000000000000000000000000080", ErrKeySmallOrder},
		{"order-8 point is rejected as small order", AlgorithmEd25519, "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05", ErrKeySmallOrder},
		{"order-8 point with the sign bit is rejected as small order", AlgorithmEd25519, "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa", ErrKeySmallOrder},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			k, err := NewPublicKey(r.alg, mustHex(t, r.hex))
			if !errors.Is(err, r.want) {
				t.Fatalf("error = %v, want %v", err, r.want)
			}
			if r.want != nil && !k.IsZero() {
				t.Fatalf("a failed call returned a key")
			}
			if r.want == nil && (k.IsZero() || k.Algorithm() != AlgorithmEd25519 || hexOf(k.Bytes()) != r.hex) {
				t.Fatalf("accepted key = %v", k)
			}
		})
	}
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, v := range b {
		out = append(out, digits[v>>4], digits[v&15])
	}
	return string(out)
}

func TestPublicKeyZeroAndCopy(t *testing.T) {
	var z PublicKey
	if !z.IsZero() || z.Bytes() != nil || z.Algorithm() != AlgorithmNone {
		t.Fatal("zero key is not unset")
	}
	if z.Verify([]byte("m"), make([]byte, 64)) {
		t.Fatal("zero key verified a signature")
	}
	k, _ := NewPublicKey(AlgorithmEd25519, mustHex(t, rfc1Pub))
	b := k.Bytes()
	b[0] ^= 0xff
	if hexOf(k.Bytes()) != rfc1Pub {
		t.Fatal("Bytes returned the key's own storage")
	}
}

// RFC 8032 test 1: empty message.
func TestPublicKeyVerifyKnownAnswer(t *testing.T) {
	sig := mustHex(t, "e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e065224901555fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b")
	k, _ := NewPublicKey(AlgorithmEd25519, mustHex(t, rfc1Pub))
	rows := []struct {
		rule string
		msg  []byte
		sig  []byte
		want bool
	}{
		{"valid signature verifies", nil, sig, true},
		{"other message fails", []byte{0}, sig, false},
		{"short signature fails", nil, sig[:63], false},
		{"flipped R byte fails", nil, flip(sig, 0), false},
		{"flipped S byte fails", nil, flip(sig, 40), false},
		{"non-canonical S (S+L) fails", nil, addL(sig), false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := k.Verify(r.msg, r.sig); got != r.want {
				t.Fatalf("Verify = %v, want %v", got, r.want)
			}
		})
	}
}

func flip(b []byte, i int) []byte {
	c := append([]byte(nil), b...)
	c[i] ^= 1
	return c
}

// addL returns the signature with S replaced by S+L, the other encoding of the
// same scalar. Its top bits stay clear, so only a missing canonical-S check
// could accept it.
func addL(sig []byte) []byte {
	l, _ := new(big.Int).SetString("7237005577332262213973186563042994240857116359379907606001950938285454250989", 10)
	s := new(big.Int).SetBytes(reverse(sig[32:]))
	s.Add(s, l)
	sb := s.Bytes()
	out := append([]byte(nil), sig[:32]...)
	le := reverse(sb)
	out = append(out, le...)
	for len(out) < 64 {
		out = append(out, 0)
	}
	return out
}

// The oracle below decodes a point with a separate method (math/big ModSqrt and
// a plain affine group law) and decides small order by multiplying by 8, so it
// shares no logic with checkEd25519 or its built-in torsion list.
type oracle struct{ p, d *big.Int }

func newOracle() oracle {
	p := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
	d := new(big.Int).Mul(big.NewInt(-121665), new(big.Int).ModInverse(big.NewInt(121666), p))
	return oracle{p: p, d: d.Mod(d, p)}
}

type point struct{ x, y *big.Int }

func (o oracle) add(a, b point) point {
	t := new(big.Int).Mul(a.x, b.x)
	t.Mul(t, a.y).Mul(t, b.y).Mul(t, o.d).Mod(t, o.p)
	x := new(big.Int).Add(new(big.Int).Mul(a.x, b.y), new(big.Int).Mul(b.x, a.y))
	x.Mul(x, new(big.Int).ModInverse(new(big.Int).Add(big.NewInt(1), t), o.p)).Mod(x, o.p)
	y := new(big.Int).Add(new(big.Int).Mul(a.y, b.y), new(big.Int).Mul(a.x, b.x))
	y.Mul(y, new(big.Int).ModInverse(new(big.Int).Sub(big.NewInt(1), t), o.p)).Mod(y, o.p)
	return point{x, y}
}

func (o oracle) isIdentity(p point) bool { return p.x.Sign() == 0 && p.y.Cmp(big.NewInt(1)) == 0 }

// classify returns "ok", "invalid" (not a canonical point) or "small".
func (o oracle) classify(b []byte) string {
	sign := b[31] >> 7
	yb := append([]byte(nil), b...)
	yb[31] &= 0x7f
	y := new(big.Int).SetBytes(reverse(yb))
	if y.Cmp(o.p) >= 0 {
		return "invalid"
	}
	y2 := new(big.Int).Mul(y, y)
	u := new(big.Int).Sub(y2, big.NewInt(1))
	v := new(big.Int).Add(new(big.Int).Mul(o.d, y2), big.NewInt(1))
	x2 := new(big.Int).Mul(u, new(big.Int).ModInverse(v.Mod(v, o.p), o.p))
	x2.Mod(x2, o.p)
	x := new(big.Int).ModSqrt(x2, o.p)
	if x == nil {
		return "invalid"
	}
	if x.Sign() == 0 && sign == 1 {
		return "invalid"
	}
	if x.Bit(0) != uint(sign) {
		x.Sub(o.p, x)
	}
	pt := point{x, y}
	acc := pt
	for i := 0; i < 3; i++ { // doubling three times is multiplication by 8
		acc = o.add(acc, acc)
	}
	if o.isIdentity(acc) {
		return "small"
	}
	return "ok"
}

func TestPublicKeyAgainstOracle(t *testing.T) {
	o := newOracle()
	rng := rand.New(rand.NewSource(1))
	var inputs [][]byte
	// Small y values, both sign bits, reduced and unreduced (y+p): this is
	// where the special points and the non-canonical encodings live.
	for y := int64(0); y < 40; y++ {
		for _, add := range []bool{false, true} {
			v := big.NewInt(y)
			if add {
				v.Add(v, o.p)
			}
			if v.BitLen() > 255 {
				continue
			}
			for sign := byte(0); sign < 2; sign++ {
				b := reverse(v.FillBytes(make([]byte, 32)))
				b[31] |= sign << 7
				inputs = append(inputs, b)
			}
		}
	}
	for i := 0; i < 3000; i++ {
		b := make([]byte, 32)
		rng.Read(b)
		inputs = append(inputs, b)
	}
	for i := 0; i < 200; i++ {
		pub, _, _ := ed25519.GenerateKey(rng)
		inputs = append(inputs, pub)
	}
	for _, e := range smallOrderEncodings {
		inputs = append(inputs, e[:])
	}
	counts := map[string]int{}
	for _, b := range inputs {
		want := o.classify(b)
		counts[want]++
		_, err := NewPublicKey(AlgorithmEd25519, b)
		var got string
		switch {
		case err == nil:
			got = "ok"
		case errors.Is(err, ErrKeySmallOrder):
			got = "small"
		case errors.Is(err, ErrKeyNotCanonical):
			got = "invalid"
		default:
			t.Fatalf("unexpected error %v", err)
		}
		if got != want {
			t.Fatalf("key %x: got %s, oracle %s", b, got, want)
		}
	}
	if counts["ok"] < 200 || counts["invalid"] < 1000 || counts["small"] < 8 {
		t.Fatalf("path coverage too thin: %v", counts)
	}
}

// The built-in torsion list must be exactly the eight points of order dividing
// 8, derived here from the curve equation: a point of order 8 doubles to a
// point with y = 0, which gives d*Y^2 + 2*Y - 1 = 0 for Y = y^2.
func TestSmallOrderListIsTheTorsionSubgroup(t *testing.T) {
	o := newOracle()
	one := big.NewInt(1)
	ys := []*big.Int{big.NewInt(0), big.NewInt(1), new(big.Int).Sub(o.p, one)}
	disc := new(big.Int).ModSqrt(new(big.Int).Add(one, o.d), o.p)
	if disc == nil {
		t.Fatal("1+d has no square root")
	}
	inv := new(big.Int).ModInverse(o.d, o.p)
	for _, s := range []int{1, -1} {
		num := new(big.Int).Add(big.NewInt(-1), new(big.Int).Mul(big.NewInt(int64(s)), disc))
		Y := num.Mul(num, inv).Mod(num, o.p)
		y := new(big.Int).ModSqrt(Y, o.p)
		if y == nil {
			continue
		}
		ys = append(ys, y, new(big.Int).Sub(o.p, y))
	}
	want := map[string]bool{}
	for _, y := range ys {
		for sign := byte(0); sign < 2; sign++ {
			b := reverse(y.FillBytes(make([]byte, 32)))
			b[31] |= sign << 7
			if c := o.classify(b); c == "small" {
				want[hexOf(b)] = true
			}
		}
	}
	got := map[string]bool{}
	for _, e := range smallOrderEncodings {
		got[hexOf(e[:])] = true
	}
	if len(want) != 8 || len(got) != 8 {
		t.Fatalf("sizes: derived %d, built-in %d, want 8", len(want), len(got))
	}
	for k := range want {
		if !got[k] {
			t.Errorf("built-in list lacks small-order encoding %s", k)
		}
	}
}

// The decoder divides by d*y^2 + 1 without a zero check. That is sound only if
// -1/d is not a square modulo p, so no y gives a zero denominator. The check
// uses Euler's criterion with constants written out here: a number a is a square
// modulo an odd prime p exactly when a^((p-1)/2) = 1, and a non-square when it
// is p-1.
func TestDecoderDenominatorIsNeverZero(t *testing.T) {
	p, _ := new(big.Int).SetString("57896044618658097711785492504343953926634992332820282019728792003956564819949", 10) // 2^255 - 19
	d, _ := new(big.Int).SetString("37095705934669439343138083508754565189542113879843219016388785533085940283555", 10) // -121665/121666 mod p
	half := new(big.Int).Rsh(new(big.Int).Sub(p, big.NewInt(1)), 1)
	euler := func(a *big.Int) *big.Int { return new(big.Int).Exp(a, half, p) }
	minusOne := new(big.Int).Sub(p, big.NewInt(1))
	if got := euler(minusOne); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("-1 should be a square modulo p, Euler symbol %v", got)
	}
	if got := euler(d); got.Cmp(minusOne) != 0 {
		t.Fatalf("d should not be a square modulo p, Euler symbol %v", got)
	}
	dInv := new(big.Int).ModInverse(d, p)
	target := new(big.Int).Mul(minusOne, dInv) // -1/d
	target.Mod(target, p)
	if got := euler(target); got.Cmp(minusOne) != 0 {
		t.Fatalf("-1/d should not be a square modulo p, Euler symbol %v", got)
	}
	// The constant in the decoder is the same number.
	if curveD.Cmp(d) != 0 || fieldP.Cmp(p) != 0 {
		t.Fatal("the decoder's d or p differs from the constants of this test")
	}
}
