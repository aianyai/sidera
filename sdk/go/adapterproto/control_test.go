package adapterproto_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// TestControlEnumCodes: the integers and names of the control values, written out
// from the specification of the signals. The integers are what a binary encoding
// will carry, so they must not move.
func TestControlEnumCodes(t *testing.T) {
	check := func(name string, got map[string]uint8, want map[string]uint8) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s: %d values, want %d", name, len(got), len(want))
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %d, want %d", name, k, got[k], v)
			}
		}
	}
	names := func(valid func(uint8) bool, str func(uint8) string) map[string]uint8 {
		out := map[string]uint8{}
		for i := 1; i < 256; i++ {
			if valid(uint8(i)) {
				out[str(uint8(i))] = uint8(i)
			}
		}
		return out
	}
	check("create outcome", names(func(i uint8) bool { return adapterproto.ControlCreateOutcome(i).Valid() }, func(i uint8) string { return adapterproto.ControlCreateOutcome(i).String() }),
		map[string]uint8{"created": 1, "unconfirmed": 2, "failed": 3})
	check("create reason", names(func(i uint8) bool { return adapterproto.ControlCreateReason(i).Valid() }, func(i uint8) string { return adapterproto.ControlCreateReason(i).String() }),
		map[string]uint8{"preset_unknown": 1, "preset_unavailable": 2, "adapter_offline": 3, "runtime_auth": 4, "native_unavailable": 5, "native_error": 6, "ready_timeout": 7, "adapter_lost": 8, "control_unknown": 9, "node_endpoint_limit": 10})
	check("close outcome", names(func(i uint8) bool { return adapterproto.ControlCloseOutcome(i).Valid() }, func(i uint8) string { return adapterproto.ControlCloseOutcome(i).String() }),
		map[string]uint8{"closed": 1, "close_unconfirmed": 2, "unsupported": 3})
	check("close reason", names(func(i uint8) bool { return adapterproto.ControlCloseReason(i).Valid() }, func(i uint8) string { return adapterproto.ControlCloseReason(i).String() }),
		map[string]uint8{"turn_still_running": 1, "adapter_offline": 2, "native_unreachable": 3})
	check("interrupt outcome", names(func(i uint8) bool { return adapterproto.ControlInterruptOutcome(i).Valid() }, func(i uint8) string { return adapterproto.ControlInterruptOutcome(i).String() }),
		map[string]uint8{"stopped": 1, "requested": 2, "not_running": 3, "failed": 4, "unsupported": 5})
	check("interrupt reason", names(func(i uint8) bool { return adapterproto.ControlInterruptReason(i).Valid() }, func(i uint8) string { return adapterproto.ControlInterruptReason(i).String() }),
		map[string]uint8{"adapter_offline": 1, "native_unreachable": 2, "native_error": 3})
	check("activity", names(func(i uint8) bool { return adapterproto.ControlActivity(i).Valid() }, func(i uint8) string { return adapterproto.ControlActivity(i).String() }),
		map[string]uint8{"idle": 1, "busy": 2, "unknown": 3})
	check("condition", names(func(i uint8) bool { return adapterproto.ControlCondition(i).Valid() }, func(i uint8) string { return adapterproto.ControlCondition(i).String() }),
		map[string]uint8{"adapter_offline": 1, "runtime_auth": 2, "native_unavailable": 3, "server_request_rejected": 4, "control_unknown": 5, "version_untested": 6})
	// The constants carry those numbers.
	if adapterproto.CreateReplyCreated != 1 || adapterproto.CreateReplyUnconfirmed != 2 || adapterproto.CreateReplyFailed != 3 ||
		adapterproto.CreateReasonReadyTimeout != 7 || adapterproto.CreateReasonAdapterLost != 8 || adapterproto.CreateReasonControlUnknown != 9 || adapterproto.CreateReasonNodeEndpointLimit != 10 ||
		adapterproto.CloseReplyUnsupported != 3 || adapterproto.InterruptReplyUnsupported != 5 || adapterproto.ActivityUnknown != 3 ||
		adapterproto.ControlConditionVersionUntested != 6 {
		t.Error("a control constant moved")
	}
}

