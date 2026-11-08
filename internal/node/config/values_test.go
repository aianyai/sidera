package config_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/config"
)

// A second adapter whose manifest has a required instance field and a typed
// preset field, a third whose package cannot be loaded, and an instance of the
// second that was stored without its required field (as an older daemon allowed).
func valuesTree(t testing.TB) map[string]string {
	t.Helper()
	tree := validTree(t)
	tree[config.AdapterCurrentPath("strict")] = goldenCurrent
	tree[config.AdapterManifestPath("strict", "1.0.0")] = strictManifest
	tree[config.AdapterCurrentPath("broken")] = goldenCurrent
	tree[config.AdapterManifestPath("broken", "1.0.0")] = "{"
	tree[config.InstancesPath] = strings.Replace(goldenInstances, `"instances": {`,
		`"instances": {
    "legacy": {
      "adapter": "strict",
      "enabled": false
    },`, 1)
	return tree
}

const strictManifest = `{"manifest_version":1,"id":"strict","version":"1.0.0","protocol":{"min":1,"max":1},` +
	`"entry":{"program":"bin/sidera-adapter-strict"},"platforms":[{"os":"linux","arch":"amd64"}],` +
	`"config":{"instance":[{"key":"token","type":"string","title":"Token","required":true},` +
	`{"key":"retries","type":"integer","title":"Retries","min":0,"max":5}],` +
	`"preset":[{"key":"mode","type":"enum","title":"Mode","options":["fast","slow"]},` +
	`{"key":"names","type":"string_list","title":"Names"},` +
	`{"key":"limits","type":"map","title":"Limits","value":{"type":"string_list"}}]}}`

func loadedWithValues(t testing.TB) (*rig, *config.Config) {
	t.Helper()
	r := newRig(t)
	putTree(t, r.mem, valuesTree(t))
	return r, r.load()
}

// A change that writes configuration values refuses a value the adapter's
// manifest does not allow, names it by its path, and writes nothing, so a bad
// value never reaches a file the adapter is started from.
func TestWritingValuesChecksThemAgainstTheManifest(t *testing.T) {
	var presets []config.Presets
	var instances []config.Instances
	type write func(c *config.Config) error
	preset := func(doc string) write {
		return func(c *config.Config) error {
			_, err := c.AddPreset(request(1, 2), "new", []byte(doc), result(&presets, `{}`))
			return err
		}
	}
	enable := func(instance, adapter, doc string) write {
		return func(c *config.Config) error {
			var cfg []byte
			if doc != "" {
				cfg = []byte(doc)
			}
			_, err := c.EnableAdapter(request(1, 4), instance, adapter, cfg, nil, result(&instances, `{}`))
			return err
		}
	}
	setConfig := func(instance, doc string) write {
		return func(c *config.Config) error {
			_, err := c.SetInstanceConfig(request(1, 4), instance, []byte(doc), nil, result(&instances, `{}`))
			return err
		}
	}
	const deny = `"local_sources": "deny"`
	rows := []struct {
		name   string
		write  write
		reason config.ChangeReason
		field  string
	}{
		{"a preset value of the wrong kind", preset(`{` + deny + `, "config": {"example": {"turns": "4"}}}`), config.ChangeInvalid, "config.example.turns"},
		{"a preset enum value that is no option", preset(`{` + deny + `, "config": {"strict": {"mode": "medium"}}}`), config.ChangeInvalid, "config.strict.mode"},
		{"a preset list item that is no string", preset(`{` + deny + `, "config": {"strict": {"names": ["a", 1]}}}`), config.ChangeInvalid, "config.strict.names[1]"},
		{"a preset setting the adapter does not have", preset(`{` + deny + `, "config": {"example": {"colour": "red"}}}`), config.ChangeInvalid, "config.example.colour"},
		{"a preset section of an adapter that is not installed", preset(`{` + deny + `, "config": {"ghost": {}}}`), config.ChangeAdapterNotInstalled, "config.ghost"},
		{"a preset section of an adapter whose package cannot be loaded", preset(`{` + deny + `, "config": {"broken": {}}}`), config.ChangeAdapterNotInstalled, "config.broken"},

		{"an instance of an adapter with a required field, enabled without it", enable("second", "strict", ""), config.ChangeInvalid, "config.token"},
		{"an instance enabled with a value of the wrong kind", enable("second", "strict", `{"token": "t", "retries": "2"}`), config.ChangeInvalid, "config.retries"},
		{"an instance enabled with a value above the maximum", enable("second", "strict", `{"token": "t", "retries": 6}`), config.ChangeInvalid, "config.retries"},
		{"an instance enabled with a setting the adapter does not have", enable("second", "example", `{"workspace": "w", "colour": "red"}`), config.ChangeInvalid, "config.colour"},
		{"an instance of an adapter whose package cannot be loaded", enable("second", "broken", ""), config.ChangeAdapterNotInstalled, "broken"},

		{"a replaced configuration with a value of the wrong kind", setConfig("example", `{"workspace": 3}`), config.ChangeInvalid, "config.workspace"},
		{"a replaced configuration with a setting the adapter does not have", setConfig("example", `{"colour": "red"}`), config.ChangeInvalid, "config.colour"},
		{"a cleared configuration of an adapter with a required field", setConfig("legacy", `{}`), config.ChangeInvalid, "config.token"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			rg, c := loadedWithValues(t)
			before := snapshot(t, rg.mem)
			err := r.write(c)
			ce := changeReason(t, err)
			if ce.Reason != r.reason || ce.Field != r.field {
				t.Errorf("error = %v, want %v at %q", ce, r.reason, r.field)
			}
			if !reflect.DeepEqual(before, snapshot(t, rg.mem)) {
				t.Error("a refused change wrote to the disk")
			}
		})
	}
	if len(presets)+len(instances) != 0 {
		t.Error("a result was built for a change that was refused")
	}
}

