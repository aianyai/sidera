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

// The declaration field of the endpoint's record holds the declaration alone, as
// one compact JSON object. The display name and the description are fields of
// the record, so a text that carries them is not a declaration.
func TestDeclarationGoldenVectors(t *testing.T) {
	a1, a2 := addrOf(t, nodeText, epText1), addrOf(t, nodeText, epText2)
	for _, tc := range []struct {
		name string
		decl authz.Declaration
		json string
	}{
		{"nothing declared", authz.Declaration{}, `{}`},
		{"every flag set", authz.Declaration{Paused: true, RefuseMultiRecipient: true, RestrictSenders: true, HiddenFromDirectory: true},
			`{"paused":true,"refuse_multi_recipient":true,"restrict_senders":true,"hidden_from_directory":true}`},
		{"a sender list, kept in order", authz.Declaration{RestrictSenders: true, AcceptFrom: []ident.Address{a2, a1}},
			`{"restrict_senders":true,"accept_from":["` + a2.String() + `","` + a1.String() + `"]}`},
		{"a flag and a sender", authz.Declaration{Paused: true, AcceptFrom: []ident.Address{a1}},
			`{"paused":true,"accept_from":["` + a1.String() + `"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := encodeDeclaration(tc.decl)
			if err != nil {
				t.Fatalf("encodeDeclaration: %v", err)
			}
			if string(got) != tc.json {
				t.Errorf("encoded\n%s\nwant\n%s", got, tc.json)
			}
			back, err := decodeDeclaration([]byte(tc.json))
			if err != nil {
				t.Fatalf("decodeDeclaration: %v", err)
			}
			again, err := encodeDeclaration(back)
			if err != nil || !bytes.Equal(again, []byte(tc.json)) {
				t.Errorf("decoding then encoding gave %s, %v", again, err)
			}
		})
	}
}

func TestDecodeDeclarationTakesNothingAsDeclaringNothing(t *testing.T) {
	for _, in := range [][]byte{nil, {}} {
		d, err := decodeDeclaration(in)
		if err != nil || d.Paused || d.RestrictSenders || len(d.AcceptFrom) != 0 {
			t.Errorf("decodeDeclaration(%q) = %+v, %v", in, d, err)
		}
	}
}

func TestDecodeDeclarationRefusals(t *testing.T) {
	a1 := addrOf(t, nodeText, epText1).String()
	many := make([]string, MaxAcceptFrom+1)
	for i, a := range distinctAddresses(t, MaxAcceptFrom+1) {
		many[i] = `"` + a.String() + `"`
	}
	for _, tc := range []struct{ name, in string }{
		{"an unknown member", `{"name":"x"}`},
		{"the display name, which the record holds in a field of its own", `{"display_name":"Notes"}`},
		{"the description, which the record holds in a field of its own", `{"description":"d"}`},
		{"a repeated member", `{"paused":true,"paused":true}`},
		{"a member of the wrong type", `{"paused":"yes"}`},
		{"null", `{"accept_from":null}`},
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
		{"invalid UTF-8 in a sender", "{\"accept_from\":[\"\xff\"]}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if d, err := decodeDeclaration([]byte(tc.in)); err == nil {
				t.Errorf("decodeDeclaration(%s) = %+v, want an error", tc.in, d)
			}
		})
	}
}

// What cannot be decoded is not written: the same checks run on the way in.
func TestEncodeDeclarationRefusals(t *testing.T) {
	a1 := addrOf(t, nodeText, epText1)
	dup := []ident.Address{a1, a1}
	big := distinctAddresses(t, MaxAcceptFrom+1)
	for _, tc := range []struct {
		name string
		d    authz.Declaration
	}{
		{"a zero address", authz.Declaration{RestrictSenders: true, AcceptFrom: []ident.Address{{}}}},
		{"the same sender twice", authz.Declaration{RestrictSenders: true, AcceptFrom: dup}},
		{"too many senders", authz.Declaration{RestrictSenders: true, AcceptFrom: big}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if b, err := encodeDeclaration(tc.d); err == nil {
				t.Errorf("encodeDeclaration = %s, want an error", b)
			}
		})
	}
}

// Whatever decodes is canonical: it encodes back to the same bytes. Decoding
// never panics, whatever is on the disk.
func FuzzDecodeDeclaration(f *testing.F) {
	a1, a2 := addrOf(f, nodeText, epText1), addrOf(f, nodeText, epText2)
	for _, d := range []authz.Declaration{
		{},
		{RestrictSenders: true, AcceptFrom: []ident.Address{a1, a2}, Paused: true},
		{HiddenFromDirectory: true, RefuseMultiRecipient: true},
	} {
		b, err := encodeDeclaration(d)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Add([]byte(`{"paused":true,"paused":true}`))
	f.Add([]byte(`{"accept_from":[]}`))
	f.Add([]byte(`{"display_name":"Notes"}`))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := decodeDeclaration(data)
		if err != nil || len(data) == 0 {
			return
		}
		back, err := encodeDeclaration(d)
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
func TestDeclarationTakesExactlyTheMostSendersItAllows(t *testing.T) {
	d := authz.Declaration{RestrictSenders: true, AcceptFrom: distinctAddresses(t, MaxAcceptFrom)}
	b, err := encodeDeclaration(d)
	if err != nil {
		t.Fatalf("encodeDeclaration of %d senders: %v", MaxAcceptFrom, err)
	}
	back, err := decodeDeclaration(b)
	if err != nil || len(back.AcceptFrom) != MaxAcceptFrom {
		t.Errorf("decodeDeclaration = %d senders, %v", len(back.AcceptFrom), err)
	}
}
