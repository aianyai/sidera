package peer_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/peer"
)

func TestRequestIDJSON(t *testing.T) {
	tests := []struct {
		in   string
		want peer.RequestID
		ok   bool
	}{
		{`7`, peer.NumberID(7), true},
		{`0`, peer.NumberID(0), true},
		{`-0`, peer.NumberID(0), true},
		{`-12`, peer.NumberID(-12), true},
		{`9007199254740991`, peer.NumberID(1<<53 - 1), true},
		{`-9007199254740991`, peer.NumberID(-(1<<53 - 1)), true},
		{`"probe-2"`, peer.StringID("probe-2"), true},
		{`"é\n"`, peer.StringID("é\n"), true},
		{`"a\"b"`, peer.StringID(`a"b`), true},
		{`"` + strings.Repeat("a", 128) + `"`, peer.StringID(strings.Repeat("a", 128)), true},
		// Characters, not bytes: 128 of these take 256 bytes.
		{`"` + strings.Repeat("é", 128) + `"`, peer.StringID(strings.Repeat("é", 128)), true},
		{`1.5`, peer.RequestID{}, false},
		{`1e2`, peer.RequestID{}, false},
		{`1.0`, peer.RequestID{}, false},
		{`null`, peer.RequestID{}, false},
		{`true`, peer.RequestID{}, false},
		{`[]`, peer.RequestID{}, false},
		{`{}`, peer.RequestID{}, false},
		{`""`, peer.RequestID{}, false},
		{`"` + strings.Repeat("a", 129) + `"`, peer.RequestID{}, false},
		{`"` + strings.Repeat("é", 129) + `"`, peer.RequestID{}, false},
		{`9007199254740992`, peer.RequestID{}, false},
		{`-9007199254740992`, peer.RequestID{}, false},
		{`9223372036854775807`, peer.RequestID{}, false},
		{`99999999999999999999`, peer.RequestID{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.in[:min(len(tc.in), 40)], func(t *testing.T) {
			var got peer.RequestID
			err := json.Unmarshal([]byte(tc.in), &got)
			if (err == nil) != tc.ok {
				t.Fatalf("Unmarshal = %v, want ok=%v", err, tc.ok)
			}
			if err != nil {
				if !errors.Is(err, peer.ErrInvalidRequestID) {
					t.Errorf("error %v does not match ErrInvalidRequestID", err)
				}
				if got != (peer.RequestID{}) {
					t.Errorf("a failed decode left %+v behind", got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("a decoded id fails Validate: %v", err)
			}
			out, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var back peer.RequestID
			if err := json.Unmarshal(out, &back); err != nil || back != got {
				t.Fatalf("round trip: %s -> %+v, %v", out, back, err)
			}
		})
	}
}

func TestRequestIDValidate(t *testing.T) {
	const exact = peer.MaxRequestNumber
	for _, tc := range []struct {
		name string
		id   peer.RequestID
		ok   bool
	}{
		{"zero number", peer.NumberID(0), true},
		{"largest exact number", peer.NumberID(exact), true},
		{"smallest exact number", peer.NumberID(-exact), true},
		{"first number beyond the range", peer.NumberID(exact + 1), false},
		{"first negative number beyond the range", peer.NumberID(-exact - 1), false},
		{"a string", peer.StringID("x"), true},
		{"a string of 128", peer.StringID(strings.Repeat("s", peer.MaxRequestStringLen)), true},
		{"a string of 129", peer.StringID(strings.Repeat("s", peer.MaxRequestStringLen+1)), false},
		{"an empty string", peer.StringID(""), false},
		{"unset", peer.RequestID{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.id.Validate()
			if (err == nil) != tc.ok {
				t.Fatalf("Validate = %v", err)
			}
			if err != nil && !errors.Is(err, peer.ErrInvalidRequestID) {
				t.Errorf("error %v does not match ErrInvalidRequestID", err)
			}
			if _, mErr := json.Marshal(tc.id); (mErr == nil) != tc.ok {
				t.Errorf("Marshal = %v, want ok=%v", mErr, tc.ok)
			}
		})
	}
	if id := peer.NumberID(5); id.IsString() || id.Number() != 5 || id.String() != "5" {
		t.Errorf("number id accessors: %+v", id)
	}
	if id := peer.StringID("x"); !id.IsString() || id.String() != "x" {
		t.Errorf("string id accessors: %+v", id)
	}
}

func TestCancelRequestParamsValidate(t *testing.T) {
	if err := (peer.CancelRequestParams{RequestID: peer.NumberID(3)}).Validate(); err != nil {
		t.Errorf("a valid id: %v", err)
	}
	err := peer.CancelRequestParams{}.Validate()
	if !errors.Is(err, peer.ErrInvalidRequestID) || !strings.HasPrefix(err.Error(), "request_id: ") {
		t.Errorf("an unset id: %v", err)
	}
}
