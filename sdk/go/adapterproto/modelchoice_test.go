package adapterproto_test

import (
	"slices"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// TestEffortScale: the efforts in the order of the scale, lowest first. A list of
// efforts is held to this order, so it must not move.
func TestEffortScale(t *testing.T) {
	want := []adapterproto.Effort{"low", "medium", "high", "xhigh", "max"}
	if got := adapterproto.Efforts(); !slices.Equal(got, want) {
		t.Fatalf("Efforts() = %v, want %v", got, want)
	}
	for _, e := range want {
		if !e.Valid() {
			t.Errorf("%q is not valid", e)
		}
	}
	for _, e := range []adapterproto.Effort{"", "ultracode", "Low", "minimal", "none"} {
		if e.Valid() {
			t.Errorf("%q is valid", e)
		}
	}
	// The table of the schema is the scale, in the same order.
	if got := adapterproto.EnumTables()["Effort"]; !slices.Equal(got, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Errorf("EnumTables()[Effort] = %v", got)
	}
}

// TestModelNameRules: one row per class the rule refuses. A model name reaches an
// argv and a terminal, so it holds no blank, control or non-ASCII character.
func TestModelNameRules(t *testing.T) {
	for s, ok := range map[string]bool{
		"opus": true, "sonnet[1m]": true, "claude-opus-4-6[1m]": true, "0": true, "a": true,
		"us.anthropic.claude-haiku-4-5-20251001-v1:0":                               true,
		"claude-haiku-4-5@20251001":                                                 true,
		"arn:aws:bedrock:us-east-1:123456789012:application-inference-profile/abc1": true,
		"gpt-5.1-codex": true, "gpt-oss:20b": true, "openai/gpt-4o": true, "a+b": true, "a_b": true,
		repeat("a", 128): true,
		"":               false, repeat("a", 129): false,
		"-opus": false, ".opus": false, "[1m]": false, "_a": false, "/a": false, ":a": false, "@a": false, "+a": false,
		"a b": false, " a": false, "a ": false, "a\n": false, "a\tb": false, "a\x00": false, "a\x1b[31m": false, "a\x7f": false,
		"é": false, "aé": false, "a​": false, "a‮": false,
		"a\\b": false, "a\"b": false, "a'b": false, "a;b": false, "a$b": false, "a`b": false, "a|b": false, "a&b": false, "a*b": false,
		"a(b)": false, "a{b}": false, "a<b>": false, "a=b": false, "a,b": false, "a?b": false, "a!b": false, "a#b": false, "a%b": false, "a^b": false, "a~b": false,
	} {
		if err := adapterproto.ValidateModelName(s); (err == nil) != ok {
			t.Errorf("ValidateModelName(%q) = %v, want ok=%v", s, err, ok)
		}
	}
}

func TestControlParamsModelChoiceRules(t *testing.T) {
	method(t, func() adapterproto.ControlParams {
		return adapterproto.ControlParams{Preset: "default", Model: "opus", Effort: adapterproto.Effort("high")}
	}, []row[adapterproto.ControlParams]{
		{"model and effort are optional", func(p *adapterproto.ControlParams) { p.Model, p.Effort = "", "" }, "", false},
		{"a model alone", func(p *adapterproto.ControlParams) { p.Effort = "" }, "", false},
		{"an effort alone", func(p *adapterproto.ControlParams) { p.Model = "" }, "", false},
		{"every effort of the scale", func(p *adapterproto.ControlParams) { p.Effort = "xhigh" }, "", false},
		{"model with a blank", func(p *adapterproto.ControlParams) { p.Model = "a b" }, "model", false},
		{"model with a newline", func(p *adapterproto.ControlParams) { p.Model = "opus\n" }, "model", false},
		{"model over the limit", func(p *adapterproto.ControlParams) { p.Model = repeat("m", 129) }, "model", false},
		{"model that starts with a dash", func(p *adapterproto.ControlParams) { p.Model = "--opus" }, "model", false},
		{"effort outside the scale", func(p *adapterproto.ControlParams) { p.Effort = "ultracode" }, "effort", false},
		{"effort in capitals", func(p *adapterproto.ControlParams) { p.Effort = "High" }, "effort", false},
		{"preset is still checked", func(p *adapterproto.ControlParams) { p.Preset = "Bad Preset" }, "preset", false},
	})
}

func TestSessionCreateParamsModelChoiceRules(t *testing.T) {
	method(t, func() adapterproto.SessionCreateParams {
		return adapterproto.SessionCreateParams{Attempt: attempt1, RuntimeEndpoint: epRT, SessionEndpoint: ep1, Preset: "default",
			FirstPrompt: adapterproto.FirstPrompt{Attempt: attempt2, Text: "hello"}, ToolAccess: toolAccess(),
			Model: "opus", Effort: "high"}
	}, []row[adapterproto.SessionCreateParams]{
		{"model and effort are optional", func(p *adapterproto.SessionCreateParams) { p.Model, p.Effort = "", "" }, "", false},
		{"model with a blank", func(p *adapterproto.SessionCreateParams) { p.Model = "a b" }, "model", false},
		{"model over the limit", func(p *adapterproto.SessionCreateParams) { p.Model = repeat("m", 129) }, "model", false},
		{"effort outside the scale", func(p *adapterproto.SessionCreateParams) { p.Effort = "ultracode" }, "effort", false},
	})
}

func TestModelChoiceCapabilityRules(t *testing.T) {
	method(t, func() adapterproto.Capabilities {
		return adapterproto.Capabilities{Runtime: &adapterproto.RuntimeCapabilities{Frontends: []adapterproto.Frontend{"cli"},
			ModelChoice: &adapterproto.ModelChoiceCapability{Efforts: []adapterproto.Effort{"low", "medium", "high", "xhigh", "max"}}}}
	}, []row[adapterproto.Capabilities]{
		{"the whole scale", nil, "", false},
		{"a subset of the scale", func(c *adapterproto.Capabilities) {
			c.Runtime.ModelChoice.Efforts = []adapterproto.Effort{"low", "high"}
		}, "", false},
		{"one effort", func(c *adapterproto.Capabilities) { c.Runtime.ModelChoice.Efforts = []adapterproto.Effort{"max"} }, "", false},
		{"the capability is optional", func(c *adapterproto.Capabilities) { c.Runtime.ModelChoice = nil }, "", false},
		{"efforts required", func(c *adapterproto.Capabilities) { c.Runtime.ModelChoice.Efforts = nil }, "model_choice.efforts", false},
		{"efforts empty", func(c *adapterproto.Capabilities) { c.Runtime.ModelChoice.Efforts = []adapterproto.Effort{} }, "model_choice.efforts", false},
		{"effort outside the scale", func(c *adapterproto.Capabilities) {
			c.Runtime.ModelChoice.Efforts = []adapterproto.Effort{"low", "ultracode"}
		}, "model_choice.efforts[1]", false},
		{"effort repeated", func(c *adapterproto.Capabilities) {
			c.Runtime.ModelChoice.Efforts = []adapterproto.Effort{"low", "low"}
		}, "model_choice.efforts", false},
		{"efforts out of order", func(c *adapterproto.Capabilities) {
			c.Runtime.ModelChoice.Efforts = []adapterproto.Effort{"high", "low"}
		}, "model_choice.efforts", true},
	})
}

// allowedRows are the rules of an allowlist, which a probe result and a status
// reply state in the same words: they are checked once, on both carriers.
var allowedRows = []struct {
	name    string
	allowed []adapterproto.ModelAllowance
	wantErr string
	beyond  bool
}{
	{"no allowlist", nil, "", false},
	{"one model", []adapterproto.ModelAllowance{{Model: "opus", Efforts: []adapterproto.Effort{"high"}}}, "", false},
	{"models sorted by name, efforts in the order of the scale", []adapterproto.ModelAllowance{
		{Model: "haiku", Efforts: []adapterproto.Effort{"low", "medium"}},
		{Model: "opus", Efforts: []adapterproto.Effort{"low", "medium", "high", "xhigh", "max"}},
	}, "", false},
	{"16 models", modelAllowances(16), "", false},
	{"17 models", modelAllowances(17), "allowed_models", false},
	{"an empty list", []adapterproto.ModelAllowance{}, "allowed_models", false},
	{"a model name that breaks the syntax", []adapterproto.ModelAllowance{{Model: "a b", Efforts: []adapterproto.Effort{"high"}}}, "allowed_models[0].model", false},
	{"a model with no effort", []adapterproto.ModelAllowance{{Model: "opus"}}, "allowed_models[0].efforts", false},
	{"a model with an empty effort list", []adapterproto.ModelAllowance{{Model: "opus", Efforts: []adapterproto.Effort{}}}, "allowed_models[0].efforts", false},
	{"an effort outside the scale", []adapterproto.ModelAllowance{{Model: "opus", Efforts: []adapterproto.Effort{"ultracode"}}}, "allowed_models[0].efforts[0]", false},
	{"an effort twice", []adapterproto.ModelAllowance{{Model: "opus", Efforts: []adapterproto.Effort{"high", "high"}}}, "allowed_models[0].efforts", false},
	{"efforts out of order", []adapterproto.ModelAllowance{{Model: "opus", Efforts: []adapterproto.Effort{"high", "low"}}}, "allowed_models[0].efforts", true},
	{"models out of order", []adapterproto.ModelAllowance{
		{Model: "opus", Efforts: []adapterproto.Effort{"high"}}, {Model: "haiku", Efforts: []adapterproto.Effort{"high"}},
	}, "allowed_models", true},
	{"a model twice", []adapterproto.ModelAllowance{
		{Model: "opus", Efforts: []adapterproto.Effort{"high"}}, {Model: "opus", Efforts: []adapterproto.Effort{"low"}},
	}, "allowed_models", true},
}

// modelAllowances is n models, sorted by name, each with the efforts low and high.
func modelAllowances(n int) []adapterproto.ModelAllowance {
	out := make([]adapterproto.ModelAllowance, n)
	for i := range out {
		out[i] = adapterproto.ModelAllowance{Model: "m" + string(rune('a'+i)), Efforts: []adapterproto.Effort{"low", "high"}}
	}
	return out
}

func TestPresetStatusModelChoiceRules(t *testing.T) {
	base := func() adapterproto.PresetStatus {
		return adapterproto.PresetStatus{Name: "default", Usable: true, Model: "opus", Effort: "high"}
	}
	rows := []row[adapterproto.PresetStatus]{
		{"no defaults", func(p *adapterproto.PresetStatus) { p.Model, p.Effort = "", "" }, "", false},
		{"model with a blank", func(p *adapterproto.PresetStatus) { p.Model = "a b" }, "model", false},
		{"effort outside the scale", func(p *adapterproto.PresetStatus) { p.Effort = "ultracode" }, "effort", false},
	}
	for _, r := range allowedRows {
		rows = append(rows, row[adapterproto.PresetStatus]{"allowlist: " + r.name, func(p *adapterproto.PresetStatus) { p.AllowedModels = r.allowed }, r.wantErr, r.beyond})
	}
	method(t, base, rows)
}

func TestPresetAvailabilityModelChoiceRules(t *testing.T) {
	base := func() adapterproto.PresetAvailability {
		return adapterproto.PresetAvailability{Name: "default", Available: true, Model: "opus", Effort: "high"}
	}
	rows := []row[adapterproto.PresetAvailability]{
		{"no defaults", func(p *adapterproto.PresetAvailability) { p.Model, p.Effort = "", "" }, "", false},
		{"model with a blank", func(p *adapterproto.PresetAvailability) { p.Model = "a b" }, "model", false},
		{"effort outside the scale", func(p *adapterproto.PresetAvailability) { p.Effort = "ultracode" }, "effort", false},
	}
	for _, r := range allowedRows {
		rows = append(rows, row[adapterproto.PresetAvailability]{"allowlist: " + r.name, func(p *adapterproto.PresetAvailability) { p.AllowedModels = r.allowed }, r.wantErr, r.beyond})
	}
	method(t, base, rows)
}

// TestMaxAllowedModels: the bound on one allowlist that the rules above state.
func TestMaxAllowedModels(t *testing.T) {
	if adapterproto.MaxAllowedModels != 16 {
		t.Errorf("MaxAllowedModels = %d, want 16", adapterproto.MaxAllowedModels)
	}
}

// The choice of a model and an effort, each optional, is judged in one place for
// every record that carries the pair.
func TestModelChoiceRules(t *testing.T) {
	for _, tc := range []struct {
		name   string
		model  string
		effort adapterproto.Effort
		valid  bool
		chosen bool
	}{
		{"nothing chosen", "", "", true, false},
		{"a model", "sonnet[1m]", "", true, true},
		{"an effort", "", adapterproto.EffortHigh, true, true},
		{"both", "opus", adapterproto.EffortLow, true, true},
		{"a model that is not a name", "a b", "", false, true},
		{"an effort that is not on the scale", "", "ultracode", false, true},
		{"a good model beside a bad effort", "opus", "Low", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := adapterproto.ValidateModelChoice(tc.model, tc.effort); (err == nil) != tc.valid {
				t.Errorf("ValidateModelChoice(%q, %q) = %v, want valid=%v", tc.model, tc.effort, err, tc.valid)
			}
			if got := adapterproto.ChoosesModel(tc.model, tc.effort); got != tc.chosen {
				t.Errorf("ChoosesModel(%q, %q) = %v, want %v", tc.model, tc.effort, got, tc.chosen)
			}
		})
	}
}
