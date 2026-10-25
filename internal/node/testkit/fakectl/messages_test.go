package fakectl_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
)

// goldenParams are the params of each message as they are written on the wire,
// written out by hand. The control channel is spoken by separate programs (the
// fixture and the fakes it starts), so the names of the members are a contract.
var goldenParams = []struct {
	name   string
	method string
	json   string
	value  any
}{
	{"hello of an adapter", fakectl.MethodHello, `{"role":"adapter","id":"fake","pid":4242}`, fakectl.HelloParams{Role: fakectl.RoleAdapter, ID: "fake", PID: 4242}},
	{"hello of a runtime", fakectl.MethodHello, `{"role":"runtime","id":"n-0123","pid":7}`, fakectl.HelloParams{Role: fakectl.RoleRuntime, ID: "n-0123", PID: 7}},
	{"clock set", fakectl.MethodClockSet, `{"to_ms":1792832400000}`, fakectl.ClockSetParams{ToMS: 1792832400000}},
	{"step set", fakectl.MethodStepSet, `{"step":"a sends to b"}`, fakectl.StepSetParams{Step: "a sends to b"}},
	{"knob with arguments", fakectl.MethodKnob, `{"name":"create.next","args":{"kind":"hold"}}`, fakectl.KnobParams{Name: "create.next", Args: json.RawMessage(`{"kind":"hold"}`)}},
	{"knob without arguments", fakectl.MethodKnob, `{"name":"hang"}`, fakectl.KnobParams{Name: "hang"}},
	{"report", fakectl.MethodReport, `{"name":"paused","detail":"create.after-native"}`, fakectl.ReportParams{Name: "paused", Detail: "create.after-native"}},
	{"report without detail", fakectl.MethodReport, `{"name":"ready"}`, fakectl.ReportParams{Name: "ready"}},
}

func TestGoldenParams(t *testing.T) {
	for _, g := range goldenParams {
		t.Run(g.name, func(t *testing.T) {
			got, err := fakectl.DecodeParams(g.method, []byte(g.json))
			if err != nil {
				t.Fatalf("DecodeParams: %v", err)
			}
			if !reflect.DeepEqual(got, g.value) {
				t.Fatalf("decoded %#v, want %#v", got, g.value)
			}
			enc, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(enc, []byte(g.json)) {
				t.Fatalf("encoded %s, want %s", enc, g.json)
			}
		})
	}
}

// TestDecodeParamsRejects: one row per rule of the control messages.
func TestDecodeParamsRejects(t *testing.T) {
	rows := []struct {
		rule   string
		method string
		json   string
	}{
		{"an unknown member is an error", fakectl.MethodHello, `{"role":"adapter","id":"a","pid":1,"extra":true}`},
		{"a missing member is an error", fakectl.MethodHello, `{"role":"adapter","id":"a"}`},
		{"a role outside the vocabulary is an error", fakectl.MethodHello, `{"role":"daemon","id":"a","pid":1}`},
		{"an empty id is an error", fakectl.MethodHello, `{"role":"adapter","id":"","pid":1}`},
		{"an id with a space is an error", fakectl.MethodHello, `{"role":"adapter","id":"a b","pid":1}`},
		{"an id of 129 characters is an error", fakectl.MethodHello, `{"role":"adapter","id":"` + strings.Repeat("a", 129) + `","pid":1}`},
		{"a negative pid is an error", fakectl.MethodHello, `{"role":"adapter","id":"a","pid":-1}`},
		{"null is not a value", fakectl.MethodHello, `{"role":null,"id":"a","pid":1}`},
		{"a repeated member is an error", fakectl.MethodClockSet, `{"to_ms":1,"to_ms":2}`},
		{"a negative time is an error", fakectl.MethodClockSet, `{"to_ms":-1}`},
		{"a fractional time is an error", fakectl.MethodClockSet, `{"to_ms":1.5}`},
		{"a step of 257 characters is an error", fakectl.MethodStepSet, `{"step":"` + strings.Repeat("s", 257) + `"}`},
		{"a knob name with capitals is an error", fakectl.MethodKnob, `{"name":"Hang"}`},
		{"an empty knob name is an error", fakectl.MethodKnob, `{"name":""}`},
		{"knob arguments that are not an object are an error", fakectl.MethodKnob, `{"name":"hang","args":[1]}`},
		{"knob arguments that are null are an error", fakectl.MethodKnob, `{"name":"hang","args":null}`},
		{"knob arguments that are an empty object are an error: omit them", fakectl.MethodKnob, `{"name":"hang","args":{}}`},
		{"a report detail over 1024 characters is an error", fakectl.MethodReport, `{"name":"x","detail":"` + strings.Repeat("d", 1025) + `"}`},
		{"an unknown method is an error", "no.such.method", `{}`},
		{"a document that is not an object is an error", fakectl.MethodStepSet, `"text"`},
		{"invalid UTF-8 is an error", fakectl.MethodStepSet, "{\"step\":\"\xff\"}"},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if v, err := fakectl.DecodeParams(r.method, []byte(r.json)); err == nil {
				t.Fatalf("accepted %s as %#v", r.json, v)
			}
		})
	}
}

// FuzzDecodeParams: decoding never panics, and what decodes is stable: encoding
// it and decoding again gives the same value.
func FuzzDecodeParams(f *testing.F) {
	for _, g := range goldenParams {
		f.Add(g.method, []byte(g.json))
	}
	f.Add(fakectl.MethodKnob, []byte(`{"name":"a","args":{"b":{"c":[1,2,{"d":null}]}}}`))
	f.Fuzz(func(t *testing.T, method string, data []byte) {
		v, err := fakectl.DecodeParams(method, data)
		if err != nil {
			return
		}
		enc, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("a decoded value does not encode: %v", err)
		}
		again, err := fakectl.DecodeParams(method, enc)
		if err != nil {
			t.Fatalf("an encoded value does not decode: %v\n%s", err, enc)
		}
		if !reflect.DeepEqual(v, again) {
			t.Fatalf("decode, encode, decode changed the value: %#v then %#v", v, again)
		}
	})
}
