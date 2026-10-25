package engine

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
)

// Identifiers written out as text, so that the vectors do not depend on the code
// that prints them: ids made from consecutive bytes.
const (
	nodeText = "node1qypqxpq9qcrsszg2pvxq6rs0zqg3yyc5z5tpwxqergd3c8g7rusqe9v0c0"
	epText1  = "ep1qypqxpq9qcrsszg2pvxq6rs0zqear7st"
	epText2  = "ep1zyfpx9q4zct3sxg6rvwp68slyqq2dn6x"
)

// distinctAddresses are n different addresses of the node, none of them zero.
func distinctAddresses(t testing.TB, n int) []ident.Address {
	t.Helper()
	node, err := ident.ParseNodeID(nodeText)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]ident.Address, n)
	for i := range out {
		b := make([]byte, ident.EndpointIDLen)
		b[0], b[1] = byte(i>>8), byte(i)
		b[15] = 1
		id, err := ident.EndpointIDFromBytes(b)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = ident.Address{Node: node, Endpoint: id}
	}
	return out
}

func addrOf(t testing.TB, node, ep string) ident.Address {
	t.Helper()
	a, err := ident.ParseAddress(node + "/" + ep)
	if err != nil {
		t.Fatalf("ParseAddress: %v", err)
	}
	return a
}