func TestControlEnumText(t *testing.T) {
	var o adapterproto.ControlCreateOutcome
	if err := o.UnmarshalText([]byte("failed")); err != nil || o != adapterproto.CreateReplyFailed {
		t.Fatalf("UnmarshalText = %v, %v", o, err)
	}
	for _, bad := range []string{"", "Failed", "failed ", "unknown", "0", "1"} {
		if err := o.UnmarshalText([]byte(bad)); err == nil {
			t.Errorf("UnmarshalText(%q) succeeded", bad)
		}
	}
	for _, v := range []adapterproto.ControlCreateOutcome{0, 4, 255} {
		if _, err := v.MarshalText(); err == nil {
			t.Errorf("MarshalText(%d) succeeded", v)
		}
		if v.Valid() {
			t.Errorf("%d is valid", v)
		}
		if !strings.HasPrefix(v.String(), "invalid(") {
			t.Errorf("String(%d) = %q", v, v.String())
		}
	}
	b, err := json.Marshal([]adapterproto.ControlCondition{1, 4, 6})
	if err != nil || string(b) != `["adapter_offline","server_request_rejected","version_untested"]` {
		t.Fatalf("Marshal = %s, %v", b, err)
	}
}

func TestControlCreateReplyRules(t *testing.T) {
	r := adapterproto.ControlCreateReply{Outcome: adapterproto.CreateReplyCreated, SessionEndpoint: ep1}
	method(t, func() adapterproto.ControlCreateReply { return r }, []row[adapterproto.ControlCreateReply]{
		{"created with a reason", func(v *adapterproto.ControlCreateReply) { v.Reason = adapterproto.CreateReasonNativeError }, "reason", false},
		{"created without an endpoint", func(v *adapterproto.ControlCreateReply) { v.SessionEndpoint = "" }, "session_endpoint", false},
		{"unconfirmed by timeout", func(v *adapterproto.ControlCreateReply) {
			v.Outcome, v.Reason = adapterproto.CreateReplyUnconfirmed, adapterproto.CreateReasonReadyTimeout
		}, "", false},
		{"unconfirmed because the adapter was lost", func(v *adapterproto.ControlCreateReply) {
			v.Outcome, v.Reason = adapterproto.CreateReplyUnconfirmed, adapterproto.CreateReasonAdapterLost
		}, "", false},
		{"unconfirmed with a failure reason", func(v *adapterproto.ControlCreateReply) {
			v.Outcome, v.Reason = adapterproto.CreateReplyUnconfirmed, adapterproto.CreateReasonNativeError
		}, "reason", false},
		{"unconfirmed without a reason", func(v *adapterproto.ControlCreateReply) { v.Outcome = adapterproto.CreateReplyUnconfirmed }, "reason", false},
		{"unconfirmed without an endpoint", func(v *adapterproto.ControlCreateReply) {
			v.Outcome, v.Reason, v.SessionEndpoint = adapterproto.CreateReplyUnconfirmed, adapterproto.CreateReasonReadyTimeout, ""
		}, "session_endpoint", false},
		{"failed with a failure reason", func(v *adapterproto.ControlCreateReply) {
			v.Outcome, v.Reason, v.SessionEndpoint = adapterproto.CreateReplyFailed, adapterproto.CreateReasonPresetUnknown, ""
		}, "", false},
		{"failed with an unconfirmed reason", func(v *adapterproto.ControlCreateReply) {
			v.Outcome, v.Reason, v.SessionEndpoint = adapterproto.CreateReplyFailed, adapterproto.CreateReasonReadyTimeout, ""
		}, "reason", false},
		{"failed without a reason", func(v *adapterproto.ControlCreateReply) {
			v.Outcome, v.SessionEndpoint = adapterproto.CreateReplyFailed, ""
		}, "reason", false},
		{"failed with an endpoint", func(v *adapterproto.ControlCreateReply) {
			v.Outcome, v.Reason = adapterproto.CreateReplyFailed, adapterproto.CreateReasonNativeError
		}, "session_endpoint", false},
		{"outcome unset", func(v *adapterproto.ControlCreateReply) { v.Outcome = 0 }, "outcome", false},
		{"outcome out of range", func(v *adapterproto.ControlCreateReply) { v.Outcome = 9 }, "outcome", false},
		{"reason out of range", func(v *adapterproto.ControlCreateReply) { v.Reason = 11 }, "reason", false},
		{"failed at the node endpoint limit", func(v *adapterproto.ControlCreateReply) {
			v.Outcome, v.Reason, v.SessionEndpoint = adapterproto.CreateReplyFailed, adapterproto.CreateReasonNodeEndpointLimit, ""
		}, "", false},
		{"node endpoint limit cannot be unconfirmed", func(v *adapterproto.ControlCreateReply) {
			v.Outcome, v.Reason = adapterproto.CreateReplyUnconfirmed, adapterproto.CreateReasonNodeEndpointLimit
		}, "reason", false},
		{"failed because control is unknown", func(v *adapterproto.ControlCreateReply) {
			v.Outcome, v.Reason, v.SessionEndpoint = adapterproto.CreateReplyFailed, adapterproto.CreateReasonControlUnknown, ""
		}, "", false},
		{"failed because control is unknown, with an endpoint", func(v *adapterproto.ControlCreateReply) {
			v.Outcome, v.Reason = adapterproto.CreateReplyFailed, adapterproto.CreateReasonControlUnknown
		}, "session_endpoint", false},
		{"unconfirmed because control is unknown", func(v *adapterproto.ControlCreateReply) {
			v.Outcome, v.Reason = adapterproto.CreateReplyUnconfirmed, adapterproto.CreateReasonControlUnknown
		}, "reason", false},
		{"created because control is unknown", func(v *adapterproto.ControlCreateReply) { v.Reason = adapterproto.CreateReasonControlUnknown }, "reason", false},
	})
	for reason := adapterproto.CreateReasonPresetUnknown; reason <= adapterproto.CreateReasonNativeError; reason++ {
		if !reason.FailureReason() || reason.UnconfirmedReason() {
			t.Errorf("reason %v is in the wrong group", reason)
		}
	}
	// control_unknown comes after the unconfirmed group in number but belongs to
	// the failed group.
	if !adapterproto.CreateReasonControlUnknown.FailureReason() || adapterproto.CreateReasonControlUnknown.UnconfirmedReason() {
		t.Error("control_unknown is not in the failed group")
	}
	if adapterproto.CreateReasonReadyTimeout.FailureReason() || !adapterproto.CreateReasonReadyTimeout.UnconfirmedReason() {
		t.Error("ready_timeout is in the wrong group")
	}
}