// The values a manifest allows are written, and a stored instance that lacks a
// field the manifest now requires is still enabled: a field is required of a
// new instance, and what is stored is not checked again.
func TestWritingValuesTheManifestAllows(t *testing.T) {
	_, c := loadedWithValues(t)
	var presets []config.Presets
	var instances []config.Instances
	if _, err := c.AddPreset(request(1, 2), "new", []byte(`{"local_sources": "deny", "config": {"strict": {"mode": "fast", "names": ["a"]}, "example": {}}}`), result(&presets, `{}`)); err != nil {
		t.Fatalf("AddPreset: %v", err)
	}
	if _, err := c.EnableAdapter(request(2, 4), "second", "strict", []byte(`{"token": "t", "retries": 5}`), nil, result(&instances, `{}`)); err != nil {
		t.Fatalf("EnableAdapter: %v", err)
	}
	if _, err := c.SetInstanceConfig(request(3, 5), "second", []byte(`{"token": "u"}`), nil, result(&instances, `{}`)); err != nil {
		t.Fatalf("SetInstanceConfig: %v", err)
	}
	if _, err := c.EnableAdapter(request(4, 6), "legacy", "strict", nil, nil, result(&instances, `{}`)); err != nil {
		t.Fatalf("EnableAdapter of a stored instance without the required field: %v", err)
	}
	if got := c.Instances().Items["second"].Config; !reflect.DeepEqual(got, map[string]json.RawMessage{"token": json.RawMessage(`"u"`)}) {
		t.Errorf("configuration = %s", got)
	}
}

// A map value is stored in one form, whatever order and spacing it was written
// in: its entries sorted by key, with no blanks. Two spellings of the same map
// then store the same bytes, so the file and its displays do not change between
// two writes of the same configuration.
func TestAMapValueIsStoredSortedByKey(t *testing.T) {
	for _, doc := range []string{
		`{"b": ["x"], "a": ["y", "z"]}`,
		`{"a":["y","z"],"b":["x"]}`,
	} {
		_, c := loadedWithValues(t)
		var presets []config.Presets
		req := `{"local_sources": "deny", "config": {"strict": {"limits": ` + doc + `}}}`
		if _, err := c.AddPreset(request(1, 2), "new", []byte(req), result(&presets, `{}`)); err != nil {
			t.Fatalf("AddPreset: %v", err)
		}
		const want = `{"a":["y","z"],"b":["x"]}`
		if got := c.Presets().Items["new"].Config["strict"]["limits"]; string(got) != want {
			t.Errorf("%s is stored as %s, want %s", doc, got, want)
		}
	}
}
