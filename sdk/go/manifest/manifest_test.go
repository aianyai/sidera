package manifest_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

func ptr[T any](v T) *T { return &v }

func repeat(s string, n int) string { return strings.Repeat(s, n) }

func base() manifest.Manifest {
	return manifest.Manifest{
		ManifestVersion: 1, ID: "example", Version: "0.3.1", Protocol: adapterproto.Range{Min: 1, Max: 1},
		Entry:     manifest.Entry{Program: "bin/sidera-adapter-example", Args: []string{"--stdio"}},
		Platforms: []manifest.Platform{{OS: "linux", Arch: "amd64"}, {OS: "darwin", Arch: "arm64"}},
		Requires:  []manifest.Requirement{{Program: "git", OS: []string{"linux", "darwin"}}},
		Tested:    []manifest.TestedRange{{Runtime: "r", Min: "2.1.306", Max: "2.1.308"}},
		Config: &manifest.Config{
			Instance: []manifest.Field{{Key: "home", Type: manifest.FieldDirectory, Title: "Home", Required: true}},
			Preset:   []manifest.Field{{Key: "model", Type: manifest.FieldString, Title: "Model", Default: json.RawMessage(`"standard"`)}},
		},
		Budgets:    []manifest.Budget{{Frontend: manifest.FrontendCLI, Unit: manifest.UnitChars, Limit: 27000}},
		AckMarkers: &manifest.AckMarkers{Replaced: []manifest.Marker{{Literal: ptr("[saved]")}}, Truncated: []manifest.Marker{{Regexp: ptr(`\.\.\. \d+ more`)}}},
	}
}

type mrow struct {
	name    string
	mutate  func(*manifest.Manifest)
	wantErr string
	beyond  bool
}

func runManifestRows(t *testing.T, rows []mrow) {
	t.Helper()
	if err := base().Validate(); err != nil {
		t.Fatalf("the base manifest is invalid: %v", err)
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			m := base()
			r.mutate(&m)
			err := m.Validate()
			if r.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want valid", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate accepted it, want an error mentioning %q", r.wantErr)
			}
			if !strings.Contains(err.Error(), r.wantErr) {
				t.Fatalf("Validate = %v, want an error mentioning %q", err, r.wantErr)
			}
			if got := errors.Is(err, manifest.ErrBeyondSchema); got != r.beyond {
				t.Fatalf("beyond-schema = %v, want %v (%v)", got, r.beyond, err)
			}
		})
	}
}

func TestIdentityRules(t *testing.T) {
	runManifestRows(t, []mrow{
		{"manifest version must be 1", func(m *manifest.Manifest) { m.ManifestVersion = 2 }, "manifest_version", false},
		{"manifest version zero", func(m *manifest.Manifest) { m.ManifestVersion = 0 }, "manifest_version", false},
		{"id lowercase with digits and dashes", func(m *manifest.Manifest) { m.ID = "chat-bridge-2" }, "", false},
		{"id of 32 characters", func(m *manifest.Manifest) { m.ID = "a" + repeat("b", 31) }, "", false},
		{"id of 33 characters", func(m *manifest.Manifest) { m.ID = "a" + repeat("b", 32) }, "id", false},
		{"id uppercase", func(m *manifest.Manifest) { m.ID = "Example" }, "id", false},
		{"id starting with a dash", func(m *manifest.Manifest) { m.ID = "-x" }, "id", false},
		{"id with an underscore", func(m *manifest.Manifest) { m.ID = "a_b" }, "id", false},
		{"id empty", func(m *manifest.Manifest) { m.ID = "" }, "id", false},
		{"version SemVer", func(m *manifest.Manifest) { m.Version = "10.20.30-alpha.1+build.5" }, "", false},
		{"version without a patch", func(m *manifest.Manifest) { m.Version = "1.0" }, "version", false},
		{"version with a v", func(m *manifest.Manifest) { m.Version = "v1.0.0" }, "version", false},
		{"version with a leading zero", func(m *manifest.Manifest) { m.Version = "1.01.0" }, "version", false},
		{"version too long", func(m *manifest.Manifest) { m.Version = "1.0.0-" + repeat("a", 60) }, "version", false},
		{"version empty", func(m *manifest.Manifest) { m.Version = "" }, "version", false},
		{"protocol range of one", func(m *manifest.Manifest) { m.Protocol = adapterproto.Range{Min: 2, Max: 2} }, "", false},
		{"protocol range reversed", func(m *manifest.Manifest) { m.Protocol = adapterproto.Range{Min: 2, Max: 1} }, "protocol", true},
		{"protocol min zero", func(m *manifest.Manifest) { m.Protocol = adapterproto.Range{Min: 0, Max: 1} }, "protocol.min", false},
		{"protocol max over the bound", func(m *manifest.Manifest) { m.Protocol = adapterproto.Range{Min: 1, Max: 65536} }, "protocol.max", false},
	})
}

