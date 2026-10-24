package strictjson_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// shapes has a field of every Go kind a document is decoded into, at several
// depths, so that each way a value can have the wrong JSON type has a position
// to land on.
type shapes struct {
	Name   string              `json:"name"`
	Count  int                 `json:"count"`
	Small  int8                `json:"small"`
	Byte   uint8               `json:"byte"`
	Ratio  float32             `json:"ratio"`
	Flag   bool                `json:"flag"`
	Note   string              `json:"note,omitempty"`
	List   []int               `json:"list"`
	Items  []inner             `json:"items"`
	Grid   [][]string          `json:"grid"`
	Pair   [2]int              `json:"pair"`
	Sub    *shapesSub          `json:"sub"`
	ByName map[string]inner    `json:"by_name"`
	Labels map[string]string   `json:"labels"`
	ByID   map[int]string      `json:"by_id"`
	Nest   map[string][]*inner `json:"nest"`
	Quoted int                 `json:"quoted,string"`
	Ptr    *int                `json:"ptr,omitempty"`
}

type shapesSub struct {
	N    int       `json:"n"`
	Deep *openLeaf `json:"deep,omitempty"`
}

// shapesBase is a document that decodes without error. A test replaces one
// member with a value of the wrong type.
func shapesBase(replace map[string]string) string {
	members := map[string]string{
		"name":    `"a"`,
		"count":   `1`,
		"small":   `1`,
		"byte":    `1`,
		"ratio":   `1.5`,
		"flag":    `true`,
		"list":    `[1,2]`,
		"items":   `[{"n":1},{"n":2},{"n":3}]`,
		"grid":    `[["a"],["b","c"]]`,
		"pair":    `[1,2]`,
		"sub":     `{"n":1}`,
		"by_name": `{"k":{"n":1}}`,
		"labels":  `{"k":"v"}`,
		"by_id":   `{"7":"v"}`,
		"nest":    `{"k":[{"n":1}]}`,
		"quoted":  `"5"`,
	}
	for k, v := range replace {
		members[k] = v
	}
	var parts []string
	for k, v := range members {
		parts = append(parts, `"`+k+`":`+v)
	}
	return `{` + strings.Join(parts, ",") + `}`
}

func TestShapesBaseDecodes(t *testing.T) {
	var s shapes
	if err := strictjson.Decode([]byte(shapesBase(nil)), &s); err != nil {
		t.Fatalf("the base document must decode: %v", err)
	}
}

// secret is a value that must never be echoed back by an error message.
const secret = "s3cr3t-31337"

func TestDecodeTypeErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		replace map[string]string
		path    string
	}{
		{"a string where an int is expected", map[string]string{"count": `"` + secret + `"`}, "count"},
		{"a number where a string is expected", map[string]string{"name": `31337`}, "name"},
		{"a number where a bool is expected", map[string]string{"flag": `31337`}, "flag"},
		{"a string where a bool is expected", map[string]string{"flag": `"` + secret + `"`}, "flag"},
		{"a bool where a number is expected", map[string]string{"count": `true`}, "count"},
		{"a number where an optional string is expected", map[string]string{"note": `31337`}, "note"},
		{"a string where an optional pointer to int is expected", map[string]string{"ptr": `"` + secret + `"`}, "ptr"},
		{"an object where a list is expected", map[string]string{"list": `{"k":"` + secret + `"}`}, "list"},
		{"a list where an object is expected", map[string]string{"sub": `["` + secret + `"]`}, "sub"},
		{"a string where a list is expected", map[string]string{"items": `"` + secret + `"`}, "items"},
		{"a list element of the wrong type", map[string]string{"list": `[1,"` + secret + `",3]`}, "list[1]"},
		{"the first list element of the wrong type", map[string]string{"list": `["` + secret + `"]`}, "list[0]"},
		{"a struct element that is not an object", map[string]string{"items": `[{"n":1},{"n":2},7]`}, "items[2]"},
		{"a field of a struct element of the wrong type", map[string]string{"items": `[{"n":1},{"n":2},{"n":"` + secret + `"}]`}, "items[2].n"},
		{"an element of a nested list", map[string]string{"grid": `[["a"],["b",31337]]`}, "grid[1][1]"},
		{"a nested list that is not a list", map[string]string{"grid": `[["a"],"` + secret + `"]`}, "grid[1]"},
		{"an element of a Go array", map[string]string{"pair": `[1,"` + secret + `"]`}, "pair[1]"},
		{"a field of a nested struct", map[string]string{"sub": `{"n":"` + secret + `"}`}, "sub.n"},
		{"a field two structs deep", map[string]string{"sub": `{"n":1,"deep":{"m":"` + secret + `"}}`}, "sub.deep.m"},
		{"a field of a map value", map[string]string{"by_name": `{"k":{"n":"` + secret + `"}}`}, "by_name.k.n"},
		{"a map value that is not an object", map[string]string{"by_name": `{"k":{"n":1},"j":[]}`}, "by_name.j"},
		{"a map value of the wrong scalar type", map[string]string{"labels": `{"k":31337}`}, "labels.k"},
		{"an element of a list in a map", map[string]string{"nest": `{"k":[{"n":1},{"n":"` + secret + `"}]}`}, "nest.k[1].n"},
		{"a fraction where an int is expected", map[string]string{"count": `1.5`}, "count"},
		{"an exponent where an int is expected", map[string]string{"count": `1e2`}, "count"},
		{"a fraction in a list of ints", map[string]string{"list": `[1,2.5]`}, "list[1]"},
		{"a number that overflows int8", map[string]string{"small": `300`}, "small"},
		{"a negative number for an unsigned int", map[string]string{"byte": `-1`}, "byte"},
		{"a number that overflows uint8", map[string]string{"byte": `256`}, "byte"},
		{"a number that overflows float32", map[string]string{"ratio": `1e50`}, "ratio"},
		{"a map key that is not an integer", map[string]string{"by_id": `{"` + secret + `":"v"}`}, "by_id." + secret},
		{"a map key that overflows its integer type", map[string]string{"by_id": `{"99999999999999999999":"v"}`}, "by_id.99999999999999999999"},
	}
	for _, tc := range tests {
		t.Run("Decode reports "+tc.name, func(t *testing.T) {
			in := shapesBase(tc.replace)
			var s shapes
			err := strictjson.Decode([]byte(in), &s)
			assertPath(t, in, err, tc.path)
		})
		t.Run("DecodeOpen reports "+tc.name, func(t *testing.T) {
			// An open object ignores members it has no field for; the
			// ones it has are checked like Decode does.
			in := strings.Replace(shapesBase(tc.replace), `{`, `{"future":{"x":[1]},`, 1)
			var s shapes
			err := strictjson.DecodeOpen([]byte(in), &s)
			assertPath(t, in, err, tc.path)
		})
	}
}

// assertPath checks that err is a *strictjson.Error at path whose message does
// not repeat the offending value.
func assertPath(t *testing.T, in string, err error, path string) {
	t.Helper()
	var se *strictjson.Error
	if !errors.As(err, &se) {
		t.Fatalf("decoding %s: got %v (%T), want a *strictjson.Error", in, err, err)
	}
	if se.Path != path {
		t.Fatalf("decoding %s: path %q (%v), want %q", in, se.Path, err, path)
	}
	if se.Msg == "" {
		t.Fatalf("decoding %s: the error at %q has no message", in, path)
	}
	// The path of a map key is the key itself, which the error may name; the
	// value of a member must not appear in the message.
	if strings.Contains(se.Msg, secret) || strings.Contains(se.Msg, "31337") {
		t.Fatalf("decoding %s: the message repeats the offending value: %q", in, se.Msg)
	}
}

func TestDecodeTypeErrorAtTheDocumentHasNoPath(t *testing.T) {
	for _, in := range []string{`[]`, `"x"`, `7`, `true`} {
		var s shapes
		err := strictjson.Decode([]byte(in), &s)
		assertPath(t, in, err, "")
	}
}

func TestDecodeTypeErrorOfARootThatIsNotAStruct(t *testing.T) {
	var list []inner
	assertPath(t, `[{"n":1},{"n":"x"}]`, strictjson.Decode([]byte(`[{"n":1},{"n":"x"}]`), &list), "[1].n")
	var byName map[string]int
	assertPath(t, `{"a":1,"b":"x"}`, strictjson.Decode([]byte(`{"a":1,"b":"x"}`), &byName), "b")
	var count int
	assertPath(t, `"x"`, strictjson.Decode([]byte(`"x"`), &count), "")
}

