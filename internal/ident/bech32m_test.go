package ident

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// Vectors from the bech32m specification (BIP 350), valid strings. They are
// all lowercase here because this decoder refuses the uppercase form.
func TestDecodeBech32mSpecificationVectors(t *testing.T) {
	rows := []struct {
		rule string
		in   string
		hrp  string
	}{
		{"short string", "a1lqfn3a", "a"},
		{"long human-readable part", "an83characterlonghumanreadablepartthatcontainsthetheexcludedcharactersbioandnumber11sg7hg6", "an83characterlonghumanreadablepartthatcontainsthetheexcludedcharactersbioandnumber1"},
		{"data part", "abcdef1l7aum6echk45nj3s0wdvt2fg8x9yrzpqzd3ryx", "abcdef"},
		{"split on the last separator", "split1checkupstagehandshakeupstreamerranterredcaperredlc445v", "split"},
		{"punctuation prefix", "?1v759aa", "?"},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			hrp, _, err := decodeBech32m(r.in)
			if err != nil || hrp != r.hrp {
				t.Fatalf("decode = %q, %v; want %q", hrp, err, r.hrp)
			}
		})
	}
}

// Invalid strings from BIP 350, plus the cases this decoder adds.
func TestDecodeBech32mRejects(t *testing.T) {
	rows := []struct {
		rule string
		in   string
		want error
		msg  string // part of the message, when the sentinel alone does not tell the rule
	}{
		{"prefix character below the range", "\x201xj0phk", ErrText, ""},
		{"prefix character above the range", "\x7f1g6xzxy", ErrText, ""},
		{"prefix character in the high range", "\x801vctc34", ErrText, ""},
		{"longer than 90 characters", "an84characterslonghumanreadablepartthatcontainsthetheexcludedcharactersbioandnumber11d6pts4", ErrLength, ""},
		{"no separator", "qyrz8wqd2c9m", ErrText, ""},
		{"empty prefix", "1qyrz8wqd2c9m", ErrText, ""},
		{"invalid data character b", "y1b0jsk6g", ErrText, ""},
		{"invalid data character i", "lt1igcx5c0", ErrText, ""},
		{"checksum too short", "in1muywd", ErrText, ""},
		{"invalid checksum character", "mm1crxm3i", ErrText, ""},
		{"invalid checksum character o", "au1s5cgom", ErrText, ""},
		{"uppercase with a wrong checksum", "M1VUXWEZ", ErrText, ""},
		{"empty prefix with checksum", "16plkw9", ErrText, ""},
		{"empty prefix, only a checksum", "1p2gdwpf", ErrText, ""},
		{"uppercase", "A1LQFN3A", ErrText, "lowercase"},
		{"mixed case", "a1LQFN3a", ErrText, "lowercase"},
		{"uppercase prefix only", "A1lqfn3a", ErrText, "lowercase"},
		{"bech32 (not bech32m) checksum", "a12uel5l", ErrChecksum, ""},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			_, _, err := decodeBech32m(r.in)
			if !errors.Is(err, r.want) || (r.msg != "" && !strings.Contains(err.Error(), r.msg)) {
				t.Fatalf("error = %v, want %v mentioning %q", err, r.want, r.msg)
			}
		})
	}
}

func TestConvertBits(t *testing.T) {
	rows := []struct {
		rule     string
		in       []byte
		from, to uint
		pad      bool
		out      []byte
		ok       bool
	}{
		{"8 to 5 pads with zeros", []byte{0xff}, 8, 5, true, []byte{31, 28}, true},
		{"5 to 8 drops zero padding", []byte{31, 28}, 5, 8, false, []byte{0xff}, true},
		{"5 to 8 refuses set padding bits", []byte{31, 29}, 5, 8, false, nil, false},
		{"5 to 8 refuses a whole spare group", []byte{31, 31, 0}, 5, 8, false, nil, false},
		{"value wider than the source group", []byte{32}, 5, 8, false, nil, false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			out, ok := convertBits(r.in, r.from, r.to, r.pad)
			if ok != r.ok || !bytes.Equal(out, r.out) {
				t.Fatalf("convertBits = %v, %v; want %v, %v", out, ok, r.out, r.ok)
			}
		})
	}
}

func TestEncodeBech32mAgainstSpecification(t *testing.T) {
	rows := []struct{ hrp, want string }{
		{"a", "a1lqfn3a"},
		{"?", "?1v759aa"},
	}
	for _, r := range rows {
		t.Run(r.hrp, func(t *testing.T) {
			if got := encodeBech32m(r.hrp, nil); got != r.want {
				t.Fatalf("encode = %q, want %q", got, r.want)
			}
		})
	}
}
