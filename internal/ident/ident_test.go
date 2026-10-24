package ident

import (
	"bytes"
	"encoding/hex"
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

// Text vectors written by hand (computed with an independent bech32m
// implementation, not with this package).
const (
	vecNode        = "node130llww2yztfmjn2fsslfx45qzye83306u3q7nzfs300qvxly7wpqgs2d9f"
	vecNodeBytes   = "8bfff7394412d3b94d49843e935680113278c5fae441e989308bde061be4f382"
	vecPerson      = "person1ka5ze2fvvznkgh5q4wvxpedus3dmet9ph9ypwmud2nrw255c25xssnvz2w"
	vecEndpoint    = "ep1zqg3yyc5z5tpwxqergd3c8g7ruua880z"
	vecEndpointHex = "101112131415161718191a1b1c1d1e1f"

	vecNodeZero     = "node1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqknry08"
	vecPersonZero   = "person1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqh4l2zv"
	vecEndpointZero = "ep1qqqqqqqqqqqqqqqqqqqqqqqqqq2uzx7q"

	// Sixteen 0xff bytes: 128 bits make 25 full groups of five set bits ("l")
	// and a last group of three set bits and two padding zeros ("u"). Computed
	// with an independent bech32m implementation written from BIP 350.
	vecEndpointSystem = "ep1lllllllllllllllllllllllllusa628q"

	// Same bytes as vecNode, with the older bech32 checksum constant.
	vecNodeBech32 = "node130llww2yztfmjn2fsslfx45qzye83306u3q7nzfs300qvxly7wpqav6pqt"
	// Same bytes as vecNode, last data character carries a set padding bit and
	// the checksum is recomputed so that it verifies.
	vecNodePadBits = "node130llww2yztfmjn2fsslfx45qzye83306u3q7nzfs300qvxly7wpp4x7ccm"
	vecEndpointPad = "ep1zqg3yyc5z5tpwxqergd3c8g7raptnjjs"
	// Other kinds with valid checksums over the same 32 bytes as vecNode.
	vecPersonOverNodeBytes   = "person130llww2yztfmjn2fsslfx45qzye83306u3q7nzfs300qvxly7wpqfkkrgz"
	vecEndpointOverNodeBytes = "ep130llww2yztfmjn2fsslfx45qzye83306u3q7nzfs300qvxly7wpq9zv05q"
)

var vecText = map[string]string{"NodeID": vecNode, "PersonID": vecPerson, "EndpointID": vecEndpoint}

func TestTextGolden(t *testing.T) {
	node, err := NodeIDFromBytes(mustHex(t, vecNodeBytes))
	if err != nil {
		t.Fatal(err)
	}
	if got := node.String(); got != vecNode {
		t.Fatalf("NodeID text = %q, want %q", got, vecNode)
	}
	ep, err := EndpointIDFromBytes(mustHex(t, vecEndpointHex))
	if err != nil {
		t.Fatal(err)
	}
	if got := ep.String(); got != vecEndpoint {
		t.Fatalf("EndpointID text = %q, want %q", got, vecEndpoint)
	}
	person, err := ParsePersonID(vecPerson)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(person.Bytes()); got != "b7682ca92c60a7645e80ab9860e5bc845bbcaca1b948176f8d54c6e55298550d" {
		t.Fatalf("PersonID bytes = %s", got)
	}
}

func TestTextRoundTrip(t *testing.T) {
	wantLen := map[string]int{"NodeID": 63, "PersonID": 65, "EndpointID": 35}
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			id, err := k.fromBytes(seq(k.length, 0xa0))
			if err != nil {
				t.Fatal(err)
			}
			s := id.String()
			if len(s) != wantLen[k.name] {
				t.Fatalf("text length = %d, want %d", len(s), wantLen[k.name])
			}
			if s != strings.ToLower(s) {
				t.Fatalf("text %q is not lowercase", s)
			}
			back, err := k.parse(s)
			if err != nil {
				t.Fatal(err)
			}
			if back.String() != s || !bytes.Equal(back.Bytes(), id.Bytes()) {
				t.Fatalf("round trip = %v, want %v", back, id)
			}
		})
	}
}

