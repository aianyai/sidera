package ident

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"testing/iotest"
)

func seq(n int, start byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = start + byte(i)
	}
	return b
}

// idKind abstracts the three identifier types so that the rules they share are
// exercised by one table.
type idKind struct {
	name      string
	length    int
	fromBytes func([]byte) (stringer, error)
	parse     func(string) (stringer, error)
}

type stringer interface {
	String() string
	Bytes() []byte
	IsZero() bool
}

var kinds = []idKind{
	{"NodeID", NodeIDLen,
		func(b []byte) (stringer, error) { return NodeIDFromBytes(b) },
		func(s string) (stringer, error) { return ParseNodeID(s) }},
	{"PersonID", PersonIDLen,
		func(b []byte) (stringer, error) { return PersonIDFromBytes(b) },
		func(s string) (stringer, error) { return ParsePersonID(s) }},
	{"EndpointID", EndpointIDLen,
		func(b []byte) (stringer, error) { return EndpointIDFromBytes(b) },
		func(s string) (stringer, error) { return ParseEndpointID(s) }},
}

func TestFromBytes(t *testing.T) {
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			rows := []struct {
				rule string
				in   []byte
				want error
			}{
				{"exact length is accepted", seq(k.length, 1), nil},
				{"nil is rejected as wrong length", nil, ErrLength},
				{"empty is rejected as wrong length", []byte{}, ErrLength},
				{"one byte short is rejected", seq(k.length-1, 1), ErrLength},
				{"one byte long is rejected", seq(k.length+1, 1), ErrLength},
				{"all-zero is rejected as unset", make([]byte, k.length), ErrZero},
			}
			for _, r := range rows {
				t.Run(r.rule, func(t *testing.T) {
					id, err := k.fromBytes(r.in)
					if !errors.Is(err, r.want) {
						t.Fatalf("error = %v, want %v", err, r.want)
					}
					if err == nil && !bytes.Equal(id.Bytes(), r.in) {
						t.Fatalf("Bytes = %x, want %x", id.Bytes(), r.in)
					}
					if err != nil && !id.IsZero() {
						t.Fatalf("failed constructor returned non-zero value %v", id)
					}
				})
			}
		})
	}
}

func TestBytesReturnsCopy(t *testing.T) {
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			in := seq(k.length, 1)
			id, err := k.fromBytes(in)
			if err != nil {
				t.Fatal(err)
			}
			in[0] ^= 0xff // rule: FromBytes copies its input
			out := id.Bytes()
			if out[0] != 1 {
				t.Fatal("FromBytes aliases its input")
			}
			out[0] ^= 0xff // rule: Bytes returns a copy
			if id.Bytes()[0] != 1 {
				t.Fatal("Bytes aliases the identifier")
			}
		})
	}
}

func TestTextRoundTrip(t *testing.T) {
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			id, err := k.fromBytes(seq(k.length, 0xa0))
			if err != nil {
				t.Fatal(err)
			}
			s := id.String()
			if len(s) != 2*k.length {
				t.Fatalf("text length = %d, want %d", len(s), 2*k.length)
			}
			if s != strings.ToLower(s) {
				t.Fatalf("text %q is not lowercase", s)
			}
			back, err := k.parse(s)
			if err != nil {
				t.Fatal(err)
			}
			if back != id {
				t.Fatalf("round trip = %v, want %v", back, id)
			}
		})
	}
}

func TestParse(t *testing.T) {
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			good := strings.Repeat("ab", k.length)
			rows := []struct {
				rule string
				in   string
				want error
			}{
				{"canonical lowercase hex is accepted", good, nil},
				{"empty text is rejected", "", ErrLength},
				{"short text is rejected", good[:len(good)-2], ErrLength},
				{"long text is rejected", good + "ab", ErrLength},
				{"odd length is rejected", good[:len(good)-1], ErrLength},
				{"uppercase is rejected", strings.ToUpper(good), ErrText},
				{"non-hex character is rejected", good[:len(good)-1] + "g", ErrText},
				{"0x prefix is rejected", "0x" + good[2:], ErrText},
				{"surrounding space is rejected", " " + good[1:], ErrText},
				{"all-zero is rejected as unset", strings.Repeat("00", k.length), ErrZero},
			}
			for _, r := range rows {
				t.Run(r.rule, func(t *testing.T) {
					_, err := k.parse(r.in)
					if !errors.Is(err, r.want) {
						t.Fatalf("error = %v, want %v", err, r.want)
					}
				})
			}
		})
	}
}

func TestZeroValue(t *testing.T) {
	rows := []struct {
		rule   string
		isZero bool
	}{
		{"NodeID zero value is unset", NodeID{}.IsZero()},
		{"PersonID zero value is unset", PersonID{}.IsZero()},
		{"EndpointID zero value is unset", EndpointID{}.IsZero()},
		{"Address zero value is unset", Address{}.IsZero()},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if !r.isZero {
				t.Fatal("IsZero = false")
			}
		})
	}

	n, _ := NodeIDFromBytes(seq(NodeIDLen, 1))
	e, _ := EndpointIDFromBytes(seq(EndpointIDLen, 1))
	addrRows := []struct {
		rule string
		a    Address
		zero bool
	}{
		{"address with both parts set is set", Address{n, e}, false},
		{"address without node is unset", Address{Endpoint: e}, true},
		{"address without endpoint is unset", Address{Node: n}, true},
	}
	for _, r := range addrRows {
		t.Run(r.rule, func(t *testing.T) {
			if got := r.a.IsZero(); got != r.zero {
				t.Fatalf("IsZero = %v, want %v", got, r.zero)
			}
		})
	}
}

