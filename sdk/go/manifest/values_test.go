package manifest_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

func jsonOf(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCheckValue(t *testing.T) {
	list := func(n int) string { return "[" + strings.TrimSuffix(strings.Repeat(`"x",`, n), ",") + "]" }
	abs := string(jsonOf(t, t.TempDir()))
	str := manifest.Field{Key: "k", Type: manifest.FieldString, Title: "T"}
	dir := manifest.Field{Key: "k", Type: manifest.FieldDirectory, Title: "T"}
	enum := manifest.Field{Key: "k", Type: manifest.FieldEnum, Title: "T", Options: []string{"low", "high"}}
	num := manifest.Field{Key: "k", Type: manifest.FieldInteger, Title: "T"}
	bounded := manifest.Field{Key: "k", Type: manifest.FieldInteger, Title: "T", Min: ptr(int64(2)), Max: ptr(int64(9))}
	boolean := manifest.Field{Key: "k", Type: manifest.FieldBoolean, Title: "T"}
	strs := manifest.Field{Key: "k", Type: manifest.FieldStringList, Title: "T"}
	mapOf := func(v manifest.MapValue) manifest.Field {
		return manifest.Field{Key: "k", Type: manifest.FieldMap, Title: "T", Value: &v}
	}
	lists := mapOf(manifest.MapValue{Type: manifest.FieldStringList})
	ints := mapOf(manifest.MapValue{Type: manifest.FieldInteger, Min: ptr(int64(2)), Max: ptr(int64(9))})
	options := mapOf(manifest.MapValue{Type: manifest.FieldEnum, Options: []string{"low", "high"}})
	dirs := mapOf(manifest.MapValue{Type: manifest.FieldDirectory})
	entries := func(n int) string {
		items := make([]string, n)
		for i := range items {
			items[i] = fmt.Sprintf(`"m%d":[]`, i)
		}
		return "{" + strings.Join(items, ",") + "}"
	}

	rows := []struct {
		name  string
		field manifest.Field
		value string
		// bad is the path the error names; empty when the value is acceptable.
		bad string
	}{
		{"a string", str, `"abc"`, ""},
		{"an empty string", str, `""`, ""},
		{"a string of the largest length", str, `"` + repeat("é", adapterproto.MaxConfigString) + `"`, ""},
		{"a string over the largest length", str, `"` + repeat("é", adapterproto.MaxConfigString+1) + `"`, "k"},
		{"a number for a string", str, `3`, "k"},
		{"null for a string", str, `null`, "k"},
		{"an object for a string", str, `{}`, "k"},
		{"nothing for a string", str, ``, "k"},

		{"an absolute directory", dir, abs, ""},
		{"a relative directory", dir, `"work/tree"`, "k"},
		{"an empty directory", dir, `""`, "k"},
		{"a number for a directory", dir, `3`, "k"},

		{"an option", enum, `"low"`, ""},
		{"a value that is no option", enum, `"medium"`, "k"},
		{"an option in another case", enum, `"Low"`, "k"},
		{"a number for an enum", enum, `1`, "k"},

		{"an integer", num, `-7`, ""},
		{"zero", num, `0`, ""},
		{"the largest exact integer", num, `9007199254740991`, ""},
		{"an integer past the exact range", num, `9007199254740992`, "k"},
		{"a negative integer past the exact range", num, `-9007199254740992`, "k"},
		{"a fraction", num, `1.5`, "k"},
		{"a whole number written as a fraction", num, `1.0`, "k"},
		{"a whole number written with an exponent", num, `1e3`, "k"},
		{"a string for an integer", num, `"3"`, "k"},
		{"an integer at min", bounded, `2`, ""},
		{"an integer at max", bounded, `9`, ""},
		{"an integer below min", bounded, `1`, "k"},
		{"an integer above max", bounded, `10`, "k"},

		{"true", boolean, `true`, ""},
		{"false", boolean, `false`, ""},
		{"a string for a boolean", boolean, `"true"`, "k"},
		{"a number for a boolean", boolean, `1`, "k"},

		{"an empty list", strs, `[]`, ""},
		{"a list of strings", strs, `["a","b"]`, ""},
		{"a list of the largest length", strs, list(adapterproto.MaxConfigList), ""},
		{"a list over the largest length", strs, list(adapterproto.MaxConfigList + 1), "k"},
		{"a list item over the largest length", strs, `["` + repeat("x", adapterproto.MaxConfigString+1) + `"]`, "k[0]"},
		{"a list holding a number", strs, `["a",1]`, "k[1]"},
		{"a list holding null", strs, `[null]`, "k[0]"},
		{"a string for a list", strs, `"a"`, "k"},
		{"null for a list", strs, `null`, "k"},

		{"an empty map", lists, `{}`, ""},
		{"a map of lists", lists, `{"a":["x"],"b":[]}`, ""},
		{"a map of the largest size", lists, entries(adapterproto.MaxConfigMapEntries), ""},
		{"a map over the largest size", lists, entries(adapterproto.MaxConfigMapEntries + 1), "k"},
		{"an entry key of the largest length", lists, `{"` + repeat("m", 128) + `":[]}`, ""},
		{"an entry key over the largest length", lists, `{"` + repeat("m", 129) + `":[]}`, "k"},
		{"an empty entry key", lists, `{"":[]}`, "k"},
		{"an entry key with a space", lists, `{"a b":[]}`, "k"},
		{"an entry key with a control character", lists, `{"a\u0007":[]}`, "k"},
		{"an entry key outside ASCII", lists, `{"é":[]}`, "k"},
		{"an entry key of punctuation", lists, `{"claude-opus-4-6[1m]":[]}`, ""},
		{"a string for a map", lists, `"a"`, "k"},
		{"a list for a map", lists, `[]`, "k"},
		{"null for a map", lists, `null`, "k"},
		{"an entry of the wrong kind", lists, `{"a":"x"}`, "k.a"},
		{"a list item of the wrong kind", lists, `{"a":[1]}`, "k.a[0]"},
		{"null as an entry", lists, `{"a":null}`, "k.a"},
		{"a map as an entry", lists, `{"a":{}}`, "k.a"},
		{"an entry error is named by the first bad key", lists, `{"b":1,"a":2}`, "k.a"},
		{"an entry at min", ints, `{"a":2}`, ""},
		{"an entry below min", ints, `{"a":1}`, "k.a"},
		{"an entry above max", ints, `{"a":10}`, "k.a"},
		{"an entry that is an option", options, `{"a":"low"}`, ""},
		{"an entry that is no option", options, `{"a":"medium"}`, "k.a"},
		{"an absolute directory as an entry", dirs, `{"a":` + abs + `}`, ""},
		{"a relative directory as an entry", dirs, `{"a":"work"}`, "k.a"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			err := manifest.CheckValues([]manifest.Field{r.field}, map[string]json.RawMessage{"k": json.RawMessage(r.value)})
			if r.bad == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			var me *manifest.Error
			if !errors.As(err, &me) {
				t.Fatalf("want a *manifest.Error, got %v", err)
			}
			if me.Field != r.bad {
				t.Fatalf("error names %q, want %q (%v)", me.Field, r.bad, err)
			}
		})
	}
}

