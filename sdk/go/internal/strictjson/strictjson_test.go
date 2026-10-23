package strictjson_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/internal/strictjson"
)

type level string

func (l *level) UnmarshalText(b []byte) error {
	if string(b) != "low" && string(b) != "high" {
		return json.Unmarshal([]byte("x"), new(int))
	}
	*l = level(b)
	return nil
}

type inner struct {
	N int `json:"n"`
}

type doc struct {
	Name     string            `json:"name"`
	Count    int               `json:"count"`
	Flag     bool              `json:"flag,omitempty"`
	Note     string            `json:"note,omitempty"`
	Limit    int               `json:"limit,omitempty"`
	Cap      *int              `json:"cap,omitempty"`
	Tags     []string          `json:"tags,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Items    []inner           `json:"items"`
	Sub      *inner            `json:"sub,omitempty"`
	Level    level             `json:"level,omitempty"`
	Raw      json.RawMessage   `json:"raw,omitempty"`
	Anything any               `json:"anything,omitempty"`
}

const base = `{"name":"a","count":0,"items":[]`

func TestDecode(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string // substring of the error, empty means success
	}{
		{"accepts the minimal document", base + `}`, ""},
		{"accepts false for an omitempty bool", base + `,"flag":false}`, ""},
		{"accepts a zero for a pointer field", base + `,"cap":0}`, ""},
		{"accepts nested required and optional fields", base + `,"sub":{"n":0},"tags":["x"],"labels":{"k":"v"}}`, ""},
		{"accepts null in a raw message", base + `,"raw":null}`, ""},
		{"accepts anything in an interface field", base + `,"anything":{"x":[null]}}`, ""},
		{"rejects a key that differs only in case", `{"Name":"a","count":0,"items":[]}`, "unknown field"},
		{"rejects an unknown field", base + `,"extra":1}`, "extra: unknown field"},
		{"rejects an unknown nested field", base + `,"sub":{"n":1,"m":2}}`, "sub.m: unknown field"},
		{"rejects a missing required field", `{"name":"a","items":[]}`, "count: required field is missing"},
		{"rejects a missing required list", `{"name":"a","count":1}`, "items: required field is missing"},
		{"rejects a missing nested required field", base + `,"sub":{}}`, "sub.n: required field is missing"},
		{"rejects null for a string", `{"name":null,"count":0,"items":[]}`, "name: null is not a value"},
		{"rejects null for a number", `{"name":"a","count":null,"items":[]}`, "count: null"},
		{"rejects null for a list", `{"name":"a","count":0,"items":null}`, "items: null"},
		{"rejects null for an optional field", base + `,"note":null}`, "note: null"},
		{"rejects null inside a list", `{"name":"a","count":0,"items":[null]}`, "items[0]: null"},
		{"rejects null for a map value", base + `,"labels":{"k":null}}`, "labels.k: null"},
		{"rejects null for a text-unmarshaled type", base + `,"level":null}`, "level: null"},
		{"rejects an explicit empty string for an optional field", base + `,"note":""}`, "note: explicit empty value"},
		{"rejects an explicit zero for an optional number", base + `,"limit":0}`, "limit: explicit empty value"},
		{"rejects an explicit negative zero for an optional number", base + `,"limit":-0}`, "limit: explicit empty value"},
		{"rejects an explicit empty list for an optional field", base + `,"tags":[]}`, "tags: explicit empty value"},
		{"rejects an explicit empty map for an optional field", base + `,"labels":{}}`, "labels: explicit empty value"},
		{"accepts an empty required list", `{"name":"a","count":0,"items":[]}`, ""},
		{"rejects a wrong type", `{"name":1,"count":0,"items":[]}`, "name"},
		{"rejects an object where a list is expected", `{"name":"a","count":0,"items":{}}`, "items: expected an array"},
		{"rejects a list where an object is expected", base + `,"sub":[]}`, "sub: expected an object"},
		{"rejects data after the value", base + `} {}`, "data after"},
		{"rejects invalid UTF-8", "{\"name\":\"\xff\",\"count\":0,\"items\":[]}", "UTF-8"},
		{"rejects a value that is not an object", `[]`, "expected an object"},
		{"rejects truncated input", `{"name":`, "strictjson"},
		{"rejects an unknown text value through the type", base + `,"level":"medium"}`, "strictjson"},
		{"rejects a duplicate key", `{"name":"a","name":"b","count":0,"items":[]}`, "name: duplicate key"},
		{"rejects a duplicate key in a nested object", base + `,"sub":{"n":1,"n":2}}`, "sub.n: duplicate key"},
		{"rejects a duplicate key in a list element", `{"name":"a","count":0,"items":[{"n":1},{"n":1,"n":1}]}`, "items[1].n: duplicate key"},
		{"rejects a duplicate key inside a raw message", base + `,"raw":{"k":1,"k":2}}`, "raw.k: duplicate key"},
		{"accepts the same key in different objects", `{"name":"a","count":0,"items":[{"n":1},{"n":1}],"sub":{"n":1}}`, ""},
		{"accepts a known text value", base + `,"level":"low"}`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var d doc
			err := strictjson.Decode([]byte(tc.in), &d)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Decode(%s) = %v, want success", tc.in, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Decode(%s) = %v, want an error containing %q", tc.in, err, tc.wantErr)
			}
		})
	}
}

func TestDecodeNeedsPointer(t *testing.T) {
	if err := strictjson.Decode([]byte(`{}`), doc{}); err == nil {
		t.Fatal("Decode into a non-pointer succeeded")
	}
	var p *doc
	if err := strictjson.Decode([]byte(`{}`), p); err == nil {
		t.Fatal("Decode into a nil pointer succeeded")
	}
}

type untagged struct {
	A int
}

func TestDecodeUntaggedFieldPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("an untagged field did not panic")
		}
	}()
	_ = strictjson.Decode([]byte(`{"A":1}`), &untagged{})
}

func FuzzDecode(f *testing.F) {
	for _, s := range []string{base + `}`, base + `,"sub":{"n":3},"raw":[1,2]}`, `{`, `null`, `[]`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var d doc
		if err := strictjson.Decode(data, &d); err != nil {
			return
		}
		// Whatever decodes strictly must also decode with the standard library
		// to the same value: strictness only removes inputs.
		var std doc
		if err := json.Unmarshal(data, &std); err != nil {
			t.Fatalf("strict accepted %q but encoding/json rejects it: %v", data, err)
		}
		a, _ := json.Marshal(d)
		b, _ := json.Marshal(std)
		if string(a) != string(b) {
			t.Fatalf("strict and standard decoding differ for %q: %s vs %s", data, a, b)
		}
	})
}
