package modelchoice_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterkit/modelchoice"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The whole scale: what a runtime that supports every level declares.
var scale = adapterproto.Efforts()

func mustParse(t *testing.T, raw string, supported []adapterproto.Effort) modelchoice.Allowed {
	t.Helper()
	a, err := modelchoice.ParseAllowed(json.RawMessage(raw), supported)
	if err != nil {
		t.Fatalf("ParseAllowed(%s): %v", raw, err)
	}
	return a
}

// TestParseAllowed: what a preset's allowlist may hold. Every refusal is a preset
// problem; the accepted rows show what is not refused.
func TestParseAllowed(t *testing.T) {
	fourModels := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf(`"m%02d":["low"]`, i)
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	tests := []struct {
		name      string
		raw       string
		supported []adapterproto.Effort
		wantErr   string // empty: accepted
	}{
		{"one model, one effort", `{"opus":["high"]}`, scale, ""},
		{"efforts in any order", `{"opus":["max","low","high"]}`, scale, ""},
		{"exactly the bound", fourModels(adapterproto.MaxAllowedModels), scale, ""},
		{"the supported subset", `{"gpt":["low","high"]}`, scale[:4], ""},

		{"an empty map", `{}`, scale, "no model"},
		{"null", `null`, scale, "no model"},
		{"a model with an empty effort list", `{"opus":[]}`, scale, `"opus"`},
		{"a model with a null effort list", `{"opus":null}`, scale, `"opus"`},
		{"a model name that breaks the syntax", `{"a b":["high"]}`, scale, "model name"},
		{"an empty model name", `{"":["high"]}`, scale, "model name"},
		{"more than the bound", fourModels(adapterproto.MaxAllowedModels + 1), scale, "at most"},
		{"an effort off the scale", `{"opus":["ultra"]}`, scale, `"ultra"`},
		{"an empty effort", `{"opus":[""]}`, scale, "effort"},
		{"an effort the adapter does not support", `{"gpt":["high","max"]}`, scale[:4], `"max"`},
		{"an effort listed twice", `{"opus":["high","low","high"]}`, scale, "twice"},
		{"an array", `["opus"]`, scale, "map"},
		{"a string", `"opus"`, scale, "map"},
		{"efforts as a string", `{"opus":"high"}`, scale, "map"},
		{"efforts as numbers", `{"opus":[1]}`, scale, "map"},
		{"efforts nested", `{"opus":[["high"]]}`, scale, "map"},
		{"no value", ``, scale, "map"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := modelchoice.ParseAllowed(json.RawMessage(tc.raw), tc.supported)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted, want an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestMaxAllowedModelsIsTheProtocolsBound(t *testing.T) {
	if modelchoice.MaxAllowedModels != adapterproto.MaxAllowedModels {
		t.Errorf("MaxAllowedModels = %d, protocol bound = %d", modelchoice.MaxAllowedModels, adapterproto.MaxAllowedModels)
	}
}

// TestEntries: the list an adapter puts into PresetStatus, in the order the
// protocol's validation requires, whatever order the owner wrote.
func TestEntries(t *testing.T) {
	a := mustParse(t, `{"sonnet":["high","low","medium"],"opus":["max","medium"],"haiku":["low"]}`, scale)
	want := []adapterproto.ModelAllowance{
		{Model: "haiku", Efforts: []adapterproto.Effort{"low"}},
		{Model: "opus", Efforts: []adapterproto.Effort{"medium", "max"}},
		{Model: "sonnet", Efforts: []adapterproto.Effort{"low", "medium", "high"}},
	}
	got := a.Entries()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Entries() = %v, want %v", got, want)
	}
	status := adapterproto.PresetStatus{Name: "p", Usable: true, AllowedModels: got}
	if err := status.Validate(); err != nil {
		t.Errorf("the entries do not pass the validation of PresetStatus: %v", err)
	}
	// The caller owns the result.
	got[0].Efforts[0] = "max"
	got[0].Model = "changed"
	if !reflect.DeepEqual(a.Entries(), want) {
		t.Error("changing the result of Entries() changed the allowlist")
	}

	var zero modelchoice.Allowed
	if zero.Entries() != nil {
		t.Errorf("zero Allowed: Entries() = %v, want nil", zero.Entries())
	}
}

// TestResolve: the fallback rules, one row per case. The allowlist is
// opus: medium high max; sonnet: low medium.
func TestResolve(t *testing.T) {
	const list = `{"opus":["medium","high","max"],"sonnet":["low","medium"]}`
	var none modelchoice.Allowed

	tests := []struct {
		name       string
		allowed    string // "": the zero value, a preset with no allowlist
		d          modelchoice.Defaults
		model      string
		effort     adapterproto.Effort
		want       modelchoice.Choice
		wantChosen bool
		refusal    string // empty: not refused; else a phrase of the message naming the rule
	}{
		{name: "neither given", allowed: list, d: modelchoice.Defaults{Model: "sonnet", Effort: "low"}},
		{name: "neither given, no allowlist", d: modelchoice.Defaults{Model: "sonnet", Effort: "low"}},
		{name: "neither given, no defaults", allowed: list},

		{name: "a model, no allowlist", model: "opus", refusal: "has no allowlist"},
		{name: "an effort, no allowlist", effort: "high", d: modelchoice.Defaults{Effort: "high"}, refusal: "has no allowlist"},
		{name: "both, no allowlist", model: "opus", effort: "high", refusal: "has no allowlist"},

		{name: "model only: the preset's effort goes with it", allowed: list, d: modelchoice.Defaults{Model: "sonnet", Effort: "medium"},
			model: "opus", want: modelchoice.Choice{Model: "opus", Effort: "medium"}, wantChosen: true},
		{name: "model only: no preset effort stays empty", allowed: list, d: modelchoice.Defaults{Model: "sonnet"},
			model: "opus", want: modelchoice.Choice{Model: "opus"}, wantChosen: true},
		{name: "model only: no defaults at all", allowed: list,
			model: "sonnet", want: modelchoice.Choice{Model: "sonnet"}, wantChosen: true},
		{name: "model only: the preset's effort is not one of the model's", allowed: list, d: modelchoice.Defaults{Model: "sonnet", Effort: "low"},
			model: "opus", refusal: "the preset's effort"},
		{name: "model only: not listed", allowed: list, d: modelchoice.Defaults{Effort: "medium"},
			model: "haiku", refusal: "is not in the allowlist"},

		{name: "effort only: the preset's model is listed", allowed: list, d: modelchoice.Defaults{Model: "sonnet", Effort: "medium"},
			effort: "low", want: modelchoice.Choice{Model: "sonnet", Effort: "low"}, wantChosen: true},
		{name: "effort only: not among the listed model's", allowed: list, d: modelchoice.Defaults{Model: "sonnet", Effort: "medium"},
			effort: "max", refusal: `effort "max" is not among the efforts`},
		{name: "effort only: a listed model, no preset effort", allowed: list, d: modelchoice.Defaults{Model: "opus"},
			effort: "high", want: modelchoice.Choice{Model: "opus", Effort: "high"}, wantChosen: true},
		{name: "effort only: no preset model, the preset's own effort", allowed: list, d: modelchoice.Defaults{Effort: "high"},
			effort: "high", want: modelchoice.Choice{Effort: "high"}, wantChosen: true},
		{name: "effort only: no preset model, another effort", allowed: list, d: modelchoice.Defaults{Effort: "high"},
			effort: "low", refusal: "must equal the preset's effort"},
		{name: "effort only: no preset model, no preset effort", allowed: list,
			effort: "high", refusal: "cannot be checked"},
		{name: "effort only: the preset's model is not listed, the preset's own effort", allowed: list, d: modelchoice.Defaults{Model: "haiku", Effort: "low"},
			effort: "low", want: modelchoice.Choice{Model: "haiku", Effort: "low"}, wantChosen: true},
		{name: "effort only: the preset's model is not listed, another effort", allowed: list, d: modelchoice.Defaults{Model: "haiku", Effort: "low"},
			effort: "high", refusal: "must equal the preset's effort"},
		{name: "effort only: the preset's model is not listed, no preset effort", allowed: list, d: modelchoice.Defaults{Model: "haiku"},
			effort: "high", refusal: "cannot be checked"},

		{name: "both", allowed: list, d: modelchoice.Defaults{Model: "sonnet", Effort: "low"},
			model: "opus", effort: "max", want: modelchoice.Choice{Model: "opus", Effort: "max"}, wantChosen: true},
		{name: "both: the defaults play no part", allowed: list,
			model: "sonnet", effort: "low", want: modelchoice.Choice{Model: "sonnet", Effort: "low"}, wantChosen: true},
		{name: "both: model not listed", allowed: list, model: "haiku", effort: "low", refusal: "is not in the allowlist"},
		{name: "both: effort not among the model's", allowed: list, model: "opus", effort: "low", refusal: `effort "low" is not among the efforts`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := none
			if tc.allowed != "" {
				a = mustParse(t, tc.allowed, scale)
			}
			got, chosen, err := modelchoice.Resolve(a, tc.d, tc.model, tc.effort)
			if tc.refusal != "" {
				if !errors.Is(err, modelchoice.ErrNotAllowed) {
					t.Fatalf("err = %v (choice %v, chosen %v), want one wrapping ErrNotAllowed", err, got, chosen)
				}
				if !strings.Contains(err.Error(), tc.refusal) {
					t.Errorf("error = %q, want it to name the rule %q", err, tc.refusal)
				}
				if chosen || got != (modelchoice.Choice{}) {
					t.Errorf("a refusal returned choice %v, chosen %v; want the zero values", got, chosen)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if chosen != tc.wantChosen || got != tc.want {
				t.Errorf("Resolve = %v, chosen %v; want %v, chosen %v", got, chosen, tc.want, tc.wantChosen)
			}
		})
	}
}

// TestChoiceValidate: a stored choice is read back into an argument of a runtime's
// process, so each class it refuses has a row.
func TestChoiceValidate(t *testing.T) {
	tests := []struct {
		name    string
		choice  modelchoice.Choice
		wantErr string // empty: accepted
	}{
		{"a model", modelchoice.Choice{Model: "opus"}, ""},
		{"an effort", modelchoice.Choice{Effort: "high"}, ""},
		{"both", modelchoice.Choice{Model: "gpt-5.1-codex", Effort: "xhigh"}, ""},

		{"neither", modelchoice.Choice{}, "chooses nothing"},
		{"a model name that breaks the syntax", modelchoice.Choice{Model: "opus 4"}, "model"},
		{"a model name that starts like an option", modelchoice.Choice{Model: "--opus"}, "model"},
		{"an effort off the scale", modelchoice.Choice{Effort: "huge"}, `"huge"`},
		{"a bad model beside a good effort", modelchoice.Choice{Model: "a b", Effort: "low"}, "model"},
		{"a good model beside a bad effort", modelchoice.Choice{Model: "opus", Effort: "huge"}, `"huge"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.choice.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("Validate() = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestChoiceJSON: the stored form is the one the adapters' checkpoints have always
// had, and a member that is absent stays absent.
func TestChoiceJSON(t *testing.T) {
	for _, tc := range []struct {
		choice modelchoice.Choice
		json   string
	}{
		{modelchoice.Choice{Model: "opus", Effort: "high"}, `{"model":"opus","effort":"high"}`},
		{modelchoice.Choice{Model: "opus"}, `{"model":"opus"}`},
		{modelchoice.Choice{Effort: "low"}, `{"effort":"low"}`},
	} {
		raw, err := json.Marshal(tc.choice)
		if err != nil || string(raw) != tc.json {
			t.Errorf("Marshal(%+v) = %s, %v, want %s", tc.choice, raw, err, tc.json)
		}
		var back modelchoice.Choice
		if err := json.Unmarshal([]byte(tc.json), &back); err != nil || back != tc.choice {
			t.Errorf("Unmarshal(%s) = %+v, %v, want %+v", tc.json, back, err, tc.choice)
		}
	}
}

// TestParseDefaults: what a preset's own model and effort may be. Both are
// checked and every problem is reported; the accepted rows show what is not refused.
func TestParseDefaults(t *testing.T) {
	codexEfforts := scale[:4]
	tests := []struct {
		name      string
		model     string
		effort    string
		supported []adapterproto.Effort
		want      modelchoice.Defaults
		problems  []string // each a substring of one problem, in order; none: accepted
	}{
		{"none set", "", "", scale, modelchoice.Defaults{}, nil},
		{"a model", "opus", "", scale, modelchoice.Defaults{Model: "opus"}, nil},
		{"an effort", "", "max", scale, modelchoice.Defaults{Effort: "max"}, nil},
		{"both", "gpt-5", "xhigh", codexEfforts, modelchoice.Defaults{Model: "gpt-5", Effort: "xhigh"}, nil},

		{"a model name that breaks the syntax", "opus 4", "", scale, modelchoice.Defaults{}, []string{"model:"}},
		{"an effort off the scale", "", "huge", scale, modelchoice.Defaults{}, []string{`effort: "huge"`}},
		{"an effort the adapter does not support", "", "max", codexEfforts, modelchoice.Defaults{}, []string{`effort: "max"`}},
		{"both wrong", "a b", "huge", scale, modelchoice.Defaults{}, []string{"model:", "effort:"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, problems := modelchoice.ParseDefaults(tc.model, tc.effort, tc.supported)
			if len(problems) != len(tc.problems) {
				t.Fatalf("problems = %q, want one containing each of %q", problems, tc.problems)
			}
			for i, want := range tc.problems {
				if !strings.Contains(problems[i], want) {
					t.Errorf("problem %d = %q, want it to contain %q", i, problems[i], want)
				}
			}
			if len(problems) == 0 && got != tc.want {
				t.Errorf("ParseDefaults() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