func TestCheckValueNamesNoKey(t *testing.T) {
	f := manifest.Field{Key: "k", Type: manifest.FieldBoolean, Title: "T"}
	if err := f.CheckValue(json.RawMessage(`true`)); err != nil {
		t.Fatal(err)
	}
	var me *manifest.Error
	if err := f.CheckValue(json.RawMessage(`1`)); !errors.As(err, &me) || me.Field != "" {
		t.Fatalf("want an error with no path, got %v", err)
	}
}

func TestCheckValues(t *testing.T) {
	fs := []manifest.Field{
		{Key: "home", Type: manifest.FieldString, Title: "T", Required: true},
		{Key: "level", Type: manifest.FieldInteger, Title: "T"},
		{Key: "tag", Type: manifest.FieldString, Title: "T", Required: true},
	}
	rows := []struct {
		name   string
		fields []manifest.Field
		values map[string]json.RawMessage
		bad    string
	}{
		{"every required field", fs, map[string]json.RawMessage{"home": json.RawMessage(`"h"`), "tag": json.RawMessage(`"t"`)}, ""},
		{"an optional field as well", fs, map[string]json.RawMessage{"home": json.RawMessage(`"h"`), "tag": json.RawMessage(`"t"`), "level": json.RawMessage(`1`)}, ""},
		{"a required field missing", fs, map[string]json.RawMessage{"home": json.RawMessage(`"h"`)}, "tag"},
		{"the first of two required fields missing", fs, map[string]json.RawMessage{"level": json.RawMessage(`1`)}, "home"},
		{"no values at all", fs, nil, "home"},
		{"a key that is no field", fs, map[string]json.RawMessage{"home": json.RawMessage(`"h"`), "tag": json.RawMessage(`"t"`), "extra": json.RawMessage(`1`)}, "extra"},
		{"a bad value", fs, map[string]json.RawMessage{"home": json.RawMessage(`"h"`), "tag": json.RawMessage(`"t"`), "level": json.RawMessage(`"1"`)}, "level"},
		{"a bad value is named before a missing field", fs, map[string]json.RawMessage{"level": json.RawMessage(`"1"`)}, "level"},
		{"no fields and no values", nil, nil, ""},
		{"no fields and empty values", nil, map[string]json.RawMessage{}, ""},
		{"no fields and a value", nil, map[string]json.RawMessage{"a": json.RawMessage(`1`)}, "a"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			err := manifest.CheckValues(r.fields, r.values)
			if r.bad == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			var me *manifest.Error
			if !errors.As(err, &me) || me.Field != r.bad {
				t.Fatalf("want an error at %q, got %v", r.bad, err)
			}
		})
	}
}

// The error is the same whatever order a map is walked in, so a refusal does
// not change between two identical requests.
func TestCheckValuesNamesTheFirstBadKey(t *testing.T) {
	values := map[string]json.RawMessage{}
	for i := 0; i < 20; i++ {
		values[fmt.Sprintf("k%02d", i)] = json.RawMessage(`1`)
	}
	for i := 0; i < 20; i++ {
		err := manifest.CheckValues(nil, values)
		var me *manifest.Error
		if !errors.As(err, &me) || me.Field != "k00" {
			t.Fatalf("want an error at k00, got %v", err)
		}
	}
}
