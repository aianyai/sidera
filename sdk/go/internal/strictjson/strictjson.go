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
//     fields are errors, except in a value decoded with DecodeOpen.
//   - No object has the same key twice, at any depth. Parsers disagree about
//     which duplicate wins, and a message must mean one thing to every reader.
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
	if err := noDuplicateKeys(data); err != nil {
		return err
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

// DecodeOpen is Decode for an object that ignores the members it has no field
// for, which lets a newer sender add members that an older reader skips. It is
// meant for the UnmarshalJSON method of such a type, where Decode would
// otherwise be reached recursively: v is a pointer to a struct of a type
// without that method (a defined type with the same fields), and data holds one
// JSON object.
//
// Only the members of this object are open. The members that have a field, and
// everything under them, are decoded exactly as Decode does, and a key that
// occurs twice anywhere in data is an error, ignored members included, since
// two readers could otherwise disagree about which of the two they skipped.
func DecodeOpen(data []byte, v any) error {
	rt := reflect.TypeOf(v)
	if rt == nil || rt.Kind() != reflect.Pointer || reflect.ValueOf(v).IsNil() || rt.Elem().Kind() != reflect.Struct {
		return errors.New("strictjson: DecodeOpen needs a non-nil pointer to a struct")
	}
	if !utf8.Valid(data) {
		return &Error{Msg: "input is not valid UTF-8"}
	}
	if err := noDuplicateKeys(data); err != nil {
		return err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil || members == nil {
		return &Error{Msg: "expected an object"}
	}
	// A field that is missing stays missing for Decode to report.
	known := make(map[string]json.RawMessage, len(members))
	for _, f := range fields(rt.Elem()) {
		if raw, ok := members[f.name]; ok {
			known[f.name] = raw
		}
	}
	kept, err := json.Marshal(known)
	if err != nil {
		return &Error{Msg: err.Error()}
	}
	return Decode(kept, v)
}

// noDuplicateKeys reports the first object that has a key twice, at any depth.
func noDuplicateKeys(data []byte) error { return scanKeys(data, true) }

// NoDuplicateTopLevelKeys reports an error when the object that data holds has a
// key twice. Objects nested inside it are not looked at, for a caller that
// interprets only the outer object and leaves the rest to a layer that decodes
// it with Decode. data must be one valid JSON object.
func NoDuplicateTopLevelKeys(data []byte) error { return scanKeys(data, false) }

// scanKeys walks data and reports the first object with a repeated key; with
// deep unset it looks at the keys of the outermost object only.
func scanKeys(data []byte, deep bool) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	token := func() (json.Token, error) {
		tok, err := dec.Token()
		if err != nil {
			return nil, &Error{Msg: err.Error()}
		}
		return tok, nil
	}
	// skip consumes the value whose first token, tok, has been read.
	skip := func(tok json.Token) error {
		if _, ok := tok.(json.Delim); !ok {
			return nil
		}
		for depth := 1; depth > 0; {
			tok, err := token()
			if err != nil {
				return err
			}
			if d, ok := tok.(json.Delim); ok {
				if d == '{' || d == '[' {
					depth++
				} else {
					depth--
				}
			}
		}
		return nil
	}
	var scan func(path string, outermost bool) error
	scan = func(path string, outermost bool) error {
		tok, err := token()
		if err != nil {
			return err
		}
		d, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		if !deep && !outermost {
			return skip(tok)
		}
		switch d {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				kt, err := token()
				if err != nil {
					return err
				}
				k := kt.(string)
				if seen[k] {
					return &Error{Path: join(path, k), Msg: "duplicate key"}
				}
				seen[k] = true
				if err := scan(join(path, k), false); err != nil {
					return err
				}
			}
		case '[':
			for i := 0; dec.More(); i++ {
				if err := scan(fmt.Sprintf("%s[%d]", path, i), false); err != nil {
					return err
				}
			}
		}
		_, err = token() // the closing delimiter
		return err
	}
	return scan("", true)
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