// The endpoint's record holds what the engine owns about it in one compact JSON
// object: what the adapter said of the endpoint, and its declaration.
func TestEndpointMetaGoldenVectors(t *testing.T) {
	a1, a2 := addrOf(t, nodeText, epText1), addrOf(t, nodeText, epText2)
	for _, tc := range []struct {
		name string
		meta endpointMeta
		json string
	}{
		{"nothing said", endpointMeta{}, `{}`},
		{"a name only", endpointMeta{DisplayName: "Notes"}, `{"display_name":"Notes"}`},
		{"name and description", endpointMeta{DisplayName: "Notes", Description: "keeps the notes"},
			`{"display_name":"Notes","description":"keeps the notes"}`},
		{"every flag set", endpointMeta{Declaration: authz.Declaration{Paused: true, RefuseMultiRecipient: true, RestrictSenders: true, HiddenFromDirectory: true}},
			`{"paused":true,"refuse_multi_recipient":true,"restrict_senders":true,"hidden_from_directory":true}`},
		{"a sender list, kept in order", endpointMeta{DisplayName: "N", Declaration: authz.Declaration{RestrictSenders: true, AcceptFrom: []ident.Address{a2, a1}}},
			`{"display_name":"N","restrict_senders":true,"accept_from":["` + a2.String() + `","` + a1.String() + `"]}`},
		{"text that needs escaping", endpointMeta{DisplayName: `a "b" <c> é`}, `{"display_name":"a \"b\" <c> é"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := encodeMeta(tc.meta)
			if err != nil {
				t.Fatalf("encodeMeta: %v", err)
			}
			if string(got) != tc.json {
				t.Errorf("encoded\n%s\nwant\n%s", got, tc.json)
			}
			back, err := decodeMeta([]byte(tc.json))
			if err != nil {
				t.Fatalf("decodeMeta: %v", err)
			}
			again, err := encodeMeta(back)
			if err != nil || !bytes.Equal(again, []byte(tc.json)) {
				t.Errorf("decoding then encoding gave %s, %v", again, err)
			}
		})
	}
}

func TestDecodeMetaTakesNothingAsDeclaringNothing(t *testing.T) {
	for _, in := range [][]byte{nil, {}} {
		m, err := decodeMeta(in)
		if err != nil || m.DisplayName != "" || m.Declaration.Paused {
			t.Errorf("decodeMeta(%q) = %+v, %v", in, m, err)
		}
	}
}

func TestDecodeMetaRefusals(t *testing.T) {
	a1 := addrOf(t, nodeText, epText1).String()
	many := make([]string, MaxAcceptFrom+1)
	for i, a := range distinctAddresses(t, MaxAcceptFrom+1) {
		many[i] = `"` + a.String() + `"`
	}
	for _, tc := range []struct{ name, in string }{
		{"an unknown member", `{"name":"x"}`},
		{"a repeated member", `{"paused":true,"paused":true}`},
		{"a member of the wrong type", `{"paused":"yes"}`},
		{"null", `{"display_name":null}`},
		{"an explicit empty name", `{"display_name":""}`},
		{"an address that is not one", `{"accept_from":["nope"]}`},
		{"a zero list stated", `{"accept_from":[]}`},
		{"the same sender twice", `{"accept_from":["` + a1 + `","` + a1 + `"]}`},
		{"more senders than may be listed", `{"accept_from":[` + strings.Join(many, ",") + `]}`},
		{"a flag stated false", `{"paused":false}`},
		{"members out of their order", `{"restrict_senders":true,"paused":true}`},
		{"white space between members", `{"paused": true}`},
		{"data after the object", `{} {}`},
		{"not an object", `[]`},
		{"not JSON", `{`},
		{"invalid UTF-8", "{\"display_name\":\"\xff\"}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if m, err := decodeMeta([]byte(tc.in)); err == nil {
				t.Errorf("decodeMeta(%s) = %+v, want an error", tc.in, m)
			}
		})
	}
}

// What cannot be decoded is not written: the same checks run on the way in.
func TestEncodeMetaRefusals(t *testing.T) {
	a1 := addrOf(t, nodeText, epText1)
	dup := []ident.Address{a1, a1}
	big := distinctAddresses(t, MaxAcceptFrom+1)
	for _, tc := range []struct {
		name string
		m    endpointMeta
	}{
		{"a zero address", endpointMeta{Declaration: authz.Declaration{RestrictSenders: true, AcceptFrom: []ident.Address{{}}}}},
		{"the same sender twice", endpointMeta{Declaration: authz.Declaration{RestrictSenders: true, AcceptFrom: dup}}},
		{"too many senders", endpointMeta{Declaration: authz.Declaration{RestrictSenders: true, AcceptFrom: big}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if b, err := encodeMeta(tc.m); err == nil {
				t.Errorf("encodeMeta = %s, want an error", b)
			}
		})
	}
}

// Whatever decodes is canonical: it encodes back to the same bytes. Decoding
// never panics, whatever is on the disk.
func FuzzDecodeMeta(f *testing.F) {
	a1, a2 := addrOf(f, nodeText, epText1), addrOf(f, nodeText, epText2)
	for _, m := range []endpointMeta{
		{}, {DisplayName: "Notes", Description: "d"},
		{Declaration: authz.Declaration{RestrictSenders: true, AcceptFrom: []ident.Address{a1, a2}, Paused: true}},
	} {
		b, err := encodeMeta(m)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Add([]byte(`{"paused":true,"paused":true}`))
	f.Add([]byte(`{"accept_from":[]}`))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := decodeMeta(data)
		if err != nil || len(data) == 0 {
			return
		}
		back, err := encodeMeta(m)
		if err != nil {
			t.Fatalf("decoded %q but cannot encode it: %v", data, err)
		}
		if !bytes.Equal(back, data) {
			t.Fatalf("decoded %q and encoded %q", data, back)
		}
		if !json.Valid(back) {
			t.Fatalf("encoded %q is not JSON", back)
		}
	})
}

// The most senders the limit allows is allowed: the bound is exact.
func TestMetaTakesExactlyTheMostSendersItAllows(t *testing.T) {
	m := endpointMeta{Declaration: authz.Declaration{RestrictSenders: true, AcceptFrom: distinctAddresses(t, MaxAcceptFrom)}}
	b, err := encodeMeta(m)
	if err != nil {
		t.Fatalf("encodeMeta of %d senders: %v", MaxAcceptFrom, err)
	}
	back, err := decodeMeta(b)
	if err != nil || len(back.Declaration.AcceptFrom) != MaxAcceptFrom {
		t.Errorf("decodeMeta = %d senders, %v", len(back.Declaration.AcceptFrom), err)
	}
}