func TestEntryRules(t *testing.T) {
	for program, ok := range map[string]bool{
		"adapter": true, "bin/adapter": true, "bin/sub/adapter.exe": true, "a.b/c": true, "..a/b": true, "a../b": true, "a..b": true, "...": true, ".a": true,
		"a/.../b": true, "a b/c": true, "bin\\adapter": true, "ab:c": true, "1:a": true, repeat("p", 256): true,
		"a:b": false, "": false, "/adapter": false, "\\adapter": false, "C:adapter.exe": false, "c:\\adapter.exe": false, "D:/x": false,
		"..": false, "../adapter": false, "bin/..": false, "bin/../adapter": false, "bin\\..\\adapter": false, "a/../../b": false, "./../a": false,
		"..\\a": false, "a\\..": false, repeat("p", 257): false,
	} {
		m := base()
		m.Entry.Program = program
		err := m.Validate()
		if (err == nil) != ok {
			t.Errorf("entry program %q: Validate = %v, want ok=%v", program, err, ok)
		}
	}
	runManifestRows(t, []mrow{
		{"no arguments", func(m *manifest.Manifest) { m.Entry.Args = nil }, "", false},
		{"an empty argument", func(m *manifest.Manifest) { m.Entry.Args = []string{""} }, "", false},
		{"64 arguments", func(m *manifest.Manifest) { m.Entry.Args = make([]string, 64) }, "", false},
		{"65 arguments", func(m *manifest.Manifest) { m.Entry.Args = make([]string, 65) }, "entry.args", false},
		{"an argument of 1024 characters", func(m *manifest.Manifest) { m.Entry.Args = []string{repeat("a", 1024)} }, "", false},
		{"an argument of 1025 characters", func(m *manifest.Manifest) { m.Entry.Args = []string{repeat("a", 1025)} }, "entry.args[0]", false},
	})
}

