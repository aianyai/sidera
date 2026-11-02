package adapterhost_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
)

// A CallID depends on the method and on what the params say, not on how a peer
// happened to write them: the adapter host and a third party build it from two
// encodings of the same request.
func TestCallIDFollowsTheMethodAndTheMeaningOfTheParams(t *testing.T) {
	base := adapterhost.CallID("prompt.deliver", []byte(`{"endpoint":"s-1","text":"a<b>&","n":1.50}`))
	for _, tc := range []struct {
		name, method, params string
		same                 bool
	}{
		{"the same bytes", "prompt.deliver", `{"endpoint":"s-1","text":"a<b>&","n":1.50}`, true},
		{"another order and spacing", "prompt.deliver", `{ "n":1.50, "text":"a<b>&", "endpoint":"s-1" }`, true},
		{"escaped characters", "prompt.deliver", `{"endpoint":"s-1","text":"a\u003cb\u003e\u0026","n":1.50}`, true},
		{"another value", "prompt.deliver", `{"endpoint":"s-1","text":"a<b>&","n":2}`, false},
		{"another method", "session.close", `{"endpoint":"s-1","text":"a<b>&","n":1.50}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := adapterhost.CallID(tc.method, []byte(tc.params)); (got == base) != tc.same {
				t.Fatalf("CallID = %s, the base is %s, want same = %v", got, base, tc.same)
			}
		})
	}
}

// A nested object is told apart by its content and not by the order of its keys.
func TestCallIDOfANestedObjectFollowsItsContent(t *testing.T) {
	a := adapterhost.CallID("m", []byte(`{"o":{"b":1,"a":{"d":2,"c":3}}}`))
	if b := adapterhost.CallID("m", []byte(`{"o":{"a":{"c":3,"d":2},"b":1}}`)); a != b {
		t.Fatalf("the same object in another order has another identity: %s and %s", a, b)
	}
	if b := adapterhost.CallID("m", []byte(`{"o":{"a":{"c":3,"d":4},"b":1}}`)); a == b {
		t.Fatalf("a nested value that differs has the same identity: %s", a)
	}
	if empty := adapterhost.CallID("m", []byte(`{}`)); a == empty {
		t.Fatalf("a nested object has the identity of {}: %s", a)
	}
}

// The host builds an identity from the params it holds, the fake adapter from the
// bytes the sdk's peer wrote on the wire, and the two are copies of one function:
// they must give one string for every kind of value a request carries.
func TestCallIDAgreesWithTheFakeAdapter(t *testing.T) {
	type checkpoint struct {
		Scope string          `json:"scope"`
		State json.RawMessage `json:"state"`
	}
	type body struct {
		Text string         `json:"text"`
		Nums []any          `json:"nums,omitempty"`
		Obj  map[string]any `json:"obj,omitempty"`
	}
	for _, tc := range []struct {
		name   string
		params any
	}{
		{"html-escaped strings", body{Text: "a<b>&</b> \u2028 \"q\" é 😀"}},
		{"floats and large integers", body{Nums: []any{1.50, 0.1, 1e21, uint64(12345678901234567890), int64(-9007199254740993), 100}}},
		{"nested objects", body{Obj: map[string]any{"b": map[string]any{"z": 1, "a": []any{map[string]any{"y": nil, "x": "<"}}}, "a": true}}},
		{"a raw message field", checkpoint{Scope: "session", State: json.RawMessage(` { "phase" : "created", "n" : 1.0, "html": "<x>" } `)}},
		{"an empty object", struct{}{}},
		{"nil params", nil},
		{"null params", json.RawMessage("null")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// What the host holds: encoded with the escaping of encoding/json.
			held, err := json.Marshal(tc.params)
			if err != nil {
				t.Fatal(err)
			}
			// What the peer puts on the wire: no HTML escaping, null as {}.
			var wire bytes.Buffer
			enc := json.NewEncoder(&wire)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(tc.params); err != nil {
				t.Fatal(err)
			}
			wireBytes := bytes.TrimRight(wire.Bytes(), "\n")
			if tc.params == nil || bytes.Equal(wireBytes, []byte("null")) {
				wireBytes = []byte(`{}`)
			}
			if got, want := adapterhost.CallID("m.x", held), fakeadapter.RequestID("m.x", wireBytes); got != want {
				t.Fatalf("the host builds %s, the fake adapter %s (wire %s)", got, want, wireBytes)
			}
		})
	}
	// Params that are absent or null are {} in both.
	empty := adapterhost.CallID("m.x", []byte(`{}`))
	for _, raw := range [][]byte{nil, []byte("null"), []byte(" ")} {
		if got := adapterhost.CallID("m.x", raw); got != empty {
			t.Errorf("host: %q is %s, want %s", raw, got, empty)
		}
		if got := fakeadapter.RequestID("m.x", raw); got != empty {
			t.Errorf("fake: %q is %s, want %s", raw, got, empty)
		}
	}
}