// A position that a type decodes itself keeps its own rules; strictjson does not
// second-guess what a value of such a type may look like.
func TestDecodeTypeCheckSkipsOpaquePositions(t *testing.T) {
	in := base + `,"raw":"anything","anything":[1,"two",{"three":3}],"level":"low"}`
	var d doc
	if err := strictjson.Decode([]byte(in), &d); err != nil {
		t.Fatal(err)
	}
}

// A field with the string option holds its number or bool in a JSON string, so
// the kind of the JSON value is not the kind of the Go field.
func TestDecodeTypeCheckAcceptsTheStringOption(t *testing.T) {
	var s shapes
	if err := strictjson.Decode([]byte(shapesBase(map[string]string{"quoted": `"42"`})), &s); err != nil {
		t.Fatal(err)
	}
	if s.Quoted != 42 {
		t.Fatalf("quoted = %d, want 42", s.Quoted)
	}
}

// typed has no position that decodes itself, so every input error the standard
// library reports as a type error is one that strictjson has to place.
type typed struct {
	S  string            `json:"s"`
	I  int               `json:"i"`
	I8 int8              `json:"i8"`
	U8 uint8             `json:"u8"`
	F  float32           `json:"f"`
	B  bool              `json:"b"`
	L  []int             `json:"l"`
	A  [2]string         `json:"a"`
	M  map[string]bool   `json:"m"`
	K  map[int8]string   `json:"k"`
	P  *typedChild       `json:"p"`
	C  []typedChild      `json:"c"`
	N  map[string][]bool `json:"n"`
}

type typedChild struct {
	X int    `json:"x"`
	Y string `json:"y,omitempty"`
}

