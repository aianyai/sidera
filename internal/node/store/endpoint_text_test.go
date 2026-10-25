package store_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/recfile"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The display name and the description of an endpoint record hold exactly what
// the protocol that registers them accepts: the record neither refuses a name an
// adapter may register nor keeps one it may not. The oracle is the protocol's own
// validation of a registration, not a number copied into the test.
func TestEndpointTextIsBoundedAsTheRegistrationIs(t *testing.T) {
	for _, c := range []struct {
		name          string
		display, desc string
	}{
		{"a display name of the most characters, each of two bytes", strings.Repeat("ü", adapterproto.MaxDisplayName), ""},
		{"a display name one character over", strings.Repeat("ü", adapterproto.MaxDisplayName+1), ""},
		{"a description of the most characters, each of three bytes", "n", strings.Repeat("€", adapterproto.MaxDescription)},
		{"a description one character over", "n", strings.Repeat("€", adapterproto.MaxDescription+1)},
		{"a display name of 129 characters of ASCII", strings.Repeat("n", 129), ""},
		{"both at their bounds", strings.Repeat("n", adapterproto.MaxDisplayName), strings.Repeat("n", adapterproto.MaxDescription)},
	} {
		t.Run(c.name, func(t *testing.T) {
			accepted := adapterproto.RegisterParams{Key: "k", DisplayName: c.display, Description: c.desc}.Validate() == nil
			r := directEndpoint(6, "chat")
			r.DisplayName, r.Description = c.display, c.desc
			data, err := store.EncodeRecord(r)
			if (err == nil) != accepted {
				t.Fatalf("the protocol accepts the registration = %v, the record encodes = %v (%v)", accepted, err == nil, err)
			}
			if !accepted {
				return
			}
			got, dmg := store.DecodeRecord(store.EndpointsDir, r.Name(), data)
			if dmg != nil || !equalRecords(got, r) {
				t.Errorf("the record did not come back: %+v\n got  %#v\n want %#v", dmg, got, r)
			}
		})
	}
}

// Nothing is written for a name or a description that is not there, and text
// that is not valid UTF-8 is refused instead of being altered on its way out.
func TestEndpointTextIsOptionalAndValidUTF8(t *testing.T) {
	r := directEndpoint(6, "chat")
	data, err := store.EncodeRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "display_name") || strings.Contains(string(data), "description") {
		t.Errorf("an endpoint without a name or a description wrote them: %q", data)
	}
	for name, edit := range map[string]func(*store.EndpointRecord){
		"a display name that is not UTF-8": func(r *store.EndpointRecord) { r.DisplayName = "B\xffro" },
		"a description that is not UTF-8":  func(r *store.EndpointRecord) { r.Description = "\xc3" },
	} {
		t.Run(name, func(t *testing.T) {
			r := directEndpoint(6, "chat")
			edit(&r)
			if data, err := store.EncodeRecord(r); err == nil {
				t.Errorf("encoded as %q", data)
			}
		})
	}
}

// A body that holds text that is not valid UTF-8 is refused on read, with a
// checksum and a first line that are right for it: the decoder never meets the
// text, because the JSON decoder replaces the bad byte, so it is the check that
// the body is the canonical encoding of the record that refuses it.
func TestEndpointTextThatIsNotUTF8IsRefusedOnRead(t *testing.T) {
	r := directEndpoint(6, "chat")
	r.DisplayName = "Bxro"
	data, err := store.EncodeRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	fr, dmg := recfile.DecodeFrame(data)
	if dmg != nil {
		t.Fatal(dmg)
	}
	if !bytes.Contains(fr.Body, []byte("Bxro")) {
		t.Fatalf("the display name is not in the body: %q", fr.Body)
	}
	body := bytes.Replace(fr.Body, []byte("Bxro"), []byte("B\xffro"), 1)
	bad, err := recfile.EncodeFrame(fr.Kind, fr.Key, body)
	if err != nil {
		t.Fatal(err)
	}
	if rec, dmg := store.DecodeRecord(store.EndpointsDir, r.Name(), bad); dmg == nil {
		t.Errorf("a body with a byte that is not UTF-8 decoded as %#v", rec)
	}
}

// A body whose display name or description is over the bound of the protocol is
// refused on read as it is on write, even when its first line and checksum are
// right for it: a record the protocol could not have registered is not kept.
func TestEndpointTextOverTheBoundIsRefusedOnRead(t *testing.T) {
	r := directEndpoint(6, "chat")
	r.DisplayName, r.Description = "Bxro", "Dxsc"
	data, err := store.EncodeRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	fr, dmg := recfile.DecodeFrame(data)
	if dmg != nil {
		t.Fatal(dmg)
	}
	for _, c := range []struct{ name, from, to string }{
		{"a display name one character over", "Bxro", strings.Repeat("n", adapterproto.MaxDisplayName+1)},
		{"a description one character over", "Dxsc", strings.Repeat("n", adapterproto.MaxDescription+1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := bytes.Replace(fr.Body, []byte(c.from), []byte(c.to), 1)
			if bytes.Equal(body, fr.Body) {
				t.Fatalf("%q is not in the body %q", c.from, fr.Body)
			}
			bad, err := recfile.EncodeFrame(fr.Kind, fr.Key, body)
			if err != nil {
				t.Fatal(err)
			}
			if rec, dmg := store.DecodeRecord(store.EndpointsDir, r.Name(), bad); dmg == nil {
				t.Errorf("an over-long text decoded as %#v", rec)
			}
		})
	}
}
