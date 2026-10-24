package strictjson_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/strictjson"
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

func TestNoDuplicateTopLevelKeys(t *testing.T) {
	tests := []struct {
		name, in string
		wantErr  string
	}{
		{"accepts an empty object", `{}`, ""},
		{"accepts distinct keys", `{"a":1,"b":2}`, ""},
		{"rejects a repeated key", `{"a":1,"b":2,"a":3}`, "a: duplicate key"},
		{"rejects a repeated key whose values are objects", `{"a":{},"a":{}}`, "a: duplicate key"},
		{"rejects a repeated key after a nested object", `{"a":{"x":[1,{"y":2}]},"b":1,"b":2}`, "b: duplicate key"},
		{"does not look inside an object value", `{"a":{"x":1,"x":2}}`, ""},
		{"does not look inside a list value", `{"a":[{"x":1,"x":2}],"b":[[{"y":1,"y":1}]]}`, ""},
		{"accepts the same key at two depths", `{"a":{"a":{"a":1}},"b":1}`, ""},
		{"a repeated key is a repeated key whatever follows it", `{"a":1,"a":[{"x":1,"x":2}]}`, "a: duplicate key"},
		{"reports truncated input", `{"a":{"b":`, "strictjson"},
		{"reads a number too large for a float64", `{"a":1e700,"b":{"c":-1e700}}`, ""},
		{"finds a repeat next to a number too large for a float64", `{"a":1e700,"a":2}`, "a: duplicate key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := strictjson.NoDuplicateTopLevelKeys([]byte(tc.in))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("got %v, want success", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// openInner ignores members it has no field for; the document around it does not.
type openInner struct {
	N    int       `json:"n"`
	Deep *inner    `json:"deep,omitempty"`
	Open *openLeaf `json:"open,omitempty"`
}

func (o *openInner) UnmarshalJSON(b []byte) error {
	type plain openInner
	return strictjson.DecodeOpen(b, (*plain)(o))
}

type openLeaf struct {
	M int `json:"m"`
}

func (o *openLeaf) UnmarshalJSON(b []byte) error {
	type plain openLeaf
	return strictjson.DecodeOpen(b, (*plain)(o))
}

type openDoc struct {
	Name  string    `json:"name"`
	Inner openInner `json:"inner"`
}

func TestDecodeOpen(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    *openDoc
		wantErr string
	}{
		{"decodes an object without extras", `{"name":"a","inner":{"n":1}}`, &openDoc{Name: "a", Inner: openInner{N: 1}}, ""},
		{"ignores an unknown member of the open object", `{"name":"a","inner":{"n":1,"future":{"x":[1,2]}}}`, &openDoc{Name: "a", Inner: openInner{N: 1}}, ""},
		{"ignores an unknown member of any JSON type", `{"name":"a","inner":{"n":1,"a":null,"b":"s","c":[],"d":1.5,"e":true}}`, &openDoc{Name: "a", Inner: openInner{N: 1}}, ""},
		{"ignores a member that differs only in case", `{"name":"a","inner":{"n":1,"N":2}}`, &openDoc{Name: "a", Inner: openInner{N: 1}}, ""},
		{"still refuses an unknown member of the document around it", `{"name":"a","inner":{"n":1},"future":1}`, nil, "future: unknown field"},
		{"still refuses an unknown member below a known member", `{"name":"a","inner":{"n":1,"deep":{"n":1,"future":1}}}`, nil, "unknown field"},
		{"opens a nested open object too", `{"name":"a","inner":{"n":1,"open":{"m":2,"future":true}}}`, &openDoc{Name: "a", Inner: openInner{N: 1, Open: &openLeaf{M: 2}}}, ""},
		{"still requires a known member", `{"name":"a","inner":{"future":1}}`, nil, "required field is missing"},
		{"still refuses null for a known member", `{"name":"a","inner":{"n":null}}`, nil, "null"},
		{"still checks the type of a known member", `{"name":"a","inner":{"n":"x"}}`, nil, "strictjson"},
		{"refuses null for the open object", `{"name":"a","inner":null}`, nil, "null"},
		{"refuses a list for the open object", `{"name":"a","inner":[]}`, nil, "strictjson"},
		{"refuses a repeated key among the ignored members", `{"name":"a","inner":{"n":1,"future":1,"future":2}}`, nil, "duplicate key"},
		{"refuses a repeated key inside an ignored member", `{"name":"a","inner":{"n":1,"future":{"k":1,"k":2}}}`, nil, "duplicate key"},
		{"refuses a repeated known key", `{"name":"a","inner":{"n":1,"n":2}}`, nil, "duplicate key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var d openDoc
			err := strictjson.Decode([]byte(tc.in), &d)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %v, want an error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, want := toJSON(t, d), toJSON(t, *tc.want); got != want {
				t.Fatalf("decoded %s, want %s", got, want)
			}
		})
	}
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDecodeOpenNeedsAPointerToAStruct(t *testing.T) {
	for _, v := range []any{nil, openLeaf{}, (*openLeaf)(nil), new(int), new(map[string]int)} {
		if err := strictjson.DecodeOpen([]byte(`{}`), v); err == nil {
			t.Errorf("DecodeOpen into %T succeeded", v)
		}
	}
}

type arrays struct {
	Pair  [2]inner  `json:"pair"`
	Ptrs  [2]*inner `json:"ptrs"`
	Names [2]string `json:"names"`
}

// The rules hold inside a Go array as they do inside a slice: a document that
// a slice field would refuse must be refused for an array field too.
func TestDecodeAppliesTheRulesInsideGoArrays(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string // empty means success
	}{
		{"accepts a well-formed document", `{"pair":[{"n":1},{"n":2}],"ptrs":[{"n":1},{"n":2}],"names":["a","b"]}`, ""},
		{"rejects an element that is null", `{"pair":[{"n":1},{"n":2}],"ptrs":[{"n":1},null],"names":["a","b"]}`, "ptrs[1]: null is not a value"},
		{"rejects a null scalar element", `{"pair":[{"n":1},{"n":2}],"ptrs":[{"n":1},{"n":2}],"names":[null,"b"]}`, "names[0]: null is not a value"},
		{"rejects a struct element that misses a required field", `{"pair":[{"n":1},{}],"ptrs":[{"n":1},{"n":2}],"names":["a","b"]}`, "pair[1].n: required field is missing"},
		{"rejects a key of the wrong case in an element", `{"pair":[{"N":1},{"n":2}],"ptrs":[{"n":1},{"n":2}],"names":["a","b"]}`, "pair[0].N: unknown field"},
		{"rejects an unknown field in an element", `{"pair":[{"n":1},{"n":2,"x":1}],"ptrs":[{"n":1},{"n":2}],"names":["a","b"]}`, "pair[1].x: unknown field"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var a arrays
			err := strictjson.Decode([]byte(tc.in), &a)
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
