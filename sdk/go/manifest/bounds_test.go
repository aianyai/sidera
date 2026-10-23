package manifest_test

import (
	"encoding/json"
	"testing"

	"github.com/aianyai/sidera/sdk/go/manifest"
)

// TestStringBounds checks both ends of every length rule of the manifest.
func TestStringBounds(t *testing.T) {
	setField := func(mod func(*manifest.Field, string)) func(string) error {
		return func(s string) error {
			m := base()
			f := manifest.Field{Key: "k", Type: manifest.FieldString, Title: "T"}
			mod(&f, s)
			m.Config = &manifest.Config{Instance: []manifest.Field{f}}
			return m.Validate()
		}
	}
	str := func(s string) json.RawMessage { b, _ := json.Marshal(s); return b }
	rules := []struct {
		name     string
		min, max int
		optional bool
		set      func(string) error
	}{
		{"entry program", 1, 256, false, func(s string) error { m := base(); m.Entry.Program = s; return m.Validate() }},
		{"entry arg", 0, 1024, true, func(s string) error { m := base(); m.Entry.Args = []string{s}; return m.Validate() }},
		{"requirement program", 1, 128, false, func(s string) error {
			m := base()
			m.Requires = []manifest.Requirement{{Program: s}}
			return m.Validate()
		}},
		{"tested runtime", 1, 64, false, func(s string) error {
			m := base()
			m.Tested = []manifest.TestedRange{{Runtime: s, Min: "1.0.0", Max: "1.0.0"}}
			return m.Validate()
		}},
		{"field title", 1, 128, false, setField(func(f *manifest.Field, s string) { f.Title = s })},
		{"field description", 1, 1024, true, setField(func(f *manifest.Field, s string) { f.Description = s })},
		{"field default string", 0, 4096, true, setField(func(f *manifest.Field, s string) { f.Default = str(s) })},
		{"field option", 1, 128, false, setField(func(f *manifest.Field, s string) { f.Type = manifest.FieldEnum; f.Options = []string{s} })},
		{"field list default item", 0, 4096, true, setField(func(f *manifest.Field, s string) {
			f.Type = manifest.FieldStringList
			f.Default = json.RawMessage(`[` + string(str(s)) + `]`)
		})},
		{"marker text", 0, 1024, true, func(s string) error {
			m := base()
			m.AckMarkers = &manifest.AckMarkers{Replaced: []manifest.Marker{{Literal: &s}}}
			return m.Validate()
		}},
	}
	for _, r := range rules {
		t.Run(r.name, func(t *testing.T) {
			for _, n := range []int{r.min, r.min + 1, r.max - 1, r.max} {
				if err := r.set(repeat("a", n)); err != nil {
					t.Errorf("length %d rejected: %v", n, err)
				}
			}
			if err := r.set(repeat("a", r.max+1)); err == nil {
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

func TestDefaultBounds(t *testing.T) {
	integer := func(raw string) error {
		m := base()
		m.Config = &manifest.Config{Instance: []manifest.Field{{Key: "k", Type: manifest.FieldInteger, Title: "T", Default: json.RawMessage(raw)}}}
		return m.Validate()
	}
	for raw, ok := range map[string]bool{
		"9007199254740991": true, "-9007199254740991": true, "9007199254740992": false, "-9007199254740992": false, "0": true,
	} {
		if err := integer(raw); (err == nil) != ok {
			t.Errorf("integer default %s: %v", raw, err)
		}
	}
	list := func(n int) error {
		m := base()
		m.Config = &manifest.Config{Instance: []manifest.Field{{Key: "k", Type: manifest.FieldStringList, Title: "T", Default: json.RawMessage("[" + repeat(`"a",`, n-1) + `"a"]`)}}}
		return m.Validate()
	}
	if err := list(256); err != nil {
		t.Errorf("256 items: %v", err)
	}
	if err := list(257); err == nil {
		t.Error("257 items accepted")
	}
	bound := func(v int64) error {
		m := base()
		m.Config = &manifest.Config{Instance: []manifest.Field{{Key: "k", Type: manifest.FieldInteger, Title: "T", Min: &v}}}
		return m.Validate()
	}
	if err := bound(-(1<<53 - 1)); err != nil {
		t.Errorf("lowest bound: %v", err)
	}
	if err := bound(-(1 << 53)); err == nil {
		t.Error("a bound beyond the exact range was accepted")
	}
}
