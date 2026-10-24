package pairing

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Code is the verification code: six decimal digits that both owners compare.
type Code uint32

const (
	// codeSpace is the number of codes.
	codeSpace = 1_000_000
	// codeDigits is the number of digits of a code.
	codeDigits = 6
	// sampleLimit is the largest multiple of codeSpace below 2^32. A candidate
	// at or above it is rejected, so every code is equally likely.
	sampleLimit = 4_294_000_000
	// sampleCandidates is the number of 4-byte candidates in the 32 bytes of
	// output keying material.
	sampleCandidates = 8
)

// sampleCode draws a code from the output keying material by rejection
// sampling: the first of its eight big-endian 4-byte integers below
// sampleLimit gives the code modulo 10^6. Reducing a 32-bit value modulo 10^6
// directly would favour the low codes; rejection removes the bias. A candidate
// is rejected with probability about 2.25e-4, all eight with about 6.6e-30, in
// which case it reports false and both sides abort the same way.
func sampleCode(okm []byte) (Code, bool) {
	for i := range sampleCandidates {
		v := binary.BigEndian.Uint32(okm[4*i:])
		if v < sampleLimit {
			return Code(v % codeSpace), true
		}
	}
	return 0, false
}

// String returns the code as shown to the owner: six digits, zero padded, in
// two groups of three ("482 019").
func (c Code) String() string {
	s := fmt.Sprintf("%0*d", codeDigits, uint32(c)%codeSpace)
	return s[:3] + " " + s[3:]
}

// ParseCode reads a code as an owner types it: six digits, optionally with the
// one space between the two groups of three that String writes.
func ParseCode(s string) (Code, error) {
	if len(s) == codeDigits+1 && s[3] == ' ' {
		s = s[:3] + s[4:]
	}
	if len(s) != codeDigits || strings.Trim(s, "0123456789") != "" {
		return 0, fmt.Errorf("%w: want six digits", ErrCode)
	}
	var v uint32
	for _, d := range []byte(s) {
		v = v*10 + uint32(d-'0')
	}
	return Code(v), nil
}