func TestControlCloseAndInterruptReplyRules(t *testing.T) {
	method(t, func() adapterproto.ControlCloseReply {
		return adapterproto.ControlCloseReply{Outcome: adapterproto.CloseReplyClosed}
	},
		[]row[adapterproto.ControlCloseReply]{
			{"unsupported", func(v *adapterproto.ControlCloseReply) { v.Outcome = adapterproto.CloseReplyUnsupported }, "", false},
			{"unconfirmed because the turn still runs", func(v *adapterproto.ControlCloseReply) {
				v.Outcome, v.Reason = adapterproto.CloseReplyUnconfirmed, adapterproto.CloseReasonTurnStillRunning
			}, "", false},
			{"unconfirmed because the adapter is offline", func(v *adapterproto.ControlCloseReply) {
				v.Outcome, v.Reason = adapterproto.CloseReplyUnconfirmed, adapterproto.CloseReasonAdapterOffline
			}, "", false},
			{"unconfirmed because the native side is unreachable", func(v *adapterproto.ControlCloseReply) {
				v.Outcome, v.Reason = adapterproto.CloseReplyUnconfirmed, adapterproto.CloseReasonNativeUnreachable
			}, "", false},
			{"unconfirmed without a reason", func(v *adapterproto.ControlCloseReply) { v.Outcome = adapterproto.CloseReplyUnconfirmed }, "reason", false},
			{"closed with a reason", func(v *adapterproto.ControlCloseReply) { v.Reason = adapterproto.CloseReasonAdapterOffline }, "reason", false},
			{"unsupported with a reason", func(v *adapterproto.ControlCloseReply) {
				v.Outcome, v.Reason = adapterproto.CloseReplyUnsupported, adapterproto.CloseReasonAdapterOffline
			}, "reason", false},
			{"outcome unset", func(v *adapterproto.ControlCloseReply) { v.Outcome = 0 }, "outcome", false},
		})
	method(t, func() adapterproto.ControlInterruptReply {
		return adapterproto.ControlInterruptReply{Outcome: adapterproto.InterruptReplyStopped}
	},
		[]row[adapterproto.ControlInterruptReply]{
			{"requested", func(v *adapterproto.ControlInterruptReply) { v.Outcome = adapterproto.InterruptReplyRequested }, "", false},
			{"not running", func(v *adapterproto.ControlInterruptReply) { v.Outcome = adapterproto.InterruptReplyNotRunning }, "", false},
			{"unsupported", func(v *adapterproto.ControlInterruptReply) { v.Outcome = adapterproto.InterruptReplyUnsupported }, "", false},
			{"failed with each reason", func(v *adapterproto.ControlInterruptReply) {
				v.Outcome, v.Reason = adapterproto.InterruptReplyFailed, adapterproto.InterruptReasonNativeError
			}, "", false},
			{"failed without a reason", func(v *adapterproto.ControlInterruptReply) { v.Outcome = adapterproto.InterruptReplyFailed }, "reason", false},
			{"stopped with a reason", func(v *adapterproto.ControlInterruptReply) { v.Reason = adapterproto.InterruptReasonAdapterOffline }, "reason", false},
			{"outcome unset", func(v *adapterproto.ControlInterruptReply) { v.Outcome = 0 }, "outcome", false},
		})
}

