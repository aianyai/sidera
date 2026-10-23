package frame

import (
	"bytes"
	"encoding/hex"
	"math/rand"
	"strings"
	"testing"
)

func TestFrameGolden(t *testing.T) {
	// Every expectation is written out by hand from the rule: a varint length,
	// the domain, then a varint length and the bytes of each field.
	long := bytes.Repeat([]byte{0xaa}, 130) // 130 needs a two-byte varint: 0x82 0x01
	rows := []struct {
		rule   string
		domain string
		fields [][]byte
		want   string
	}{
		{"a domain and no field", "sidera/x/1", nil,
			"0a" + hex.EncodeToString([]byte("sidera/x/1"))},
		{"one field", "sidera/x/1", [][]byte{[]byte("ab")},
			"0a" + hex.EncodeToString([]byte("sidera/x/1")) + "02" + "6162"},
		{"two fields", "sidera/x/1", [][]byte{{0x01}, {0x02, 0x03}},
			"0a" + hex.EncodeToString([]byte("sidera/x/1")) + "01" + "01" + "02" + "0203"},
		{"an empty field keeps its length byte", "sidera/x/1", [][]byte{{}},
			"0a" + hex.EncodeToString([]byte("sidera/x/1")) + "00"},
		{"a nil field is an empty field", "sidera/x/1", [][]byte{nil},
			"0a" + hex.EncodeToString([]byte("sidera/x/1")) + "00"},
		{"a field of 130 bytes has a two-byte length", "sidera/x/1", [][]byte{long},
			"0a" + hex.EncodeToString([]byte("sidera/x/1")) + "8201" + strings.Repeat("aa", 130)},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := hex.EncodeToString(Frame(r.domain, r.fields...)); got != r.want {
				t.Fatalf("Frame = %s\nwant    %s", got, r.want)
			}
		})
	}
}

// parse reads framed bytes back with its own varint reader, independent of the
// encoder, and returns the domain and the fields.
func parse(t *testing.T, b []byte) (string, [][]byte) {
	t.Helper()
	next := func() []byte {
		var n uint64
		for shift := 0; ; shift += 7 {
			if len(b) == 0 {
				t.Fatal("truncated length")
			}
			c := b[0]
			b = b[1:]
			n |= uint64(c&0x7f) << shift
			if c < 0x80 {
				break
			}
		}
		if uint64(len(b)) < n {
			t.Fatal("truncated field")
		}
		out := b[:n]
		b = b[n:]
		return out
	}
	domain := string(next())
	var fields [][]byte
	for len(b) > 0 {
		fields = append(fields, next())
	}
	return domain, fields
}

// The framing is injective: it parses back into exactly the inputs, whatever
// the lengths, so two different inputs can never share bytes. Fixed seed; the
// lengths straddle the one-byte and two-byte varint boundary.
func TestFrameParsesBackIntoItsInputs(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	sizes := []int{0, 1, 2, 126, 127, 128, 129, 300}
	multi := 0
	for i := 0; i < 2000; i++ {
		domain := DomainPrefix + strings.Repeat("d", rng.Intn(MaxDomainLen-len(DomainPrefix)+1))
		fields := make([][]byte, rng.Intn(5))
		for j := range fields {
			fields[j] = make([]byte, sizes[rng.Intn(len(sizes))])
			rng.Read(fields[j])
		}
		gotDomain, gotFields := parse(t, Frame(domain, fields...))
		if gotDomain != domain || len(gotFields) != len(fields) {
			t.Fatalf("round trip lost structure: %q %d fields, want %q %d", gotDomain, len(gotFields), domain, len(fields))
		}
		for j := range fields {
			if !bytes.Equal(gotFields[j], fields[j]) {
				t.Fatalf("field %d differs", j)
			}
		}
		if len(fields) > 1 {
			multi++
		}
	}
	if multi < 1000 {
		t.Fatalf("only %d inputs had several fields", multi)
	}
}

func TestFrameBoundariesAreNotAmbiguous(t *testing.T) {
	const d = "sidera/x/1"
	rows := []struct {
		rule string
		a, b [][]byte
	}{
		{"a boundary moved between two fields", [][]byte{[]byte("ab"), []byte("c")}, [][]byte{[]byte("a"), []byte("bc")}},
		{"two fields against their concatenation", [][]byte{[]byte("a"), []byte("b")}, [][]byte{[]byte("ab")}},
		{"an empty field against no field", [][]byte{{}}, nil},
		{"an empty field in front against behind", [][]byte{{}, []byte("a")}, [][]byte{[]byte("a"), {}}},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if bytes.Equal(Frame(d, r.a...), Frame(d, r.b...)) {
				t.Fatal("two different inputs frame to the same bytes")
			}
		})
	}
	t.Run("the domain is part of the framed bytes", func(t *testing.T) {
		if bytes.Equal(Frame("sidera/a/1", []byte("x")), Frame("sidera/b/1", []byte("x"))) {
			t.Fatal("domains do not separate")
		}
	})
	t.Run("a domain cannot absorb the first field", func(t *testing.T) {
		if bytes.Equal(Frame("sidera/a/1", []byte("b")), Frame("sidera/a/1b")) {
			t.Fatal("domain and field are ambiguous")
		}
	})
	t.Run("the framed bytes of one domain never begin with those of another", func(t *testing.T) {
		a, b := Frame("sidera/a/1"), Frame("sidera/a/10", []byte("x"))
		if bytes.HasPrefix(b, a) || bytes.HasPrefix(a, b) {
			t.Fatal("one domain's framing is a prefix of another's")
		}
	})
}

func TestFrameFirstByteIsTheDomainLengthBelow64(t *testing.T) {
	d := DomainPrefix + strings.Repeat("d", MaxDomainLen-len(DomainPrefix))
	if len(d) != MaxDomainLen {
		t.Fatalf("test setup: domain is %d bytes", len(d))
	}
	if b := Frame(Domain(d), []byte{1}); b[0] != MaxDomainLen || b[0] == 'n' || b[0] == 'l' {
		t.Fatalf("first byte = %d", b[0])
	}
}

func TestDomainRules(t *testing.T) {
	long := DomainPrefix + strings.Repeat("d", MaxDomainLen-len(DomainPrefix))
	rows := []struct {
		rule   string
		domain string
		ok     bool
	}{
		{"a sidera/ domain", "sidera/id/node/1", true},
		{"a domain of exactly 63 bytes", long, true},
		{"a domain of 64 bytes", long + "d", false},
		{"no sidera/ prefix", "libp2p-peer-record", false},
		{"the prefix without its slash", "sidera", false},
		{"upper-case prefix", "Sidera/x/1", false},
		{"empty", "", false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if err := CheckDomain(r.domain); (err == nil) != r.ok {
				t.Fatalf("CheckDomain(%q) = %v, want ok=%v", r.domain, err, r.ok)
			}
			func() {
				defer func() {
					if (recover() == nil) != r.ok {
						t.Fatalf("Domain(%q): panic mismatch, want ok=%v", r.domain, r.ok)
					}
				}()
				if got := Domain(r.domain); r.ok && got != r.domain {
					t.Fatalf("Domain returned %q", got)
				}
			}()
		})
	}
}