func TestParse(t *testing.T) {
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			good := vecText[k.name]
			zero := map[string]string{"NodeID": vecNodeZero, "PersonID": vecPersonZero, "EndpointID": vecEndpointZero}[k.name]
			hrp := good[:strings.IndexByte(good, '1')]
			rows := []struct {
				rule string
				in   string
				want error
			}{
				{"canonical lowercase bech32m is accepted", good, nil},
				{"empty text is rejected", "", ErrText},
				{"short text is rejected", good[:len(good)-1], ErrChecksum},
				{"long text is rejected", good + "q", ErrChecksum},
				{"uppercase is rejected", strings.ToUpper(good), ErrText},
				{"mixed case is rejected", good[:len(good)-1] + strings.ToUpper(good[len(good)-1:]), ErrText},
				{"character outside the alphabet is rejected", good[:len(good)-1] + "b", ErrText},
				{"hex text of the old form is rejected", strings.Repeat("ab", k.length), ErrText},
				{"surrounding space is rejected", " " + good, ErrText},
				{"single changed character fails the checksum", changeChar(good, len(good)-8), ErrChecksum},
				{"missing prefix is rejected", good[len(hrp):], ErrText},
				{"all-zero value is rejected as unset", zero, ErrZero},
			}
			for _, r := range rows {
				t.Run(r.rule, func(t *testing.T) {
					got, err := k.parse(r.in)
					if !errors.Is(err, r.want) {
						t.Fatalf("error = %v, want %v", err, r.want)
					}
					if r.want != nil && got != nil && !got.IsZero() {
						t.Fatalf("a failed parse returned %v", got)
					}
				})
			}
		})
	}
}

func changeChar(s string, i int) string {
	repl := byte('q')
	if s[i] == 'q' {
		repl = 'p'
	}
	return s[:i] + string(repl) + s[i+1:]
}

func TestParseKindRules(t *testing.T) {
	rows := []struct {
		rule  string
		parse func() error
		want  error
	}{
		{"a person text where a node is expected", func() error { _, err := ParseNodeID(vecPersonOverNodeBytes); return err }, ErrPrefix},
		{"an endpoint text where a node is expected", func() error { _, err := ParseNodeID(vecEndpointOverNodeBytes); return err }, ErrPrefix},
		{"a node text where a person is expected", func() error { _, err := ParsePersonID(vecNode); return err }, ErrPrefix},
		{"a node text where an endpoint is expected", func() error { _, err := ParseEndpointID(vecNode); return err }, ErrPrefix},
		{"a bech32 (not bech32m) checksum is rejected", func() error { _, err := ParseNodeID(vecNodeBech32); return err }, ErrChecksum},
		{"a set padding bit is rejected even with a valid checksum", func() error { _, err := ParseNodeID(vecNodePadBits); return err }, ErrText},
		{"a set padding bit in an endpoint is rejected", func() error { _, err := ParseEndpointID(vecEndpointPad); return err }, ErrText},
		{"a node text with 16 data bytes is rejected", func() error { _, err := ParseNodeID("node1qypqxpq9qcrsszg2pvxq6rs0zqwfdk0r"); return err }, ErrLength},
		{"a node text with 33 data bytes is rejected", func() error {
			_, err := ParseNodeID("node1qypqxpq9qcrsszg2pvxq6rs0zqg3yyc5z5tpwxqergd3c8g7ruszzsg29a9")
			return err
		}, ErrLength},
		{"an endpoint text with 32 data bytes is rejected", func() error {
			_, err := ParseEndpointID("ep1qypqxpq9qcrsszg2pvxq6rs0zqg3yyc5z5tpwxqergd3c8g7rusq5h2dfx")
			return err
		}, ErrLength},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if err := r.parse(); !errors.Is(err, r.want) {
				t.Fatalf("error = %v, want %v", err, r.want)
			}
		})
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
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
		{"missing endpoint is rejected", node + "/", ErrText},
		{"missing node is rejected", "/" + endpoint, ErrText},
		{"extra segment is rejected", node + "/" + endpoint + "/" + endpoint, ErrChecksum},
		{"swapped parts are rejected", endpoint + "/" + node, ErrPrefix},
		{"malformed node is rejected", strings.ToUpper(node) + "/" + endpoint, ErrText},
		{"malformed endpoint is rejected", node + "/" + strings.ToUpper(endpoint), ErrText},
		{"zero node is rejected", vecNodeZero + "/" + endpoint, ErrZero},
		{"zero endpoint is rejected", node + "/" + vecEndpointZero, ErrZero},
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

func systemBytes() []byte { return bytes.Repeat([]byte{0xff}, EndpointIDLen) }