func TestPlatformAndRequirementRules(t *testing.T) {
	runManifestRows(t, []mrow{
		{"platforms required", func(m *manifest.Manifest) { m.Platforms = nil }, "platforms", false},
		{"platforms empty", func(m *manifest.Manifest) { m.Platforms = []manifest.Platform{} }, "platforms", false},
		{"os unknown", func(m *manifest.Manifest) { m.Platforms = []manifest.Platform{{OS: "beos", Arch: "amd64"}} }, "platforms[0].os", false},
		{"os in another case", func(m *manifest.Manifest) { m.Platforms = []manifest.Platform{{OS: "Linux", Arch: "amd64"}} }, "platforms[0].os", false},
		{"arch unknown", func(m *manifest.Manifest) { m.Platforms = []manifest.Platform{{OS: "linux", Arch: "x86_64"}} }, "platforms[0].arch", false},
		{"unusual but known pair", func(m *manifest.Manifest) { m.Platforms = []manifest.Platform{{OS: "plan9", Arch: "mips64p32le"}} }, "", false},
		{"duplicate platform", func(m *manifest.Manifest) {
			m.Platforms = []manifest.Platform{{OS: "linux", Arch: "amd64"}, {OS: "linux", Arch: "amd64"}}
		}, "platforms[1]", false},
		{"same os with another arch", func(m *manifest.Manifest) {
			m.Platforms = []manifest.Platform{{OS: "linux", Arch: "amd64"}, {OS: "linux", Arch: "arm64"}}
		}, "", false},
		{"64 platforms", func(m *manifest.Manifest) { m.Platforms = platforms(64) }, "", false},
		{"65 platforms", func(m *manifest.Manifest) { m.Platforms = platforms(65) }, "platforms", false},
		{"no requirements", func(m *manifest.Manifest) { m.Requires = nil }, "", false},
		{"requirement for every os", func(m *manifest.Manifest) { m.Requires = []manifest.Requirement{{Program: "git"}} }, "", false},
		{"requirement without a program", func(m *manifest.Manifest) { m.Requires = []manifest.Requirement{{}} }, "requires[0].program", false},
		{"requirement program over the limit", func(m *manifest.Manifest) { m.Requires = []manifest.Requirement{{Program: repeat("p", 129)}} }, "requires[0].program", false},
		{"requirement os unknown", func(m *manifest.Manifest) {
			m.Requires = []manifest.Requirement{{Program: "git", OS: []string{"macos"}}}
		}, "requires[0].os[0]", false},
		{"requirement os repeated", func(m *manifest.Manifest) {
			m.Requires = []manifest.Requirement{{Program: "git", OS: []string{"linux", "linux"}}}
		}, "requires[0].os", false},
		{"requirement os empty list", func(m *manifest.Manifest) { m.Requires = []manifest.Requirement{{Program: "git", OS: []string{}}} }, "requires[0].os", false},
		{"empty requirement list", func(m *manifest.Manifest) { m.Requires = []manifest.Requirement{} }, "requires", false},
	})
}

func platforms(n int) []manifest.Platform {
	arches := manifest.KnownArch()
	oses := manifest.KnownOS()
	var out []manifest.Platform
	for _, o := range oses {
		for _, a := range arches {
			if len(out) < n {
				out = append(out, manifest.Platform{OS: o, Arch: a})
			}
		}
	}
	return out
}

func TestTestedRangeRules(t *testing.T) {
	r := func(min, max string) []manifest.TestedRange {
		return []manifest.TestedRange{{Runtime: "r", Min: min, Max: max}}
	}
	runManifestRows(t, []mrow{
		{"no tested ranges", func(m *manifest.Manifest) { m.Tested = nil }, "", false},
		{"a range of one version", func(m *manifest.Manifest) { m.Tested = r("2.1.306", "2.1.306") }, "", false},
		{"min above max", func(m *manifest.Manifest) { m.Tested = r("2.1.308", "2.1.306") }, "tested[0]", true},
		{"compared as numbers, not text", func(m *manifest.Manifest) { m.Tested = r("2.10.0", "2.9.0") }, "tested[0]", true},
		{"compared as numbers, ascending", func(m *manifest.Manifest) { m.Tested = r("2.9.0", "2.10.0") }, "", false},
		{"a pre-release is below its release", func(m *manifest.Manifest) { m.Tested = r("1.0.0", "1.0.0-rc.1") }, "tested[0]", true},
		{"a pre-release range", func(m *manifest.Manifest) { m.Tested = r("1.0.0-alpha", "1.0.0-beta") }, "", false},
		{"build metadata does not order", func(m *manifest.Manifest) { m.Tested = r("1.0.0+b", "1.0.0+a") }, "", false},
		{"min not SemVer", func(m *manifest.Manifest) { m.Tested = r("2.1", "2.2.0") }, "tested[0].min", false},
		{"max not SemVer", func(m *manifest.Manifest) { m.Tested = r("2.1.0", "latest") }, "tested[0].max", false},
		{"runtime required", func(m *manifest.Manifest) { m.Tested = []manifest.TestedRange{{Min: "1.0.0", Max: "1.0.0"}} }, "tested[0].runtime", false},
		{"runtime over the limit", func(m *manifest.Manifest) {
			m.Tested = []manifest.TestedRange{{Runtime: repeat("r", 65), Min: "1.0.0", Max: "1.0.0"}}
		}, "tested[0].runtime", false},
		{"two ranges for one runtime", func(m *manifest.Manifest) {
			m.Tested = []manifest.TestedRange{{Runtime: "r", Min: "1.0.0", Max: "1.2.0"}, {Runtime: "r", Min: "2.0.0", Max: "2.0.1"}}
		}, "", false},
		{"an empty list", func(m *manifest.Manifest) { m.Tested = []manifest.TestedRange{} }, "tested", false},
	})
}