func TestNewEndpointID(t *testing.T) {
	rows := []struct {
		rule string
		r    func() *bytes.Reader
		want error
	}{
		{"random bytes are used as the id", func() *bytes.Reader { return bytes.NewReader(seq(32, 1)) }, nil},
		{"all-zero randomness is rejected", func() *bytes.Reader { return bytes.NewReader(make([]byte, 32)) }, ErrZero},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			id, err := NewEndpointID(r.r())
			if !errors.Is(err, r.want) {
				t.Fatalf("error = %v, want %v", err, r.want)
			}
			if err == nil && !bytes.Equal(id.Bytes(), seq(EndpointIDLen, 1)) {
				t.Fatalf("id = %v, want first %d bytes of the source", id, EndpointIDLen)
			}
		})
	}

	t.Run("short source is an error", func(t *testing.T) {
		if _, err := NewEndpointID(bytes.NewReader(seq(EndpointIDLen-1, 1))); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("failing source is an error", func(t *testing.T) {
		if _, err := NewEndpointID(iotest.ErrReader(errors.New("boom"))); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("consecutive reads give distinct ids", func(t *testing.T) {
		r := bytes.NewReader(seq(2*EndpointIDLen, 1))
		a, _ := NewEndpointID(r)
		b, _ := NewEndpointID(r)
		if a == b {
			t.Fatal("ids are equal")
		}
	})
}

func TestAddressText(t *testing.T) {
	n, _ := NodeIDFromBytes(seq(NodeIDLen, 1))
	e, _ := EndpointIDFromBytes(seq(EndpointIDLen, 0x40))
	addr := Address{Node: n, Endpoint: e}

	if got, want := addr.String(), n.String()+"/"+e.String(); got != want {
		t.Fatalf("String = %q, want %q", got, want)
	}
	back, err := ParseAddress(addr.String())
	if err != nil || back != addr {
		t.Fatalf("round trip = %v, %v; want %v", back, err, addr)
	}

	node, endpoint := n.String(), e.String()
	rows := []struct {
		rule string
		in   string
		want error
	}{
		{"empty is rejected", "", ErrText},
		{"missing separator is rejected", node + endpoint, ErrText},
		{"missing endpoint is rejected", node + "/", ErrLength},
		{"missing node is rejected", "/" + endpoint, ErrLength},
		{"extra segment is rejected", node + "/" + endpoint + "/" + endpoint, ErrLength},
		{"swapped parts are rejected", endpoint + "/" + node, ErrLength},
		{"malformed node is rejected", strings.ToUpper(node) + "/" + endpoint, ErrText},
		{"malformed endpoint is rejected", node + "/" + strings.ToUpper(endpoint), ErrText},
		{"zero node is rejected", strings.Repeat("00", NodeIDLen) + "/" + endpoint, ErrZero},
		{"zero endpoint is rejected", node + "/" + strings.Repeat("00", EndpointIDLen), ErrZero},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			_, err := ParseAddress(r.in)
			if !errors.Is(err, r.want) {
				t.Fatalf("error = %v, want %v", err, r.want)
			}
		})
	}
}

func TestComparableAndMapKey(t *testing.T) {
	a1, _ := NodeIDFromBytes(seq(NodeIDLen, 1))
	a2, _ := NodeIDFromBytes(seq(NodeIDLen, 1))
	b, _ := NodeIDFromBytes(seq(NodeIDLen, 2))
	e1, _ := EndpointIDFromBytes(seq(EndpointIDLen, 1))
	e2, _ := EndpointIDFromBytes(seq(EndpointIDLen, 2))

	rows := []struct {
		rule string
		got  bool
		want bool
	}{
		{"equal bytes give equal NodeIDs", a1 == a2, true},
		{"different bytes give different NodeIDs", a1 == b, false},
		{"equal addresses compare equal", Address{a1, e1} == Address{a2, e1}, true},
		{"addresses differing in node compare different", Address{a1, e1} == Address{b, e1}, false},
		{"addresses differing in endpoint compare different", Address{a1, e1} == Address{a1, e2}, false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if r.got != r.want {
				t.Fatalf("got %v, want %v", r.got, r.want)
			}
		})
	}

	t.Run("equal values hit the same map entry", func(t *testing.T) {
		m := map[Address]string{{a1, e1}: "x"}
		if m[Address{a2, e1}] != "x" {
			t.Fatal("lookup by equal address failed")
		}
		if _, ok := m[Address{b, e1}]; ok {
			t.Fatal("lookup by different address succeeded")
		}
	})
}
