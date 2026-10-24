package config_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/node/config"
)

// A preset without the statement about local sources is invalid, and so is one
// whose statement is not exactly "allow" or "deny": there is no default, and a
// value that merely resembles a statement is not read as one. The zero
// authz.PresetLocalAccess is never the result of a successful decoding.
func TestPresetWithoutLocalSourcesIsInvalid(t *testing.T) {
	valid := map[string]authz.PresetLocalAccess{
		`{"local_sources": "allow"}`: authz.PresetLocalAllow,
		`{"local_sources": "deny"}`:  authz.PresetLocalDeny,
	}
	for doc, want := range valid {
		p, err := config.DecodePreset([]byte(doc))
		if err != nil {
			t.Errorf("DecodePreset(%s) = %v", doc, err)
			continue
		}
		if p.LocalSources != want {
			t.Errorf("DecodePreset(%s).LocalSources = %v, want %v", doc, p.LocalSources, want)
		}
	}

	invalid := []struct {
		name, doc string
		malformed bool // not a preset document at all, as opposed to a bad value
	}{
		{"no field at all", `{}`, true},
		{"only a description", `{"description": "x"}`, true},
		{"only config", `{"config": {}}`, true},
		{"empty string", `{"local_sources": ""}`, false},
		{"another word", `{"local_sources": "maybe"}`, false},
		{"allow in capitals", `{"local_sources": "ALLOW"}`, false},
		{"deny in capitals", `{"local_sources": "Deny"}`, false},
		{"allow with a space", `{"local_sources": "allow "}`, false},
		{"yes", `{"local_sources": "yes"}`, false},
		{"true", `{"local_sources": true}`, true},
		{"false", `{"local_sources": false}`, true},
		{"a number", `{"local_sources": 1}`, true},
		{"zero", `{"local_sources": 0}`, true},
		{"null", `{"local_sources": null}`, true},
		{"a list", `{"local_sources": ["allow"]}`, true},
		{"an object", `{"local_sources": {"allow": true}}`, true},
		{"the key in another case", `{"Local_Sources": "allow"}`, true},
		{"the statement twice", `{"local_sources": "allow", "local_sources": "deny"}`, true},
		{"an unknown field beside it", `{"local_sources": "allow", "local": true}`, true},
	}
	for _, r := range invalid {
		t.Run(r.name, func(t *testing.T) {
			p, err := config.DecodePreset([]byte(r.doc))
			if err == nil {
				t.Fatalf("DecodePreset(%s) accepted it, as %+v", r.doc, p)
			}
			var fe *config.FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("error %v is not a *config.FieldError", err)
			}
			if fe.Malformed != r.malformed {
				t.Errorf("Malformed = %v, want %v: %v", fe.Malformed, r.malformed, err)
			}
			if p.LocalSources != 0 {
				t.Errorf("a refused preset still has a statement: %v", p.LocalSources)
			}
		})
	}
}

// presets.add with a preset that makes no statement is refused as an invalid
// argument that points at the field, and writes nothing.
func TestAddPresetWithoutLocalSourcesIsRefused(t *testing.T) {
	rg, c := loaded(t)
	before := snapshot(t, rg.mem)
	var seen []config.Presets
	_, err := c.AddPreset(request(1, 2), "plain", []byte(`{"description": "no statement"}`), result(&seen, `{}`))
	ce := changeReason(t, err)
	if ce.Reason != config.ChangeInvalid || ce.Field != "local_sources" {
		t.Errorf("error = %+v, want ChangeInvalid at local_sources", ce)
	}
	if len(seen) != 0 {
		t.Error("a result was built for a change that was refused")
	}
	if c.Presets().Version != 2 || !reflect.DeepEqual(before, snapshot(t, rg.mem)) {
		t.Error("a refused preset changed the version or the disk")
	}
}

// A preset that makes the statement is added, and one that is not valid never
// reaches the file: whatever is in presets.json made the statement.
func TestEveryPresetOnDiskMadeTheStatement(t *testing.T) {
	rg, c := loaded(t)
	var seen []config.Presets
	after, err := c.AddPreset(request(1, 2), "strict", []byte(`{"local_sources": "deny", "description": "d"}`), result(&seen, `{"ok":true}`))
	if err != nil {
		t.Fatalf("AddPreset: %v", err)
	}
	if after.Items["strict"].LocalSources != authz.PresetLocalDeny {
		t.Errorf("the added preset = %+v", after.Items["strict"])
	}
	data, err := rg.mem.ReadFile(config.PresetsPath)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := config.DecodePresets(data)
	if err != nil {
		t.Fatalf("the file written is not valid: %v", err)
	}
	for name, p := range onDisk.Items {
		if p.LocalSources != authz.PresetLocalAllow && p.LocalSources != authz.PresetLocalDeny {
			t.Errorf("preset %q has no statement", name)
		}
	}
}