func TestControlStatusReplyRules(t *testing.T) {
	method(t, func() adapterproto.ControlStatusReply {
		return adapterproto.ControlStatusReply{Activity: adapterproto.ActivityBusy, Queued: 3, AsOf: 1792713600,
			Conditions: []adapterproto.ControlCondition{1, 4}, Presets: []adapterproto.PresetAvailability{{Name: "a", Available: true}, {Name: "b"}}}
	}, []row[adapterproto.ControlStatusReply]{
		{"activity unset", func(v *adapterproto.ControlStatusReply) { v.Activity = 0 }, "activity", false},
		{"queued zero", func(v *adapterproto.ControlStatusReply) { v.Queued = 0 }, "", false},
		{"queued negative", func(v *adapterproto.ControlStatusReply) { v.Queued = -1 }, "queued", false},
		{"no conditions", func(v *adapterproto.ControlStatusReply) { v.Conditions = nil }, "", false},
		{"conditions out of order", func(v *adapterproto.ControlStatusReply) { v.Conditions = []adapterproto.ControlCondition{4, 1} }, "conditions", true},
		{"conditions repeated", func(v *adapterproto.ControlStatusReply) { v.Conditions = []adapterproto.ControlCondition{4, 4} }, "conditions", false},
		{"all six conditions", func(v *adapterproto.ControlStatusReply) {
			v.Conditions = []adapterproto.ControlCondition{1, 2, 3, 4, 5, 6}
		}, "", false},
		{"condition out of range", func(v *adapterproto.ControlStatusReply) { v.Conditions = []adapterproto.ControlCondition{7} }, "conditions", false},
		{"presets are for runtime endpoints only, so optional", func(v *adapterproto.ControlStatusReply) { v.Presets = nil }, "", false},
		{"presets out of order", func(v *adapterproto.ControlStatusReply) {
			v.Presets = []adapterproto.PresetAvailability{{Name: "b"}, {Name: "a"}}
		}, "presets", true},
		{"presets with a repeated name", func(v *adapterproto.ControlStatusReply) {
			v.Presets = []adapterproto.PresetAvailability{{Name: "a"}, {Name: "a", Available: true}}
		}, "presets", true},
		{"preset name invalid", func(v *adapterproto.ControlStatusReply) { v.Presets = []adapterproto.PresetAvailability{{Name: "A"}} }, "presets[0].name", false},
		{"as_of required", func(v *adapterproto.ControlStatusReply) { v.AsOf = 0 }, "as_of", false},
	})
}

