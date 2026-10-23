package adapterproto_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// TestMethodTable: every method with its direction and kind, written out from
// the protocol's method list.
func TestMethodTable(t *testing.T) {
	type entry struct {
		dir          adapterproto.Direction
		notification bool
	}
	d2a, a2d, both := adapterproto.DaemonToAdapter, adapterproto.AdapterToDaemon, adapterproto.Both
	want := map[string]entry{
		"initialize": {d2a, false}, "ping": {both, false}, "$/cancel_request": {both, true}, "shutdown": {d2a, false},
		"adapter.start": {d2a, false}, "adapter.probe": {d2a, false}, "adapter.availability": {a2d, true},
		"session.create": {d2a, false}, "checkpoint.put": {a2d, false},
		"prompt.deliver": {d2a, false}, "prompt.persisted": {a2d, true},
		"turn.started": {a2d, true}, "turn.ended": {a2d, true},
		"session.interrupt": {d2a, false}, "session.close": {d2a, false}, "prompt.reconcile": {d2a, false},
		"recv.observed": {a2d, false}, "native_request.handled": {a2d, true},
		"endpoint.register": {a2d, false}, "endpoint.unregister": {a2d, false}, "endpoint.pending": {d2a, true},
		"endpoint.recv": {a2d, false}, "endpoint.ack": {a2d, false}, "endpoint.send": {a2d, false}, "endpoint.message_status": {a2d, false},
	}
	got := adapterproto.Methods()
	if len(got) != len(want) {
		t.Fatalf("%d methods, want %d", len(got), len(want))
	}
	for _, m := range got {
		w, ok := want[m.Name]
		if !ok {
			t.Errorf("unexpected method %q", m.Name)
			continue
		}
		if m.Direction != w.dir || m.Notification != w.notification {
			t.Errorf("%s: direction %q notification %v, want %q %v", m.Name, m.Direction, m.Notification, w.dir, w.notification)
		}
		if m.NewParams() == nil {
			t.Errorf("%s has no params type", m.Name)
		}
		if (m.NewResult() == nil) != w.notification {
			t.Errorf("%s: result type presence is wrong", m.Name)
		}
		if info, ok := adapterproto.Lookup(m.Name); !ok || info.Name != m.Name {
			t.Errorf("Lookup(%q) failed", m.Name)
		}
	}
	if _, ok := adapterproto.Lookup("nope"); ok {
		t.Error("Lookup found a method that does not exist")
	}
	if _, err := mustLookup(t, "ping").DecodeResult([]byte(`{}`)); err != nil {
		t.Errorf("ping result: %v", err)
	}
	if _, err := mustLookup(t, "turn.ended").DecodeResult([]byte(`{}`)); err == nil {
		t.Error("a notification decoded a result")
	}
}

func mustLookup(t *testing.T, name string) adapterproto.MethodInfo {
	t.Helper()
	m, ok := adapterproto.Lookup(name)
	if !ok {
		t.Fatalf("no method %q", name)
	}
	return m
}

func TestDirection(t *testing.T) {
	for _, tc := range []struct {
		d          adapterproto.Direction
		fromDaemon bool
		ok         bool
	}{
		{adapterproto.DaemonToAdapter, true, true}, {adapterproto.DaemonToAdapter, false, false},
		{adapterproto.AdapterToDaemon, true, false}, {adapterproto.AdapterToDaemon, false, true},
		{adapterproto.Both, true, true}, {adapterproto.Both, false, true},
		{"sideways", true, false}, {"sideways", false, false},
	} {
		if tc.d.CalledBy(tc.fromDaemon) != tc.ok {
			t.Errorf("%q.CalledBy(fromDaemon=%v) = %v", tc.d, tc.fromDaemon, !tc.ok)
		}
	}
	if len(adapterproto.Directions()) != 3 {
		t.Error("Directions lists three values")
	}
}