func TestTestedRangeContains(t *testing.T) {
	m := base()
	m.Tested = []manifest.TestedRange{{Runtime: "r", Min: "2.1.306", Max: "2.1.308"}, {Runtime: "r", Min: "3.0.0-rc.1", Max: "3.0.0"}, {Runtime: "other", Min: "9.0.0", Max: "9.9.9"}}
	for _, tc := range []struct {
		runtime, version string
		want             bool
	}{
		{"r", "2.1.306", true}, {"r", "2.1.307", true}, {"r", "2.1.308", true}, {"r", "2.1.305", false}, {"r", "2.1.309", false},
		{"r", "2.1.308+build", true}, {"r", "2.1.308-rc.1", true}, {"r", "2.1.306-rc.1", false},
		{"r", "3.0.0-rc.2", true}, {"r", "3.0.0", true}, {"r", "3.0.1", false}, {"r", "3.0.0-beta", false},
		{"other", "2.1.307", false}, {"other", "9.1.0", true}, {"missing", "2.1.307", false},
		{"r", "not a version", false}, {"r", "", false}, {"r", "2.1", false},
	} {
		if got := m.Tests(tc.runtime, tc.version); got != tc.want {
			t.Errorf("Tests(%q, %q) = %v, want %v", tc.runtime, tc.version, got, tc.want)
		}
	}
}