func TestSystemEndpointValue(t *testing.T) {
	sys := SystemEndpoint()
	if got := hex.EncodeToString(sys.Bytes()); got != "ffffffffffffffffffffffffffffffff" {
		t.Fatalf("SystemEndpoint bytes = %s, want sixteen 0xff bytes", got)
	}
	if sys.IsZero() {
		t.Fatal("SystemEndpoint is the unset value")
	}
}

func TestSystemEndpointTextGolden(t *testing.T) {
	if got := SystemEndpoint().String(); got != vecEndpointSystem {
		t.Fatalf("SystemEndpoint text = %q, want %q", got, vecEndpointSystem)
	}
	if n := strings.Count(vecEndpointSystem, "l"); n != 25 {
		t.Fatalf("golden vector has %d 'l' characters, want 25 (vector typo)", n)
	}
}

// The node must be able to hold the system endpoint in records and pages, so
// both entry points that build an identifier from outside accept it and give
// back the reserved value.
func TestSystemEndpointRoundTrip(t *testing.T) {
	fromText, err := ParseEndpointID(vecEndpointSystem)
	if err != nil {
		t.Fatalf("ParseEndpointID(%q): %v", vecEndpointSystem, err)
	}
	fromBytes, err := EndpointIDFromBytes(systemBytes())
	if err != nil {
		t.Fatalf("EndpointIDFromBytes(system bytes): %v", err)
	}
	for name, id := range map[string]EndpointID{"parsed text": fromText, "from bytes": fromBytes} {
		if id != SystemEndpoint() || !id.IsSystem() {
			t.Fatalf("%s = %v, want the system endpoint", name, id)
		}
		if got := id.String(); got != vecEndpointSystem {
			t.Fatalf("%s prints as %q, want %q", name, got, vecEndpointSystem)
		}
	}

	node, _ := NodeIDFromBytes(seq(NodeIDLen, 1))
	addr, err := ParseAddress(node.String() + "/" + vecEndpointSystem)
	if err != nil {
		t.Fatalf("ParseAddress with the system endpoint: %v", err)
	}
	if addr.Endpoint != SystemEndpoint() || addr.String() != node.String()+"/"+vecEndpointSystem {
		t.Fatalf("address = %v", addr)
	}
}

func TestIsSystem(t *testing.T) {
	oneBitOff := systemBytes()
	oneBitOff[EndpointIDLen-1] = 0xfe
	rows := []struct {
		rule string
		id   EndpointID
		want bool
	}{
		{"the reserved value is the system endpoint", SystemEndpoint(), true},
		{"sixteen 0xff bytes are the system endpoint", mustEndpoint(t, systemBytes()), true},
		{"the unset value is not the system endpoint", EndpointID{}, false},
		{"one bit off the reserved value is not the system endpoint", mustEndpoint(t, oneBitOff), false},
		{"an ordinary id is not the system endpoint", mustEndpoint(t, seq(EndpointIDLen, 1)), false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := r.id.IsSystem(); got != r.want {
				t.Fatalf("IsSystem = %v, want %v", got, r.want)
			}
		})
	}
}

func mustEndpoint(t *testing.T, b []byte) EndpointID {
	t.Helper()
	id, err := EndpointIDFromBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// A node never issues the reserved value, even when the randomness source
// produces exactly its bytes, and it treats that like the all-zero draw: a
// failure, not a silent substitute.
func TestNewEndpointIDNeverIssuesReserved(t *testing.T) {
	rows := []struct {
		rule string
		src  []byte
		want error
	}{
		{"randomness equal to the reserved value is refused", systemBytes(), ErrReserved},
		{"randomness equal to the reserved value is refused with more bytes behind it", append(systemBytes(), seq(EndpointIDLen, 1)...), ErrReserved},
		{"all-zero randomness is still refused", make([]byte, EndpointIDLen), ErrZero},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			id, err := NewEndpointID(bytes.NewReader(r.src))
			if !errors.Is(err, r.want) {
				t.Fatalf("error = %v, want %v", err, r.want)
			}
			if id != (EndpointID{}) {
				t.Fatalf("returned %v with an error, want the zero value", id)
			}
		})
	}
	t.Run("the reserved value is distinct from the zero error", func(t *testing.T) {
		if errors.Is(ErrReserved, ErrZero) || errors.Is(ErrZero, ErrReserved) {
			t.Fatal("ErrReserved and ErrZero are the same error")
		}
	})
}