func TestControlReplyJSON(t *testing.T) {
	tests := []struct {
		name string
		in   adapterproto.ControlReply
		json string
	}{
		{"created", adapterproto.ControlReply{Signal: "session.create", Create: &adapterproto.ControlCreateReply{Outcome: adapterproto.CreateReplyCreated, SessionEndpoint: ep1}},
			`{"outcome":"created","session_endpoint":"` + ep1 + `","signal":"session.create"}`},
		{"unconfirmed", adapterproto.ControlReply{Signal: "session.create", Create: &adapterproto.ControlCreateReply{Outcome: adapterproto.CreateReplyUnconfirmed, SessionEndpoint: ep1, Reason: adapterproto.CreateReasonAdapterLost}},
			`{"outcome":"unconfirmed","reason":"adapter_lost","session_endpoint":"` + ep1 + `","signal":"session.create"}`},
		{"closed", adapterproto.ControlReply{Signal: "session.close", Close: &adapterproto.ControlCloseReply{Outcome: adapterproto.CloseReplyClosed}},
			`{"outcome":"closed","signal":"session.close"}`},
		{"interrupt failed", adapterproto.ControlReply{Signal: "session.interrupt", Interrupt: &adapterproto.ControlInterruptReply{Outcome: adapterproto.InterruptReplyFailed, Reason: adapterproto.InterruptReasonNativeUnreachable}},
			`{"outcome":"failed","reason":"native_unreachable","signal":"session.interrupt"}`},
		{"status", adapterproto.ControlReply{Signal: "endpoint.status", Status: &adapterproto.ControlStatusReply{Activity: adapterproto.ActivityIdle, Queued: 0, AsOf: 5}},
			`{"activity":"idle","as_of":5,"queued":0,"signal":"endpoint.status"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.in)
			if err != nil || string(b) != tc.json {
				t.Fatalf("Marshal = %s, %v, want %s", b, err, tc.json)
			}
			var back adapterproto.ControlReply
			if err := adapterproto.Decode(b, &back); err != nil {
				t.Fatalf("Decode: %v", err)
			}
			again, _ := json.Marshal(back)
			if string(again) != tc.json {
				t.Fatalf("round trip = %s", again)
			}
		})
	}
}

func TestControlReplyDecodeRejects(t *testing.T) {
	for name, in := range map[string]string{
		"no signal":                 `{"outcome":"closed"}`,
		"null signal":               `{"signal":null,"outcome":"closed"}`,
		"unknown signal":            `{"signal":"message.send","outcome":"closed"}`,
		"the fields of another":     `{"signal":"session.close","outcome":"closed","activity":"idle"}`,
		"unknown outcome":           `{"signal":"session.close","outcome":"gone"}`,
		"outcome as a number":       `{"signal":"session.close","outcome":1}`,
		"signal in another case":    `{"Signal":"session.close","outcome":"closed"}`,
		"a duplicate signal":        `{"signal":"session.close","signal":"session.close","outcome":"closed"}`,
		"a reason with the outcome": `{"signal":"session.close","outcome":"closed","reason":"adapter_offline"}`,
		"an explicit empty list":    `{"signal":"endpoint.status","activity":"idle","queued":0,"conditions":[],"as_of":1}`,
		"a null reason":             `{"signal":"session.close","outcome":"closed","reason":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			var r adapterproto.ControlReply
			if err := adapterproto.Decode([]byte(in), &r); err == nil {
				t.Fatalf("Decode accepted %s", in)
			}
		})
	}
	// Marshal refuses a reply whose fields do not fit the signal.
	if _, err := json.Marshal(adapterproto.ControlReply{Signal: "session.close"}); err == nil {
		t.Error("Marshal of an empty reply succeeded")
	}
	if _, err := json.Marshal(adapterproto.ControlReply{Signal: "session.close", Close: &adapterproto.ControlCloseReply{Outcome: adapterproto.CloseReplyClosed},
		Status: &adapterproto.ControlStatusReply{}}); err == nil {
		t.Error("Marshal of a reply with two bodies succeeded")
	}
}