func TestConfigFieldRules(t *testing.T) {
	field := func(f manifest.Field) func(*manifest.Manifest) {
		return func(m *manifest.Manifest) { m.Config = &manifest.Config{Instance: []manifest.Field{f}} }
	}
	str := func(s string) json.RawMessage { b, _ := json.Marshal(s); return b }
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	f := func(typ manifest.FieldType, mod func(*manifest.Field)) manifest.Field {
		fld := manifest.Field{Key: "k", Type: typ, Title: "T"}
		if mod != nil {
			mod(&fld)
		}
		return fld
	}
	rows := []mrow{
		{"no config", func(m *manifest.Manifest) { m.Config = nil }, "", false},
		{"an empty config", func(m *manifest.Manifest) { m.Config = &manifest.Config{} }, "", false},
		{"every type", func(m *manifest.Manifest) {
			m.Config = &manifest.Config{Instance: []manifest.Field{
				f("string", nil), {Key: "i", Type: "integer", Title: "T"}, {Key: "b", Type: "boolean", Title: "T"},
				{Key: "e", Type: "enum", Title: "T", Options: []string{"x"}}, {Key: "d", Type: "directory", Title: "T"}, {Key: "l", Type: "string_list", Title: "T"}}}
		}, "", false},
		{"type unknown", field(f("path", nil)), "type", false},
		{"type missing", field(f("", nil)), "type", false},
		{"key uppercase", field(f("string", func(x *manifest.Field) { x.Key = "Home" })), "key", false},
		{"key with a dash", field(f("string", func(x *manifest.Field) { x.Key = "a-b" })), "key", false},
		{"key of 64 characters", field(f("string", func(x *manifest.Field) { x.Key = "a" + repeat("b", 63) })), "", false},
		{"key of 65 characters", field(f("string", func(x *manifest.Field) { x.Key = "a" + repeat("b", 64) })), "key", false},
		{"title required", field(f("string", func(x *manifest.Field) { x.Title = "" })), "title", false},
		{"title over the limit", field(f("string", func(x *manifest.Field) { x.Title = repeat("t", 129) })), "title", false},
		{"description over the limit", field(f("string", func(x *manifest.Field) { x.Description = repeat("d", 1025) })), "description", false},
		{"required flag", field(f("string", func(x *manifest.Field) { x.Required = true })), "", false},
		{"enum without options", field(f("enum", nil)), "options", false},
		{"enum with empty options", field(f("enum", func(x *manifest.Field) { x.Options = []string{} })), "options", false},
		{"enum with a repeated option", field(f("enum", func(x *manifest.Field) { x.Options = []string{"a", "a"} })), "options", false},
		{"enum with an empty option", field(f("enum", func(x *manifest.Field) { x.Options = []string{""} })), "options[0]", false},
		{"enum with 64 options", field(f("enum", func(x *manifest.Field) { x.Options = numbered(64) })), "", false},
		{"enum with 65 options", field(f("enum", func(x *manifest.Field) { x.Options = numbered(65) })), "options", false},
		{"options on a string", field(f("string", func(x *manifest.Field) { x.Options = []string{"a"} })), "options", false},
		{"min on a string", field(f("string", func(x *manifest.Field) { x.Min = ptr(int64(1)) })), "min", false},
		{"max on a boolean", field(f("boolean", func(x *manifest.Field) { x.Max = ptr(int64(1)) })), "min", false},
		{"integer with bounds", field(f("integer", func(x *manifest.Field) { x.Min, x.Max = ptr(int64(0)), ptr(int64(10)) })), "", false},
		{"integer with equal bounds", field(f("integer", func(x *manifest.Field) { x.Min, x.Max = ptr(int64(5)), ptr(int64(5)) })), "", false},
		{"integer with min above max", field(f("integer", func(x *manifest.Field) { x.Min, x.Max = ptr(int64(5)), ptr(int64(4)) })), "min", true},
		{"integer bound over the exact range", field(f("integer", func(x *manifest.Field) { x.Max = ptr(int64(1) << 53) })), "min", false},
		{"default string for a string", field(f("string", func(x *manifest.Field) { x.Default = str("x") })), "", false},
		{"default empty string for a string", field(f("string", func(x *manifest.Field) { x.Default = str("") })), "", false},
		{"default integer for a string", field(f("string", func(x *manifest.Field) { x.Default = raw(`3`) })), "default", false},
		{"default string over the limit", field(f("string", func(x *manifest.Field) { x.Default = str(repeat("s", 4097)) })), "default", false},
		{"default null", field(f("string", func(x *manifest.Field) { x.Default = raw(`null`) })), "default", false},
		{"default for a directory", field(f("directory", func(x *manifest.Field) { x.Default = str("/srv") })), "", false},
		{"default zero for an integer", field(f("integer", func(x *manifest.Field) { x.Default = raw(`0`) })), "", false},
		{"default string for an integer", field(f("integer", func(x *manifest.Field) { x.Default = str("3") })), "default", false},
		{"default float for an integer", field(f("integer", func(x *manifest.Field) { x.Default = raw(`1.5`) })), "default", false},
		{"default integer below min", field(f("integer", func(x *manifest.Field) { x.Min = ptr(int64(5)); x.Default = raw(`4`) })), "default", true},
		{"default integer at min", field(f("integer", func(x *manifest.Field) { x.Min = ptr(int64(5)); x.Default = raw(`5`) })), "", false},
		{"default integer above max", field(f("integer", func(x *manifest.Field) { x.Max = ptr(int64(5)); x.Default = raw(`6`) })), "default", true},
		{"default integer at max", field(f("integer", func(x *manifest.Field) { x.Max = ptr(int64(5)); x.Default = raw(`5`) })), "", false},
		{"default false for a boolean", field(f("boolean", func(x *manifest.Field) { x.Default = raw(`false`) })), "", false},
		{"default string for a boolean", field(f("boolean", func(x *manifest.Field) { x.Default = str("true") })), "default", false},
		{"default option for an enum", field(f("enum", func(x *manifest.Field) { x.Options = []string{"a", "b"}; x.Default = str("b") })), "", false},
		{"default not an option", field(f("enum", func(x *manifest.Field) { x.Options = []string{"a", "b"}; x.Default = str("c") })), "default", true},
		{"default empty list", field(f("string_list", func(x *manifest.Field) { x.Default = raw(`[]`) })), "", false},
		{"default list of strings", field(f("string_list", func(x *manifest.Field) { x.Default = raw(`["a","b"]`) })), "", false},
		{"default list of numbers", field(f("string_list", func(x *manifest.Field) { x.Default = raw(`[1]`) })), "default[0]", false},
		{"default list holding null", field(f("string_list", func(x *manifest.Field) { x.Default = raw(`["a",null]`) })), "default[1]", false},
		{"default string for a list", field(f("string_list", func(x *manifest.Field) { x.Default = str("a") })), "default", false},
		{"duplicate keys in a group", func(m *manifest.Manifest) {
			m.Config = &manifest.Config{Instance: []manifest.Field{f("string", nil), f("boolean", nil)}}
		}, "instance[1].key", true},
		{"the same key in two groups", func(m *manifest.Manifest) {
			m.Config = &manifest.Config{Instance: []manifest.Field{f("string", nil)}, Preset: []manifest.Field{f("string", nil)}}
		}, "", false},
		{"64 fields", func(m *manifest.Manifest) { m.Config = &manifest.Config{Preset: fields(64)} }, "", false},
		{"65 fields", func(m *manifest.Manifest) { m.Config = &manifest.Config{Preset: fields(65)} }, "preset", false},
		{"an empty group", func(m *manifest.Manifest) { m.Config = &manifest.Config{Instance: []manifest.Field{}} }, "instance", false},
	}
	runManifestRows(t, rows)
}

