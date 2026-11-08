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
