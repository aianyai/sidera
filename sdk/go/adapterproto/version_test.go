package adapterproto_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

func TestNegotiate(t *testing.T) {
	r := func(min, max int) adapterproto.Range { return adapterproto.Range{Min: min, Max: max} }
	tests := []struct {
		name      string
		requested adapterproto.Range
		supported adapterproto.Range
		want      int // 0 means no common version
	}{
		{"identical single versions", r(1, 1), r(1, 1), 1},
		{"picks the highest common version", r(1, 5), r(1, 3), 3},
		{"daemon range is the narrower", r(2, 3), r(1, 9), 3},
		{"overlap of one at the daemon's top", r(1, 2), r(2, 4), 2},
		{"overlap of one at the adapter's top", r(3, 6), r(1, 3), 3},
		{"daemon is newer than the adapter", r(2, 3), r(1, 1), 0},
		{"daemon is older than the adapter", r(1, 1), r(2, 3), 0},
		{"adjacent ranges do not overlap", r(1, 2), r(3, 4), 0},
		{"large versions", r(65534, 65535), r(65535, 65535), 65535},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := adapterproto.Negotiate(tc.requested, tc.supported)
			if tc.want == 0 {
				var nc *adapterproto.NoCommonVersionError
				if !errors.As(err, &nc) {
					t.Fatalf("Negotiate = %d, %v, want a *NoCommonVersionError", got, err)
				}
				// The error carries both ranges, as the unsupported-version
				// error data requires, whichever side asked.
				if nc.Data.Requested != tc.requested || nc.Data.Supported != tc.supported {
					t.Fatalf("error data = %+v", nc.Data)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Negotiate = %d, %v, want %d", got, err, tc.want)
			}
		})
	}
}

func TestNegotiateRejectsInvalidRanges(t *testing.T) {
	good := adapterproto.Range{Min: 1, Max: 1}
	for _, bad := range []adapterproto.Range{{0, 1}, {2, 1}, {1, 65536}, {-1, 1}, {}} {
		if _, err := adapterproto.Negotiate(bad, good); err == nil {
			t.Errorf("Negotiate(%+v, %+v) succeeded", bad, good)
		}
		if _, err := adapterproto.Negotiate(good, bad); err == nil {
			t.Errorf("Negotiate(%+v, %+v) succeeded", good, bad)
		}
	}
	var nc *adapterproto.NoCommonVersionError
	_, err := adapterproto.Negotiate(adapterproto.Range{Min: 2, Max: 1}, good)
	if errors.As(err, &nc) {
		t.Error("an invalid range was reported as a version mismatch")
	}
	if !errors.Is(err, adapterproto.ErrBeyondSchema) {
		t.Error("min above max is a rule the schema cannot state")
	}
}

func TestSupportedRangeIsNegotiable(t *testing.T) {
	s := adapterproto.SupportedRange()
	if s.Min != adapterproto.ProtocolMin || s.Max != adapterproto.ProtocolMax || s.Validate() != nil {
		t.Fatalf("SupportedRange = %+v", s)
	}
	if v, err := adapterproto.Negotiate(s, s); err != nil || v != s.Max {
		t.Fatalf("Negotiate(own, own) = %d, %v", v, err)
	}
}

// TestUnsupportedVersionErrorShape: the error response of a failed negotiation is
// a valid error object whose data is the two ranges.
func TestUnsupportedVersionErrorShape(t *testing.T) {
	_, err := adapterproto.Negotiate(adapterproto.Range{Min: 2, Max: 3}, adapterproto.Range{Min: 1, Max: 1})
	var nc *adapterproto.NoCommonVersionError
	if !errors.As(err, &nc) {
		t.Fatal(err)
	}
	obj := adapterproto.NewUnsupportedVersionError(nc)
	if obj.Code != -32022 {
		t.Fatalf("code = %d", obj.Code)
	}
	if err := obj.Validate(); err != nil {
		t.Fatal(err)
	}
	var data map[string]map[string]int
	if err := json.Unmarshal(obj.Data, &data); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]int{"requested": {"min": 2, "max": 3}, "supported": {"min": 1, "max": 1}}
	if len(data) != 2 || data["requested"]["min"] != 2 || data["requested"]["max"] != 3 || data["supported"]["min"] != 1 || data["supported"]["max"] != 1 {
		t.Fatalf("data = %v, want %v", data, want)
	}
}