func numbered(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "o" + strings.Repeat("x", i)
	}
	return out
}

func fields(n int) []manifest.Field {
	out := make([]manifest.Field, n)
	for i := range out {
		out[i] = manifest.Field{Key: "k" + strings.Repeat("x", i), Type: manifest.FieldBoolean, Title: "T"}
	}
	return out
}

func TestBudgetRules(t *testing.T) {
	b := func(f manifest.Frontend, u manifest.Unit, l int) manifest.Budget {
		return manifest.Budget{Frontend: f, Unit: u, Limit: l}
	}
	runManifestRows(t, []mrow{
		{"no budgets", func(m *manifest.Manifest) { m.Budgets = nil }, "", false},
		{"all three front ends", func(m *manifest.Manifest) {
			m.Budgets = []manifest.Budget{b("cli", "chars", 1), b("mcp", "tokens", 131072), b("direct", "chars", 9000)}
		}, "", false},
		{"limit 1", func(m *manifest.Manifest) { m.Budgets = []manifest.Budget{b("cli", "chars", 1)} }, "", false},
		{"limit 131072", func(m *manifest.Manifest) { m.Budgets = []manifest.Budget{b("cli", "chars", 131072)} }, "", false},
		{"limit 0", func(m *manifest.Manifest) { m.Budgets = []manifest.Budget{b("cli", "chars", 0)} }, "budgets[0].limit", false},
		{"limit negative", func(m *manifest.Manifest) { m.Budgets = []manifest.Budget{b("cli", "chars", -1)} }, "budgets[0].limit", false},
		{"limit 131073", func(m *manifest.Manifest) { m.Budgets = []manifest.Budget{b("cli", "chars", 131073)} }, "budgets[0].limit", false},
		{"unit unknown", func(m *manifest.Manifest) { m.Budgets = []manifest.Budget{b("cli", "bytes", 100)} }, "budgets[0].unit", false},
		{"unit missing", func(m *manifest.Manifest) { m.Budgets = []manifest.Budget{b("cli", "", 100)} }, "budgets[0].unit", false},
		{"front end unknown", func(m *manifest.Manifest) { m.Budgets = []manifest.Budget{b("web", "chars", 100)} }, "budgets[0].frontend", false},
		{"front end missing", func(m *manifest.Manifest) { m.Budgets = []manifest.Budget{b("", "chars", 100)} }, "budgets[0].frontend", false},
		{"two budgets for one front end", func(m *manifest.Manifest) {
			m.Budgets = []manifest.Budget{b("cli", "chars", 100), b("cli", "tokens", 200)}
		}, "budgets[1].frontend", true},
		{"an empty list", func(m *manifest.Manifest) { m.Budgets = []manifest.Budget{} }, "budgets", false},
	})
}

