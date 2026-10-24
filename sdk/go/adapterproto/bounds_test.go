package adapterproto_test

import (
	"errors"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// lengthRule is a field that holds a string of min to max characters. optional
// says an empty string means the field is absent, so only min..max applies to a
// value that is present.
type lengthRule struct {
	name     string
	min, max int
	optional bool
	set      func(s string) error // validates a valid message with the field set to s
}

func validSend() adapterproto.SendParams {
	return adapterproto.SendParams{Endpoint: ep1, To: []adapterproto.Address{addr1}, ContentType: "text/plain", Body: "x"}
}

func validProbeResult() adapterproto.ProbeResult {
	return adapterproto.ProbeResult{Auth: "valid", Dependencies: []adapterproto.Dependency{{Name: "git", State: "ok"}},
		Presets:    []adapterproto.PresetStatus{{Name: "default", Usable: true, Problems: []string{"p"}}},
		ToolProbes: []adapterproto.ToolProbeResult{{Preset: "default", Frontend: "cli", Outcome: "ran"}}}
}

func validStart() adapterproto.StartParams {
	return adapterproto.StartParams{Node: "n", Config: adapterproto.ConfigValues{}, Presets: []adapterproto.PresetConfig{},
		Sessions: []adapterproto.SessionAttach{}, DirectEndpoints: []adapterproto.DirectAttach{{Endpoint: ep1, Key: "k"}}}
}

func validCreated() adapterproto.SessionCreateResult {
	return adapterproto.SessionCreateResult{Outcome: "created", NativeRef: "n", Frontends: []adapterproto.Frontend{"cli"}}
}

func validIndeterminate() adapterproto.SessionCreateResult {
	return adapterproto.SessionCreateResult{Outcome: "indeterminate", Grounds: &adapterproto.Grounds{Code: "turn_running"}}
}

// TestStringLengthBounds checks both ends of every length rule: the shortest and
// the longest value is accepted, one more than the longest is not, and for a
// required field the empty string is not either.
func TestStringLengthBounds(t *testing.T) {
	rules := []lengthRule{
		{"send.content_type", 1, 128, false, func(s string) error { p := validSend(); p.ContentType = s; return p.Validate() }},
		{"send.thread", 1, 128, true, func(s string) error { p := validSend(); p.Thread = s; return p.Validate() }},
		{"send.idempotency_key", 1, 128, true, func(s string) error { p := validSend(); p.IdempotencyKey = s; return p.Validate() }},
		{"send.reply_to", 1, 128, true, func(s string) error { p := validSend(); p.ReplyTo = adapterproto.MessageID(s); return p.Validate() }},
		{"register.description", 1, 1024, true, func(s string) error {
			return adapterproto.RegisterParams{Key: "k", DisplayName: "n", Description: s}.Validate()
		}},
		{"register.display_name", 1, 128, false, func(s string) error { return adapterproto.RegisterParams{Key: "k", DisplayName: s}.Validate() }},
		{"instance.id", 1, 128, false, func(s string) error {
			return adapterproto.InitializeParams{Protocol: adapterproto.Range{Min: 1, Max: 1}, Daemon: adapterproto.DaemonInfo{Version: "1"}, Instance: adapterproto.InstanceInfo{ID: s, LockPath: "/l"}}.Validate()
		}},
		{"instance.lock_path", 1, 4096, false, func(s string) error {
			return adapterproto.InitializeParams{Protocol: adapterproto.Range{Min: 1, Max: 1}, Daemon: adapterproto.DaemonInfo{Version: "1"}, Instance: adapterproto.InstanceInfo{ID: "i", LockPath: s}}.Validate()
		}},
		{"start.node", 1, 128, false, func(s string) error { p := validStart(); p.Node = s; return p.Validate() }},
		{"start.direct_endpoints.key", 1, 128, false, func(s string) error { p := validStart(); p.DirectEndpoints[0].Key = s; return p.Validate() }},
		{"runtime.name", 1, 64, false, func(s string) error {
			r := validProbeResult()
			r.Runtime = &adapterproto.RuntimeInfo{Name: s, Version: "1"}
			return r.Validate()
		}},
		{"runtime.version", 1, 64, false, func(s string) error {
			r := validProbeResult()
			r.Runtime = &adapterproto.RuntimeInfo{Name: "n", Version: s}
			return r.Validate()
		}},
		{"dependency.name", 1, 128, false, func(s string) error { r := validProbeResult(); r.Dependencies[0].Name = s; return r.Validate() }},
		{"dependency.detail", 1, 1024, true, func(s string) error { r := validProbeResult(); r.Dependencies[0].Detail = s; return r.Validate() }},
		{"preset.problems", 1, 1024, false, func(s string) error {
			r := validProbeResult()
			r.Presets[0].Problems = []string{s}
			return r.Validate()
		}},
		{"tool probe.detail", 1, 1024, true, func(s string) error { r := validProbeResult(); r.ToolProbes[0].Detail = s; return r.Validate() }},
		{"availability.detail", 1, 1024, true, func(s string) error {
			return adapterproto.AvailabilityParams{Conditions: []adapterproto.Availability{{Condition: "runtime_auth", Detail: s}}}.Validate()
		}},
		{"create result.detail", 1, 1024, true, func(s string) error { r := validCreated(); r.Detail = s; return r.Validate() }},
		{"create result.native_ref", 1, 256, false, func(s string) error {
			r := validCreated()
			r.NativeRef = adapterproto.NativeRef(s)
			return r.Validate()
		}},
		{"grounds.detail", 1, 1024, true, func(s string) error { r := validIndeterminate(); r.Grounds.Detail = s; return r.Validate() }},
		{"recv.message_id", 1, 128, true, func(s string) error {
			return adapterproto.RecvParams{Endpoint: ep1, MessageID: adapterproto.MessageID(s), Part: 1}.Validate()
		}},
		{"send refused reason", 1, 64, false, func(s string) error {
			return adapterproto.ErrorObject{Code: 1009, Message: "m", Data: raw(`{"reason":"` + s + `"}`)}.Validate()
		}},
		{"invalid address field", 1, 128, false, func(s string) error {
			return adapterproto.ErrorObject{Code: 1008, Message: "m", Data: raw(`{"field":"` + s + `"}`)}.Validate()
		}},
		{"control reply.session_endpoint", 1, 128, false, func(s string) error {
			return adapterproto.ControlCreateReply{Outcome: adapterproto.CreateReplyCreated, SessionEndpoint: adapterproto.EndpointRef(s)}.Validate()
		}},
		{"tool probe result.preset", 1, 64, false, func(s string) error { r := validProbeResult(); r.ToolProbes[0].Preset = s; return r.Validate() }},
	}
	for _, r := range rules {
		t.Run(r.name, func(t *testing.T) {
			ch := "a"
			if r.name == "send refused reason" {
				ch = "a"
			}
			for _, n := range []int{r.min, r.min + 1, r.max - 1, r.max} {
				if err := r.set(repeat(ch, n)); err != nil {
					t.Errorf("length %d rejected: %v", n, err)
				}
			}
			if err := r.set(repeat(ch, r.max+1)); err == nil {
				t.Errorf("length %d accepted", r.max+1)
			}
			if !r.optional {
				if err := r.set(""); err == nil {
					t.Error("the empty string was accepted")
				}
			}
		})
	}
}

// intRule is a field that holds an integer from min to max.
type intRule struct {
	name     string
	min, max int64
	set      func(v int64) error
}

func TestIntegerBounds(t *testing.T) {
	const exact = 1<<53 - 1
	rules := []intRule{
		{"pending.count", 1, 1<<31 - 1, func(v int64) error { return adapterproto.PendingParams{Endpoint: ep1, Count: int(v)}.Validate() }},
		{"status.queued", 0, 1<<31 - 1, func(v int64) error {
			return adapterproto.ControlStatusReply{Activity: adapterproto.ActivityIdle, Queued: int(v), AsOf: 1}.Validate()
		}},
		{"status.as_of", 1, exact, func(v int64) error {
			return adapterproto.ControlStatusReply{Activity: adapterproto.ActivityIdle, AsOf: v}.Validate()
		}},
		{"recv.part", 1, 1000000, func(v int64) error {
			return adapterproto.RecvParams{Endpoint: ep1, MessageID: "m", Part: int(v)}.Validate()
		}},
		{"part ack.part", 1, 1000000, func(v int64) error {
			return adapterproto.PartAck{MessageID: "m", Part: int(v), Ack: "recorded"}.Validate()
		}},
		{"part ref.part", 1, 1000000, func(v int64) error { return adapterproto.PartRef{MessageID: "m", Part: int(v)}.Validate() }},
		{"initialize result.protocol_version", 1, 65535, func(v int64) error {
			return adapterproto.InitializeResult{ProtocolVersion: int(v), Adapter: adapterproto.AdapterInfo{ID: "a", Version: "1.0.0"}}.Validate()
		}},
		{"range.min", 1, 65535, func(v int64) error { return adapterproto.Range{Min: int(v), Max: 65535}.Validate() }},
		{"range.max", 1, 65535, func(v int64) error { return adapterproto.Range{Min: 1, Max: int(v)}.Validate() }},
		{"timestamp", 1, exact, func(v int64) error {
			return adapterproto.TurnEndedParams{Endpoint: ep1, Turn: "t", Final: "failed", AtMS: v}.Validate()
		}},
		{"accept before", 1, 1<<31 - 1, func(v int64) error {
			if v == 0 {
				return errors.New("zero means absent, which is valid")
			}
			p := validSend()
			p.AcceptBeforeS = v
			return p.Validate()
		}},
		{"pid", 1, 1<<32 - 1, func(v int64) error { return adapterproto.ProcessIdentity{PID: v, StartKey: "k"}.Validate() }},
		{"config integer", -exact, exact, func(v int64) error {
			return adapterproto.ConfigValues{"k": raw(itoa(int(v)))}.Validate()
		}},
		{"max endpoints", 1, 1000, func(v int64) error { return adapterproto.DirectCapabilities{MaxEndpoints: int(v)}.Validate() }},
	}
	for _, r := range rules {
		t.Run(r.name, func(t *testing.T) {
			for _, v := range []int64{r.min, r.min + 1, r.max - 1, r.max} {
				if err := r.set(v); err != nil {
					t.Errorf("%d rejected: %v", v, err)
				}
			}
			for _, v := range []int64{r.min - 1, r.max + 1} {
				if err := r.set(v); err == nil {
					t.Errorf("%d accepted", v)
				}
			}
		})
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func TestCountLimits(t *testing.T) {
	t.Run("64 config entries", func(t *testing.T) {
		cfg := adapterproto.ConfigValues{}
		for i := 0; i < 64; i++ {
			cfg["k"+itoa(i)] = raw(`1`)
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("64 entries: %v", err)
		}
		cfg["k64"] = raw(`1`)
		if err := cfg.Validate(); err == nil {
			t.Fatal("65 entries accepted")
		}
	})
	t.Run("config strings", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			raw  string
			ok   bool
		}{
			{"empty string", `""`, true},
			{"string of 4096", `"` + repeat("s", 4096) + `"`, true},
			{"list item empty", `[""]`, true},
			{"list item of 4096", `["` + repeat("s", 4096) + `"]`, true},
			{"list item of 4097", `["` + repeat("s", 4097) + `"]`, false},
		} {
			err := adapterproto.ConfigValues{"k": raw(tc.raw)}.Validate()
			if (err == nil) != tc.ok {
				t.Errorf("%s: %v", tc.name, err)
			}
		}
	})
	t.Run("env name of 128", func(t *testing.T) {
		if err := (adapterproto.ToolAccess{Env: map[string]string{repeat("A", 128): "x"}}).Validate(); err != nil {
			t.Fatal(err)
		}
		if err := (adapterproto.ToolAccess{Env: map[string]string{"A": ""}}).Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("one character names", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			err  error
		}{
			{"endpoint", adapterproto.EndpointRef("e").Validate()},
			{"preset", adapterproto.ValidatePresetName("p")},
			{"preset of 64", adapterproto.ValidatePresetName(repeat("p", 64))},
		} {
			if tc.err != nil {
				t.Errorf("%s: %v", tc.name, tc.err)
			}
		}
	})
}

func TestPageNextAtTheBound(t *testing.T) {
	p := page()
	p.Messages[2].Part = &adapterproto.PagePart{Index: 999999, Total: 1000000, Label: "part 999999/1000000", Next: &adapterproto.PageNext{Msg: "m-big", Part: 1000000}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.Messages[2].Part = &adapterproto.PagePart{Index: 1000000, Total: 1000000, Label: "part 1000000/1000000"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.Messages[2].Part = &adapterproto.PagePart{Index: 1, Total: 1000001, Label: "part 1/1000001", Next: &adapterproto.PageNext{Msg: "m-big", Part: 2}}
	if err := p.Validate(); err == nil {
		t.Fatal("a total over the bound was accepted")
	}
}

func TestStatusReplyReasonsMustBeKnown(t *testing.T) {
	if err := (adapterproto.ControlCloseReply{Outcome: adapterproto.CloseReplyUnconfirmed, Reason: 9}).Validate(); err == nil {
		t.Error("an unknown close reason was accepted")
	}
	if err := (adapterproto.ControlInterruptReply{Outcome: adapterproto.InterruptReplyFailed, Reason: 9}).Validate(); err == nil {
		t.Error("an unknown interrupt reason was accepted")
	}
	if err := (adapterproto.ControlCreateReply{Outcome: adapterproto.CreateReplyFailed, Reason: 9}).Validate(); err == nil {
		t.Error("an unknown create reason was accepted")
	}
}

func TestVersionErrorDataIsValidated(t *testing.T) {
	good := `{"requested":{"min":2,"max":3},"supported":{"min":1,"max":1}}`
	if err := (adapterproto.ErrorObject{Code: -32022, Message: "m", Data: raw(good)}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"requested":{"min":2,"max":3},"supported":{"min":3,"max":1}}`,
		`{"requested":{"min":2,"max":3},"supported":{"min":0,"max":1}}`,
		`{"requested":{"min":0,"max":3},"supported":{"min":1,"max":1}}`,
	} {
		if err := (adapterproto.ErrorObject{Code: -32022, Message: "m", Data: raw(bad)}).Validate(); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if err := (adapterproto.UnsupportedVersionData{Requested: adapterproto.Range{Min: 1, Max: 1}, Supported: adapterproto.Range{Min: 2, Max: 1}}).Validate(); err == nil {
		t.Error("a reversed supported range was accepted")
	}
}

func TestCreateResultPartsAreValidated(t *testing.T) {
	r := validIndeterminate()
	r.Grounds.Code = "who"
	if err := r.Validate(); err == nil {
		t.Error("unknown grounds code accepted")
	}
	c := validCreated()
	c.NativeRef = adapterproto.NativeRef(repeat("n", 257))
	if err := c.Validate(); err == nil {
		t.Error("overlong native ref accepted")
	}
	p := validProbeResult()
	p.ToolProbes[0].Preset = "Bad"
	if err := p.Validate(); err == nil {
		t.Error("tool probe result with an invalid preset accepted")
	}
}