func TestRangeValidate(t *testing.T) {
	tests := []struct {
		name   string
		r      adapterproto.Range
		wantOK bool
		beyond bool
	}{
		{"one version", adapterproto.Range{Min: 1, Max: 1}, true, false},
		{"a span", adapterproto.Range{Min: 1, Max: 65535}, true, false},
		{"min zero", adapterproto.Range{Min: 0, Max: 1}, false, false},
		{"max zero", adapterproto.Range{Min: 0, Max: 0}, false, false},
		{"max above the bound", adapterproto.Range{Min: 1, Max: 65536}, false, false},
		{"negative min", adapterproto.Range{Min: -3, Max: 1}, false, false},
		{"min above max", adapterproto.Range{Min: 3, Max: 2}, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.r.Validate()
			if (err == nil) != tc.wantOK {
				t.Fatalf("Validate(%+v) = %v", tc.r, err)
			}
			if err != nil && errors.Is(err, adapterproto.ErrBeyondSchema) != tc.beyond {
				t.Fatalf("beyond-schema = %v, want %v (%v)", !tc.beyond, tc.beyond, err)
			}
		})
	}
}

func TestRangeContains(t *testing.T) {
	r := adapterproto.Range{Min: 2, Max: 4}
	for v, want := range map[int]bool{1: false, 2: true, 3: true, 4: true, 5: false} {
		if r.Contains(v) != want {
			t.Errorf("Contains(%d) = %v", v, !want)
		}
	}
}