func TestAckMarkerShape(t *testing.T) {
	lit := func(s string) manifest.Marker { return manifest.Marker{Literal: &s} }
	re := func(s string) manifest.Marker { return manifest.Marker{Regexp: &s} }
	runManifestRows(t, []mrow{
		{"no markers", func(m *manifest.Manifest) { m.AckMarkers = nil }, "", false},
		{"an empty declaration", func(m *manifest.Manifest) { m.AckMarkers = &manifest.AckMarkers{} }, "", false},
		{"a literal and a regexp", func(m *manifest.Manifest) {
			m.AckMarkers = &manifest.AckMarkers{Replaced: []manifest.Marker{lit("a"), re("b+")}}
		}, "", false},
		{"both set", func(m *manifest.Manifest) {
			m.AckMarkers = &manifest.AckMarkers{Replaced: []manifest.Marker{{Literal: ptr("a"), Regexp: ptr("b")}}}
		}, "exactly one", false},
		{"neither set", func(m *manifest.Manifest) { m.AckMarkers = &manifest.AckMarkers{Truncated: []manifest.Marker{{}}} }, "exactly one", false},
		{"an empty literal is a shape the daemon judges", func(m *manifest.Manifest) { m.AckMarkers = &manifest.AckMarkers{Replaced: []manifest.Marker{lit("")}} }, "", false},
		{"a regexp that does not compile is the daemon's to judge", func(m *manifest.Manifest) { m.AckMarkers = &manifest.AckMarkers{Replaced: []manifest.Marker{re("(")}} }, "", false},
		{"a regexp that matches nothing is the daemon's to judge", func(m *manifest.Manifest) {
			m.AckMarkers = &manifest.AckMarkers{Truncated: []manifest.Marker{re("x*")}}
		}, "", false},
		{"a marker of 1024 characters", func(m *manifest.Manifest) {
			m.AckMarkers = &manifest.AckMarkers{Replaced: []manifest.Marker{lit(repeat("é", 1024))}}
		}, "", false},
		{"a marker of 1025 characters", func(m *manifest.Manifest) {
			m.AckMarkers = &manifest.AckMarkers{Replaced: []manifest.Marker{lit(repeat("é", 1025))}}
		}, "replaced[0]", false},
		{"32 markers", func(m *manifest.Manifest) { m.AckMarkers = &manifest.AckMarkers{Truncated: markers(32)} }, "", false},
		{"33 markers", func(m *manifest.Manifest) { m.AckMarkers = &manifest.AckMarkers{Truncated: markers(33)} }, "truncated", false},
		{"an empty list", func(m *manifest.Manifest) { m.AckMarkers = &manifest.AckMarkers{Replaced: []manifest.Marker{}} }, "replaced", false},
	})
}

func markers(n int) []manifest.Marker {
	out := make([]manifest.Marker, n)
	for i := range out {
		s := "m"
		out[i] = manifest.Marker{Literal: &s}
	}
	return out
}

