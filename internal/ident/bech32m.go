package ident

import (
	"fmt"
	"strings"
)

// bech32m (BIP 350) as used for identifier text. The package carries its own
// implementation because the format is small, it has no standard-library
// version, and the available libraries are either not importable or pull in
// large unrelated trees.

const (
	bech32Charset  = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	bech32mConst   = 0x2bc830a3
	bech32Const    = 1
	checksumLen    = 6
	bech32MaxTotal = 90
)

var bech32Generator = [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}

func bech32Polymod(values []byte) uint32 {
	chk := uint32(1)
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (top>>i)&1 == 1 {
				chk ^= bech32Generator[i]
			}
		}
	}
	return chk
}

func hrpExpand(hrp string) []byte {
	out := make([]byte, 0, 2*len(hrp)+1)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]>>5)
	}
	out = append(out, 0)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]&31)
	}
	return out
}

// convertBits regroups data from from-bit to to-bit groups. With pad it zero
// pads the final group (encoding); without pad it fails if more than from-1
// bits are left over or if any leftover bit is set (decoding), so a string
// whose padding bits are not zero is refused.
func convertBits(data []byte, from, to uint, pad bool) ([]byte, bool) {
	var acc uint32
	var bits uint
	maxv := uint32(1)<<to - 1
	var out []byte
	for _, v := range data {
		if uint32(v)>>from != 0 {
			return nil, false
		}
		acc = acc<<from | uint32(v)
		bits += from
		for bits >= to {
			bits -= to
			out = append(out, byte(acc>>bits&maxv))
		}
	}
	if pad {
		if bits > 0 {
			out = append(out, byte(acc<<(to-bits)&maxv))
		}
	} else if bits >= from || acc<<(to-bits)&maxv != 0 {
		return nil, false
	}
	return out, true
}

// encodeBech32m returns hrp + "1" + data + checksum. hrp must be lowercase
// printable ASCII and data holds 5-bit values.
func encodeBech32m(hrp string, data5 []byte) string {
	values := append(hrpExpand(hrp), data5...)
	values = append(values, make([]byte, checksumLen)...)
	mod := bech32Polymod(values) ^ bech32mConst
	var sb strings.Builder
	sb.WriteString(hrp)
	sb.WriteByte('1')
	for _, d := range data5 {
		sb.WriteByte(bech32Charset[d])
	}
	for i := 0; i < checksumLen; i++ {
		sb.WriteByte(bech32Charset[byte(mod>>(5*(5-uint(i))))&31])
	}
	return sb.String()
}

// decodeBech32m splits and verifies a bech32m string. It accepts only the
// lowercase form (the mixed-case and uppercase forms BIP 350 allows are
// refused, so every value has one text) and only the bech32m checksum constant.
// It returns the human-readable part and the 5-bit data without the checksum.
func decodeBech32m(s string) (hrp string, data5 []byte, err error) {
	if len(s) > bech32MaxTotal {
		return "", nil, fmt.Errorf("%w: %d characters, at most %d", ErrLength, len(s), bech32MaxTotal)
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 33 || c > 126 {
			return "", nil, fmt.Errorf("%w: character out of range", ErrText)
		}
	}
	if strings.ToLower(s) != s {
		return "", nil, fmt.Errorf("%w: want lowercase", ErrText)
	}
	sep := strings.LastIndexByte(s, '1')
	if sep < 1 || len(s)-sep-1 < checksumLen {
		return "", nil, fmt.Errorf("%w: missing separator, empty prefix or short checksum", ErrText)
	}
	hrp = s[:sep]
	values := make([]byte, 0, len(s)-sep-1)
	for i := sep + 1; i < len(s); i++ {
		idx := strings.IndexByte(bech32Charset, s[i])
		if idx < 0 {
			return "", nil, fmt.Errorf("%w: character %q is not in the bech32 alphabet", ErrText, s[i])
		}
		values = append(values, byte(idx))
	}
	if bech32Polymod(append(hrpExpand(hrp), values...)) != bech32mConst {
		return "", nil, fmt.Errorf("%w: not a valid bech32m checksum", ErrChecksum)
	}
	return hrp, values[:len(values)-checksumLen], nil
}

// encodeID writes raw as a bech32m string with the given prefix.
func encodeID(hrp string, raw []byte) string {
	data5, _ := convertBits(raw, 8, 5, true)
	return encodeBech32m(hrp, data5)
}

// decodeID parses a bech32m identifier text with the expected prefix; the
// caller checks the length of the bytes it gets back. The result is checked by
// re-encoding, so only the one canonical text of a value is accepted.
func decodeID(name, hrp, s string) ([]byte, error) {
	gotHRP, data5, err := decodeBech32m(s)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if gotHRP != hrp {
		return nil, fmt.Errorf("%s: %w: got %q, want %q", name, ErrPrefix, gotHRP, hrp)
	}
	raw, ok := convertBits(data5, 5, 8, false)
	if !ok {
		return nil, fmt.Errorf("%s: %w: padding bits are not zero", name, ErrText)
	}
	if encodeID(hrp, raw) != s {
		return nil, fmt.Errorf("%s: %w: not the canonical form", name, ErrText)
	}
	return raw, nil
}
