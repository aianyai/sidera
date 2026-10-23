// Package strictjson decodes JSON into Go values with the strictness the adapter
// protocol and the manifest require, which encoding/json alone does not give:
//
//   - Object keys match field names exactly. encoding/json matches them
//     case-insensitively, so {"PID": 7} would fill a field named "pid".
//   - A field without omitempty is required: a missing key is an error even
//     when the zero value would be legal.
//   - null is never a value. encoding/json treats null as "leave the zero
//     value", which would turn a missing or malformed field into a valid one.
//   - A field with omitempty and a non-pointer type is absent to mean "not
//     provided", so an explicit empty string, 0, empty list or empty object is
//     an error: it would be indistinguishable from absence after decoding. A
//     pointer field accepts any value of its type, including zero, and false is
//     always accepted for a bool.
//   - The input is one valid UTF-8 JSON value with nothing after it, and unknown
//     fields are errors.
//
// Positions typed json.RawMessage or any are opaque: they keep whatever JSON
// the sender wrote, null included, and the owner validates them. A type with its
// own UnmarshalJSON or UnmarshalText is opaque as well and is responsible for
// its own strictness (it may call Decode).
package strictjson

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

// Error is a strict-decoding failure at a JSON path.
type Error struct {
	// Path locates the offending value, for example "sessions[2].process.pid".
	// It is empty for the document itself.
	Path string
	Msg  string
}

func (e *Error) Error() string {
	if e.Path == "" {
		return "strictjson: " + e.Msg
	}
	return "strictjson: " + e.Path + ": " + e.Msg
}

// Decode decodes data, which must hold exactly one JSON value, into v, a non-nil
// pointer, under the rules in the package comment.
func Decode(data []byte, v any) error {
	rt := reflect.TypeOf(v)
	if rt == nil || rt.Kind() != reflect.Pointer || reflect.ValueOf(v).IsNil() {
		return errors.New("strictjson: Decode needs a non-nil pointer")
	}
	if !utf8.Valid(data) {
		return &Error{Msg: "input is not valid UTF-8"}
	}
	tree, err := parseTree(data)
	if err != nil {
		return err
	}
	if err := check(tree, rt.Elem(), ""); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &Error{Msg: err.Error()}
	}
	return nil
}

func parseTree(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, &Error{Msg: err.Error()}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, &Error{Msg: "data after the JSON value"}
	}
	return tree, nil
}

var (
	jsonUnmarshaler = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
	rawMessageType  = reflect.TypeFor[json.RawMessage]()
)

// opaque reports whether t keeps raw JSON or decodes itself.
func opaque(t reflect.Type) bool {
	if t == rawMessageType || t.Kind() == reflect.Interface {
		return true
	}
	pt := reflect.PointerTo(t)
	return pt.Implements(jsonUnmarshaler) || pt.Implements(textUnmarshaler)
}

// check walks the parsed document alongside the Go type that will receive it.
func check(tree any, t reflect.Type, path string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if opaque(t) {
		if tree == nil && t != rawMessageType && t.Kind() != reflect.Interface {
			return &Error{Path: path, Msg: "null is not a value"}
		}
		return nil
	}
	if tree == nil {
		return &Error{Path: path, Msg: "null is not a value"}
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := tree.(map[string]any)
		if !ok {
			return &Error{Path: path, Msg: "expected an object"}
		}
		return checkStruct(obj, t, path)
	case reflect.Slice:
		arr, ok := tree.([]any)
		if !ok {
			return &Error{Path: path, Msg: "expected an array"}
		}
		for i, e := range arr {
			if err := check(e, t.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		obj, ok := tree.(map[string]any)
		if !ok {
			return &Error{Path: path, Msg: "expected an object"}
		}
		for k, e := range obj {
			if err := check(e, t.Elem(), join(path, k)); err != nil {
				return err
			}
		}
	}
	return nil
}

type field struct {
	index    int
	name     string
	optional bool
	pointer  bool
}

func fields(t reflect.Type) []field {
	var out []field
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		tag, ok := sf.Tag.Lookup("json")
		if !sf.IsExported() || tag == "-" {
			continue
		}
		if !ok || sf.Anonymous {
			panic("strictjson: " + t.String() + "." + sf.Name + " needs a json tag and must not be embedded")
		}
		name, opts, _ := strings.Cut(tag, ",")
		f := field{index: i, name: name, pointer: sf.Type.Kind() == reflect.Pointer}
		for _, o := range strings.Split(opts, ",") {
			if o == "omitempty" {
				f.optional = true
			}
		}
		out = append(out, f)
	}
	return out
}

func checkStruct(obj map[string]any, t reflect.Type, path string) error {
	fs := fields(t)
	known := make(map[string]bool, len(fs))
	for _, f := range fs {
		known[f.name] = true
	}
	for k := range obj {
		if !known[k] {
			return &Error{Path: join(path, k), Msg: "unknown field"}
		}
	}
	for _, f := range fs {
		val, present := obj[f.name]
		p := join(path, f.name)
		if !present {
			if !f.optional {
				return &Error{Path: p, Msg: "required field is missing"}
			}
			continue
		}
		ft := t.Field(f.index).Type
		if f.optional && !f.pointer && explicitZero(val, ft) {
			return &Error{Path: p, Msg: "explicit empty value; omit the field instead"}
		}
		if err := check(val, ft, p); err != nil {
			return err
		}
	}
	return nil
}

// explicitZero reports whether val is the JSON spelling of the zero value of an
// omitempty field. false is allowed: it means the same as absence and callers
// write it naturally. Opaque positions are the owner's business.
func explicitZero(val any, t reflect.Type) bool {
	if opaque(t) {
		return false
	}
	switch v := val.(type) {
	case string:
		return v == ""
	case json.Number:
		s := v.String()
		return s == "0" || s == "-0"
	case []any:
		return len(v) == 0
	case map[string]any:
		return t.Kind() == reflect.Map && len(v) == 0
	}
	return false
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}