// TestDecodeStrictness: the strict rules apply to every message type, shown on a
// message with required and optional fields of several shapes.
func TestDecodeStrictness(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid", `{"endpoint":"` + ep1 + `","attempt":"` + attempt1 + `","text":"hi"}`, true},
		{"unknown field", `{"endpoint":"` + ep1 + `","attempt":"` + attempt1 + `","text":"hi","x":1}`, false},
		{"field in another case", `{"endpoint":"` + ep1 + `","attempt":"` + attempt1 + `","Text":"hi"}`, false},
		{"missing field", `{"endpoint":"` + ep1 + `","text":"hi"}`, false},
		{"null field", `{"endpoint":"` + ep1 + `","attempt":null,"text":"hi"}`, false},
		{"wrong type", `{"endpoint":"` + ep1 + `","attempt":5,"text":"hi"}`, false},
		{"duplicate key", `{"endpoint":"` + ep1 + `","attempt":"` + attempt1 + `","text":"hi","text":"ho"}`, false},
		{"not an object", `[]`, false},
		{"trailing data", `{"endpoint":"` + ep1 + `","attempt":"` + attempt1 + `","text":"hi"} {}`, false},
		{"invalid UTF-8", "{\"endpoint\":\"" + ep1 + "\",\"attempt\":\"" + attempt1 + "\",\"text\":\"\xff\"}", false},
		{"valid but breaks a rule", `{"endpoint":"","attempt":"` + attempt1 + `","text":"hi"}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var p adapterproto.PromptDeliverParams
			err := adapterproto.Decode([]byte(tc.in), &p)
			if (err == nil) != tc.ok {
				t.Fatalf("Decode = %v, want ok=%v", err, tc.ok)
			}
			var ve *adapterproto.ValidationError
			if err != nil && !errors.As(err, &ve) {
				t.Errorf("error is %T, want a *ValidationError", err)
			}
		})
	}
	t.Run("an optional field present but empty", func(t *testing.T) {
		var r adapterproto.RegisterParams
		if err := adapterproto.Decode([]byte(`{"key":"k","display_name":"n","description":""}`), &r); err == nil {
			t.Fatal("explicit empty description accepted")
		}
	})
	t.Run("an optional pointer field accepts zero", func(t *testing.T) {
		var r adapterproto.RecvParams
		if err := adapterproto.Decode([]byte(`{"endpoint":"`+ep1+`","wait_s":0}`), &r); err != nil || r.WaitS == nil || *r.WaitS != 0 {
			t.Fatalf("Decode = %v, %+v", err, r)
		}
	})
	t.Run("a required list may be empty", func(t *testing.T) {
		var r adapterproto.StartResult
		if err := adapterproto.Decode([]byte(`{"sessions":[]}`), &r); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a raw state keeps its content", func(t *testing.T) {
		var r adapterproto.CheckpointPutParams
		in := `{"scope":"session","id":"` + ep1 + `","state":{"a":null,"b":[1,2.5,{"c":"d"}]}}`
		if err := adapterproto.Decode([]byte(in), &r); err != nil {
			t.Fatal(err)
		}
		if string(r.State) != `{"a":null,"b":[1,2.5,{"c":"d"}]}` {
			t.Fatalf("state = %s", r.State)
		}
	})
}

func TestEncodeRefusesInvalid(t *testing.T) {
	if _, err := adapterproto.Encode(adapterproto.PromptDeliverParams{}); err == nil {
		t.Fatal("encoded an invalid message")
	}
	b, err := adapterproto.Encode(adapterproto.PromptDeliverParams{Endpoint: ep1, Attempt: attempt1, Text: "a < b & c"})
	if err != nil {
		t.Fatal(err)
	}
	var back adapterproto.PromptDeliverParams
	if err := adapterproto.Decode(b, &back); err != nil || back.Text != "a < b & c" {
		t.Fatalf("round trip = %+v, %v", back, err)
	}
}

// TestRequiredListsEncodeAsLists: a required list built as nil would encode as
// null, which the schema forbids; Validate refuses it first.
func TestRequiredListsEncodeAsLists(t *testing.T) {
	if _, err := adapterproto.Encode(adapterproto.StartResult{}); err == nil {
		t.Fatal("a nil required list was encoded")
	}
	b, err := adapterproto.Encode(adapterproto.StartResult{Sessions: []adapterproto.SessionState{}})
	if err != nil || string(b) != `{"sessions":[]}` {
		t.Fatalf("Encode = %s, %v", b, err)
	}
}

// TestEnumTables: each enum is a closed set of distinct lowercase words and its
// Valid method agrees with the table.
func TestEnumTables(t *testing.T) {
	tables := adapterproto.EnumTables()
	if len(tables) < 25 {
		t.Fatalf("only %d enums", len(tables))
	}
	for name, vals := range tables {
		seen := map[string]bool{}
		for _, v := range vals {
			if v == "" || v != strings.ToLower(v) || strings.ContainsAny(v, " -") {
				t.Errorf("%s: value %q is not a snake_case word", name, v)
			}
			if seen[v] {
				t.Errorf("%s: %q twice", name, v)
			}
			seen[v] = true
		}
	}
	// Valid agrees with the table for a few enums, by reflection over their names.
	for name, typ := range map[string]reflect.Type{
		"Frontend": reflect.TypeFor[adapterproto.Frontend](), "PromptStage": reflect.TypeFor[adapterproto.PromptStage](),
		"GroundsCode": reflect.TypeFor[adapterproto.GroundsCode](), "ItemAck": reflect.TypeFor[adapterproto.ItemAck](),
		"RejectReason": reflect.TypeFor[adapterproto.RejectReason](), "ControlSignal": reflect.TypeFor[adapterproto.ControlSignal](),
	} {
		valid := func(s string) bool {
			v := reflect.New(typ).Elem()
			v.SetString(s)
			return v.Interface().(interface{ Valid() bool }).Valid()
		}
		for _, s := range tables[name] {
			if !valid(s) {
				t.Errorf("%s.Valid(%q) = false", name, s)
			}
		}
		for _, s := range []string{"", "Cli", "nope", " cli"} {
			if valid(s) {
				t.Errorf("%s.Valid(%q) = true", name, s)
			}
		}
	}
}

// decodeAny decodes data into the params or result of the method and returns the
// canonical encoding, or an error.
func canonical(m adapterproto.MethodInfo, result bool, data []byte) ([]byte, error) {
	var v adapterproto.Message
	var err error
	if result {
		v, err = m.DecodeResult(data)
	} else {
		v, err = m.DecodeParams(data)
	}
	if err != nil {
		return nil, err
	}
	return adapterproto.Encode(v)
}

// FuzzDecodeMessages feeds arbitrary bytes to the decoder of each method. A
// message that decodes must encode, and the encoding must decode to the same
// encoding (it is canonical); nothing may panic.
func FuzzDecodeMessages(f *testing.F) {
	methods := adapterproto.Methods()
	seeds := []string{
		`{}`, `{"endpoint":"` + ep1 + `"}`, `{"endpoint":"` + ep1 + `","attempt":"` + attempt1 + `","text":"hi"}`,
		`{"sessions":[]}`, `{"outcome":"created","native_ref":"n","frontends":["cli"]}`, `{"stage":"refused","reason":"busy"}`,
		`{"lease":"` + lease1 + `","messages":[],"more":0}`, `{"page":{"lease":"` + lease1 + `","messages":[],"more":0}}`,
		`{"verdict":"found","turn":{"ref":"t","state":"running"},"grounds":{"code":"attempt_in_record"}}`,
		`{"protocol":{"min":1,"max":1},"daemon":{"version":"0.1.0"},"instance":{"id":"w","lock_path":"/l"}}`,
		`{"conditions":[]}`, `{"request_id":3}`, `{"request_id":"x"}`, `{"scope":"session","id":"e","state":{}}`,
		`{"recipients":[{"to":"a","state":"queued"}],"control_reply":{"signal":"session.close","outcome":"closed"}}`,
	}
	for i := range methods {
		for _, s := range seeds {
			f.Add(uint8(i), false, []byte(s))
			f.Add(uint8(i), true, []byte(s))
		}
	}
	f.Fuzz(func(t *testing.T, sel uint8, result bool, data []byte) {
		m := methods[int(sel)%len(methods)]
		if result && m.Notification {
			return
		}
		first, err := canonical(m, result, data)
		if err != nil {
			return
		}
		second, err := canonical(m, result, first)
		if err != nil {
			t.Fatalf("%s: the canonical encoding %s no longer decodes: %v", m.Name, first, err)
		}
		if string(first) != string(second) {
			t.Fatalf("%s: encoding is not stable\n first:  %s\n second: %s", m.Name, first, second)
		}
		if !json.Valid(first) {
			t.Fatalf("%s: encoding is not JSON: %s", m.Name, first)
		}
	})
}

func ExampleNegotiate() {
	v, err := adapterproto.Negotiate(adapterproto.Range{Min: 1, Max: 3}, adapterproto.Range{Min: 2, Max: 5})
	fmt.Println(v, err)
	_, err = adapterproto.Negotiate(adapterproto.Range{Min: 4, Max: 5}, adapterproto.Range{Min: 1, Max: 3})
	fmt.Println(err)
	// Output:
	// 3 <nil>
	// adapterproto: no common protocol version: requested 4..5, supported 1..3
}