func TestApplicationCodeTable(t *testing.T) {
	// The numbers are part of the protocol; written out here independently of the
	// constants.
	want := map[int]string{
		1001: "unknown_endpoint", 1002: "wrong_endpoint_kind", 1003: "capability_not_declared", 1004: "unknown_attempt",
		1005: "attempt_conflict", 1006: "checkpoint_too_large", 1007: "endpoint_limit", 1008: "invalid_address",
		1009: "send_refused", 1010: "lease_unknown", 1011: "instance_locked", 1012: "node_busy",
	}
	got := map[int]string{}
	for _, c := range adapterproto.ApplicationCodes() {
		if _, dup := got[c.Code]; dup {
			t.Errorf("code %d listed twice", c.Code)
		}
		got[c.Code] = c.Name
		if !peer.IsApplicationCode(c.Code) {
			t.Errorf("code %d %s is outside the range the peer reserves for the application", c.Code, c.Name)
		}
		if peer.CodeName(c.Code) != "" {
			t.Errorf("code %d %s is also a code of the peer", c.Code, c.Name)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("%d codes, want %d", len(got), len(want))
	}
	for code, name := range want {
		if got[code] != name {
			t.Errorf("code %d: %q, want %q", code, got[code], name)
		}
		if !adapterproto.KnownApplicationCode(code) {
			t.Errorf("KnownApplicationCode(%d) = false", code)
		}
	}
	// The codes of the peer are not application codes, and nothing else is.
	for _, code := range append([]int{0, 1, -32000, -32099, 999, 1013, 2001, -32005, -32701}, peer.Codes()...) {
		if adapterproto.KnownApplicationCode(code) {
			t.Errorf("KnownApplicationCode(%d) = true", code)
		}
	}
	// Constants match the table.
	consts := map[int]int{
		adapterproto.CodeUnknownEndpoint: 1001, adapterproto.CodeWrongEndpointKind: 1002, adapterproto.CodeCapabilityNotDeclared: 1003,
		adapterproto.CodeUnknownAttempt: 1004, adapterproto.CodeAttemptConflict: 1005, adapterproto.CodeCheckpointTooLarge: 1006,
		adapterproto.CodeEndpointLimit: 1007, adapterproto.CodeInvalidAddress: 1008, adapterproto.CodeSendRefused: 1009,
		adapterproto.CodeLeaseUnknown: 1010, adapterproto.CodeInstanceLocked: 1011, adapterproto.CodeNodeBusy: 1012,
	}
	for c, w := range consts {
		if c != w {
			t.Errorf("constant is %d, want %d", c, w)
		}
	}
}

func TestErrorObjectValidate(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	tests := []struct {
		name string
		e    adapterproto.ErrorObject
		ok   bool
	}{
		{"a plain error", adapterproto.ErrorObject{Code: peer.CodeMethodNotFound, Message: "no such method"}, true},
		{"unknown code", adapterproto.ErrorObject{Code: 4242, Message: "x"}, false},
		{"code zero", adapterproto.ErrorObject{Message: "x"}, false},
		{"empty message", adapterproto.ErrorObject{Code: -32603}, false},
		{"message at the limit", adapterproto.ErrorObject{Code: -32603, Message: repeat("m", 1024)}, true},
		{"message over the limit", adapterproto.ErrorObject{Code: -32603, Message: repeat("m", 1025)}, false},
		{"an empty object as data on a code without a data shape", adapterproto.ErrorObject{Code: 1001, Message: "x", Data: raw(`{}`)}, true},
		{"details as data on invalid params", adapterproto.ErrorObject{Code: peer.CodeInvalidParams, Message: "x", Data: raw(`{"field":"to","errors":[1,null,{"a":"b"}]}`)}, true},
		{"details as data on an internal error", adapterproto.ErrorObject{Code: peer.CodeInternalError, Message: "x", Data: raw(`{"trace":"abc"}`)}, true},
		{"a string as data on a code without a data shape", adapterproto.ErrorObject{Code: peer.CodeInternalError, Message: "x", Data: raw(`"abc"`)}, false},
		{"a list as data on a code without a data shape", adapterproto.ErrorObject{Code: peer.CodeInvalidParams, Message: "x", Data: raw(`[]`)}, false},
		{"null as data on a code without a data shape", adapterproto.ErrorObject{Code: peer.CodeInvalidParams, Message: "x", Data: raw(`null`)}, false},
		{"data that repeats a key on a code without a data shape", adapterproto.ErrorObject{Code: peer.CodeInvalidParams, Message: "x", Data: raw(`{"a":1,"a":2}`)}, false},
		{"version error with data", adapterproto.ErrorObject{Code: -32022, Message: "x", Data: raw(`{"requested":{"min":2,"max":3},"supported":{"min":1,"max":1}}`)}, true},
		{"version error without data", adapterproto.ErrorObject{Code: -32022, Message: "x"}, false},
		{"version error with half the data", adapterproto.ErrorObject{Code: -32022, Message: "x", Data: raw(`{"requested":{"min":2,"max":3}}`)}, false},
		{"version error with extra data", adapterproto.ErrorObject{Code: -32022, Message: "x", Data: raw(`{"requested":{"min":2,"max":3},"supported":{"min":1,"max":1},"x":1}`)}, false},
		{"version error with a reversed range", adapterproto.ErrorObject{Code: -32022, Message: "x", Data: raw(`{"requested":{"min":3,"max":2},"supported":{"min":1,"max":1}}`)}, false},
		{"version error with null data", adapterproto.ErrorObject{Code: -32022, Message: "x", Data: raw(`null`)}, false},
		{"address error with data", adapterproto.ErrorObject{Code: 1008, Message: "x", Data: raw(`{"field":"to[0]"}`)}, true},
		{"address error with empty field", adapterproto.ErrorObject{Code: 1008, Message: "x", Data: raw(`{"field":""}`)}, false},
		{"address error without data", adapterproto.ErrorObject{Code: 1008, Message: "x"}, false},
		{"send error with data", adapterproto.ErrorObject{Code: 1009, Message: "x", Data: raw(`{"reason":"body_too_large"}`)}, true},
		{"send error with a reason that is not a word", adapterproto.ErrorObject{Code: 1009, Message: "x", Data: raw(`{"reason":"Body too large"}`)}, false},
		{"send error with a reason that is too long", adapterproto.ErrorObject{Code: 1009, Message: "x", Data: raw(`{"reason":"` + repeat("a", 65) + `"}`)}, false},
		{"busy error with a reason", adapterproto.ErrorObject{Code: 1012, Message: "x", Data: raw(`{"reason":"node_queue_full"}`)}, true},
		{"busy error with the last reason", adapterproto.ErrorObject{Code: 1012, Message: "x", Data: raw(`{"reason":"recovering"}`)}, true},
		{"busy error without data", adapterproto.ErrorObject{Code: 1012, Message: "x"}, false},
		{"busy error with null data", adapterproto.ErrorObject{Code: 1012, Message: "x", Data: raw(`null`)}, false},
		{"busy error with empty data", adapterproto.ErrorObject{Code: 1012, Message: "x", Data: raw(`{}`)}, false},
		{"busy error with a reason this version does not send", adapterproto.ErrorObject{Code: 1012, Message: "x", Data: raw(`{"reason":"disk_on_fire"}`)}, true},
		{"busy error with a reason that is not a word", adapterproto.ErrorObject{Code: 1012, Message: "x", Data: raw(`{"reason":"Disk full"}`)}, false},
		{"busy error with a reason that is too long", adapterproto.ErrorObject{Code: 1012, Message: "x", Data: raw(`{"reason":"` + repeat("a", 65) + `"}`)}, false},
		{"busy error with an empty reason", adapterproto.ErrorObject{Code: 1012, Message: "x", Data: raw(`{"reason":""}`)}, false},
		{"busy error with a reason in capitals", adapterproto.ErrorObject{Code: 1012, Message: "x", Data: raw(`{"reason":"Loop_busy"}`)}, false},
		{"busy error with extra data", adapterproto.ErrorObject{Code: 1012, Message: "x", Data: raw(`{"reason":"loop_busy","retry_after":1}`)}, false},
		{"busy error with a reason that is not a string", adapterproto.ErrorObject{Code: 1012, Message: "x", Data: raw(`{"reason":1}`)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.e.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

// TestErrorObjectKnowsEveryCode: the codes of the peer and the application codes
// are all the codes an error object may carry.
func TestErrorObjectKnowsEveryCode(t *testing.T) {
	versionData := json.RawMessage(`{"requested":{"min":2,"max":3},"supported":{"min":1,"max":1}}`)
	addressData := json.RawMessage(`{"field":"to"}`)
	sendData := json.RawMessage(`{"reason":"too_many_recipients"}`)
	busyData := json.RawMessage(`{"reason":"storage_full"}`)
	dataOf := func(code int) json.RawMessage {
		switch code {
		case peer.CodeUnsupportedProtocolVersion:
			return versionData
		case adapterproto.CodeInvalidAddress:
			return addressData
		case adapterproto.CodeSendRefused:
			return sendData
		case adapterproto.CodeNodeBusy:
			return busyData
		}
		return nil
	}
	codes := peer.Codes()
	for _, c := range adapterproto.ApplicationCodes() {
		codes = append(codes, c.Code)
	}
	for _, code := range codes {
		e := adapterproto.ErrorObject{Code: code, Message: "m", Data: dataOf(code)}
		if err := e.Validate(); err != nil {
			t.Errorf("code %d: %v", code, err)
		}
	}
	// The object is the peer's, so one converts to the other without copying.
	e := adapterproto.NewUnsupportedVersionError(&adapterproto.NoCommonVersionError{})
	if pe := (*peer.Error)(&e); pe.Code != peer.CodeUnsupportedProtocolVersion {
		t.Errorf("converted code %d", pe.Code)
	}
}

// TestBusyReasonTable: the reasons this version of the node sends in a node_busy
// error, written out in order. The names are those the engine gives its refusals
// that left no effect, so nothing is translated on the way to the adapter; a name
// once sent is never renamed or removed, and each one validates as the data of
// the code. A reader accepts any reason word, so the list constrains the sender
// only.
func TestBusyReasonTable(t *testing.T) {
	want := []string{"node_queue_full", "storage_full", "storage_unavailable", "clock_rollback", "loop_busy", "recovering"}
	got := adapterproto.BusyReasons()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BusyReasons() = %v, want %v", got, want)
	}
	for _, why := range want {
		e := adapterproto.ErrorObject{Code: adapterproto.CodeNodeBusy, Message: "m", Data: json.RawMessage(`{"reason":"` + why + `"}`)}
		if err := e.Validate(); err != nil {
			t.Errorf("reason %s: %v", why, err)
		}
	}
	// The list handed out is a copy: changing it changes nothing for the next caller.
	got[0] = "changed"
	if adapterproto.BusyReasons()[0] != "node_queue_full" {
		t.Error("BusyReasons shares its backing array with its caller")
	}
}

// TestNodeBusyIsNoEffect: the peer's effect rule treats every application code as
// no effect, which is what node_busy promises: the node refused before anything
// was recorded, and the request may be sent again later.
func TestNodeBusyIsNoEffect(t *testing.T) {
	err := peer.NewError(adapterproto.CodeNodeBusy, "busy").WithData(map[string]string{"reason": "loop_busy"})
	if !peer.NoEffect(err) || peer.ResultUnknown(err) {
		t.Errorf("NoEffect = %v, ResultUnknown = %v", peer.NoEffect(err), peer.ResultUnknown(err))
	}
}