func TestParse(t *testing.T) {
	good, err := json.Marshal(base())
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Parse(good)
	if err != nil || m.ID != "example" {
		t.Fatalf("Parse = %+v, %v", m, err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"an unknown field":                 func(d map[string]any) { d["homepage"] = "x" },
		"a field in another case":          func(d map[string]any) { d["ID"] = d["id"]; delete(d, "id") },
		"a null field":                     func(d map[string]any) { d["version"] = nil },
		"a missing required field":         func(d map[string]any) { delete(d, "entry") },
		"an explicit empty list":           func(d map[string]any) { d["requires"] = []any{} },
		"an explicit empty object":         func(d map[string]any) { d["config"] = map[string]any{"instance": []any{}} },
		"a wrong type":                     func(d map[string]any) { d["manifest_version"] = "1" },
		"a float where an integer belongs": func(d map[string]any) { d["manifest_version"] = 1.5 },
		"an invalid value":                 func(d map[string]any) { d["id"] = "UPPER" },
	} {
		t.Run(name, func(t *testing.T) {
			var d map[string]any
			if err := json.Unmarshal(good, &d); err != nil {
				t.Fatal(err)
			}
			mutate(d)
			b, _ := json.Marshal(d)
			if _, err := manifest.Parse(b); err == nil {
				t.Fatalf("Parse accepted %s", b)
			}
		})
	}
	for name, in := range map[string]string{
		"a repeated key":       `{"manifest_version":1,"manifest_version":1,"id":"x","version":"1.0.0","protocol":{"min":1,"max":1},"entry":{"program":"p"},"platforms":[{"os":"linux","arch":"amd64"}]}`,
		"not an object":        `[]`,
		"trailing data":        string(good) + `{}`,
		"invalid UTF-8":        strings.Replace(string(good), "example", "ex\xffample", 1),
		"empty input":          ``,
		"a null manifest":      `null`,
		"an object with a bom": "\ufeff" + string(good),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := manifest.Parse([]byte(in)); err == nil {
				t.Fatalf("Parse accepted %q", in)
			}
		})
	}
	t.Run("a rule beyond the schema is reported as one", func(t *testing.T) {
		var d map[string]any
		_ = json.Unmarshal(good, &d)
		d["protocol"] = map[string]any{"min": 2, "max": 1}
		b, _ := json.Marshal(d)
		_, err := manifest.Parse(b)
		if !errors.Is(err, manifest.ErrBeyondSchema) {
			t.Fatalf("Parse = %v", err)
		}
	})
	t.Run("the error says where", func(t *testing.T) {
		var d map[string]any
		_ = json.Unmarshal(good, &d)
		d["budgets"] = []any{map[string]any{"frontend": "cli", "unit": "chars", "limit": 0}}
		b, _ := json.Marshal(d)
		_, err := manifest.Parse(b)
		var me *manifest.Error
		if !errors.As(err, &me) || me.Field != "budgets[0].limit" {
			t.Fatalf("Parse = %v", err)
		}
	})
}

func TestVocabularies(t *testing.T) {
	if got := len(manifest.FieldTypes()); got != 6 {
		t.Errorf("%d field types, want 6", got)
	}
	if got := manifest.BudgetFrontends(); len(got) != 3 || got[0] != "cli" || got[1] != "mcp" || got[2] != "direct" {
		t.Errorf("budget front ends = %v", got)
	}
	if got := manifest.Units(); len(got) != 2 || got[0] != "chars" || got[1] != "tokens" {
		t.Errorf("units = %v", got)
	}
	for _, os := range []string{"linux", "darwin", "windows", "freebsd", "android", "ios", "js", "wasip1"} {
		found := false
		for _, k := range manifest.KnownOS() {
			found = found || k == os
		}
		if !found {
			t.Errorf("GOOS %s is missing", os)
		}
	}
	for _, arch := range []string{"amd64", "arm64", "386", "arm", "riscv64", "loong64", "ppc64le", "s390x", "wasm", "mips64le"} {
		found := false
		for _, k := range manifest.KnownArch() {
			found = found || k == arch
		}
		if !found {
			t.Errorf("GOARCH %s is missing", arch)
		}
	}
	// The returned slices are copies.
	manifest.KnownOS()[0] = "changed"
	if manifest.KnownOS()[0] == "changed" {
		t.Error("KnownOS returns the table itself")
	}
}

func FuzzParse(f *testing.F) {
	full, _ := json.Marshal(base())
	for _, s := range []string{string(full), `{}`, `null`, `[]`,
		`{"manifest_version":1,"id":"x","version":"1.0.0","protocol":{"min":1,"max":1},"entry":{"program":"p"},"platforms":[{"os":"linux","arch":"amd64"}]}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := manifest.Parse(data)
		if err != nil {
			return
		}
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("a parsed manifest does not encode: %v", err)
		}
		again, err := manifest.Parse(out)
		if err != nil {
			t.Fatalf("the encoding %s of a parsed manifest no longer parses: %v", out, err)
		}
		out2, _ := json.Marshal(again)
		if string(out) != string(out2) {
			t.Fatalf("encoding is not stable:\n %s\n %s", out, out2)
		}
	})
}