func FuzzDecodeTypeErrorsHavePaths(f *testing.F) {
	ok := `{"s":"a","i":1,"i8":1,"u8":1,"f":1.5,"b":true,"l":[1],"a":["x","y"],"m":{"k":true},"k":{"1":"v"},"p":{"x":1},"c":[{"x":1}],"n":{"k":[true]}}`
	f.Add([]byte(ok))
	for _, s := range []string{
		`{"s":1,"i":1,"i8":1,"u8":1,"f":1.5,"b":true,"l":[1],"a":["x","y"],"m":{"k":true},"k":{"1":"v"},"p":{"x":1},"c":[{"x":1}],"n":{"k":[true]}}`,
		`{"s":"a","i":1.5,"i8":1,"u8":1,"f":1.5,"b":true,"l":[1],"a":["x","y"],"m":{"k":true},"k":{"1":"v"},"p":{"x":1},"c":[{"x":1}],"n":{"k":[true]}}`,
		`{"s":"a","i":1,"i8":300,"u8":1,"f":1.5,"b":true,"l":[1],"a":["x","y"],"m":{"k":true},"k":{"1":"v"},"p":{"x":1},"c":[{"x":1}],"n":{"k":[true]}}`,
		`{"s":"a","i":1,"i8":1,"u8":-1,"f":1.5,"b":true,"l":[1],"a":["x","y"],"m":{"k":true},"k":{"1":"v"},"p":{"x":1},"c":[{"x":1}],"n":{"k":[true]}}`,
		`{"s":"a","i":1,"i8":1,"u8":1,"f":1e99,"b":true,"l":[1],"a":["x","y"],"m":{"k":true},"k":{"1":"v"},"p":{"x":1},"c":[{"x":1}],"n":{"k":[true]}}`,
		`{"s":"a","i":1,"i8":1,"u8":1,"f":1.5,"b":1,"l":[1],"a":["x","y"],"m":{"k":true},"k":{"1":"v"},"p":{"x":1},"c":[{"x":1}],"n":{"k":[true]}}`,
		`{"s":"a","i":1,"i8":1,"u8":1,"f":1.5,"b":true,"l":[1,"x"],"a":["x","y"],"m":{"k":true},"k":{"1":"v"},"p":{"x":1},"c":[{"x":1}],"n":{"k":[true]}}`,
		`{"s":"a","i":1,"i8":1,"u8":1,"f":1.5,"b":true,"l":[1],"a":["x",2],"m":{"k":true},"k":{"1":"v"},"p":{"x":1},"c":[{"x":1}],"n":{"k":[true]}}`,
		`{"s":"a","i":1,"i8":1,"u8":1,"f":1.5,"b":true,"l":[1],"a":["x","y"],"m":{"k":"t"},"k":{"1":"v"},"p":{"x":1},"c":[{"x":1}],"n":{"k":[true]}}`,
		`{"s":"a","i":1,"i8":1,"u8":1,"f":1.5,"b":true,"l":[1],"a":["x","y"],"m":{"k":true},"k":{"z":"v"},"p":{"x":1},"c":[{"x":1}],"n":{"k":[true]}}`,
		`{"s":"a","i":1,"i8":1,"u8":1,"f":1.5,"b":true,"l":[1],"a":["x","y"],"m":{"k":true},"k":{"1":"v"},"p":{"x":"1"},"c":[{"x":1}],"n":{"k":[true]}}`,
		`{"s":"a","i":1,"i8":1,"u8":1,"f":1.5,"b":true,"l":[1],"a":["x","y"],"m":{"k":true},"k":{"1":"v"},"p":{"x":1},"c":[{"x":1},{"x":[]}],"n":{"k":[true]}}`,
		`{"s":"a","i":1,"i8":1,"u8":1,"f":1.5,"b":true,"l":[1],"a":["x","y"],"m":{"k":true},"k":{"1":"v"},"p":{"x":1},"c":[{"x":1}],"n":{"k":[true,0]}}`,
		`{"s":"a","i":1,"i8":1,"u8":1,"f":1.5,"b":true,"l":{},"a":["x","y"],"m":{"k":true},"k":{"1":"v"},"p":[],"c":[{"x":1}],"n":{"k":[true]}}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var d typed
		err := strictjson.Decode(data, &d)
		if err == nil {
			return
		}
		// The oracle is the standard library: where it reports a value of the
		// wrong type below the document, strictjson, which may stop at
		// another problem first, has a path for whatever it reports. Invalid
		// UTF-8 is a problem of the whole document that the standard library
		// repairs instead of reporting.
		if !utf8.Valid(data) {
			return
		}
		var std typed
		var ute *json.UnmarshalTypeError
		if e := json.Unmarshal(data, &std); !errors.As(e, &ute) || ute.Field == "" {
			return
		}
		var se *strictjson.Error
		if !errors.As(err, &se) || se.Path == "" {
			t.Fatalf("Decode(%q) = %v; the standard library places a type error at %q, strictjson must give a path", data, err, ute.Field)
		}
	})
}

type openList struct {
	Leaves []openLeaf `json:"leaves"`
}

// A type that decodes itself reports a refusal at the position of its value,
// however deep that is, because the standard library does not say where.
func TestDecodeRefusalOfATypeThatDecodesItselfHasPath(t *testing.T) {
	tests := []struct {
		name   string
		target func() any
		in     string
		path   string
	}{
		{"a text type that refuses its value", func() any { return new(doc) }, base + `,"level":"` + secret + `"}`, "level"},
		{"a text type given a number", func() any { return new(doc) }, base + `,"level":31337}`, "level"},
		{"a text type given an object", func() any { return new(doc) }, base + `,"level":{"k":"` + secret + `"}}`, "level"},
		{"a field of an open object", func() any { return new(openDoc) }, `{"name":"a","inner":{"n":"` + secret + `"}}`, "inner.n"},
		{"a field of an open object inside another", func() any { return new(openDoc) }, `{"name":"a","inner":{"n":1,"open":{"m":"` + secret + `"}}}`, "inner.open.m"},
		{"an unknown field below an open object", func() any { return new(openDoc) }, `{"name":"a","inner":{"n":1,"deep":{"n":1,"future":1}}}`, "inner.deep.future"},
		{"a missing field of an open object", func() any { return new(openDoc) }, `{"name":"a","inner":{"future":1}}`, "inner.n"},
		{"null for a field of an open object", func() any { return new(openDoc) }, `{"name":"a","inner":{"n":null}}`, "inner.n"},
		{"an open object that is not an object", func() any { return new(openDoc) }, `{"name":"a","inner":[]}`, "inner"},
		{"an element of a list of open objects", func() any { return new(openList) }, `{"leaves":[{"m":1},{"m":"` + secret + `"}]}`, "leaves[1].m"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertPath(t, tc.in, strictjson.Decode([]byte(tc.in), tc.target()), tc.path)
		})
	}
}
