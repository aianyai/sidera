// Package strictjson decodes JSON into Go values with the strictness that wire
// messages, manifests and configuration files need, which encoding/json alone
// does not give. It is the one definition of these rules: every reader of such a
// document, in the SDK or outside it, decodes with this package instead of
// restating them. Decoding is strict in these ways:
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
// The rules about keys, required fields and null apply at any depth: inside
// nested structs, slices, Go arrays and map values alike.
//
// Positions typed json.RawMessage or any are opaque: they keep whatever JSON
// the sender wrote, null included, and the owner validates them. A type with its
// own UnmarshalJSON or UnmarshalText is opaque as well and is responsible for
// its own strictness (it may call Decode).
//
// What the caller must guarantee: every exported field of a struct that a
// document is decoded into carries a json tag (the tag "-" leaves it out) and is
// not embedded. Decode panics on a struct that breaks this, because the mistake
// is in the program and not in the input. A target that is not a non-nil
// pointer is also a programming error, reported as a plain error.
//
// A type that decodes itself through Decode or DecodeOpen has two more duties.
// When decoding fails, each such type in the document is offered its value once
// more, on a fresh instance and with the value re-encoded (members sorted,
// escapes normalised), to find which one refused; so its decoder must have no
// side effects and must decide only on the content of the value, never on its
// byte form, the order of its members or state of its own, or the path may name
// the wrong position or be missing. And the failure path costs 2^k decodes for k
// such types nested inside one another, so these types must not nest without
// bound on untrusted input.
//
// Every problem with the input is an *Error. Its Path names the offending value
// in nearly every case: a value of the wrong JSON type or one that does not fit
// its Go type, a refusal by a type that decodes itself, and every rule above.
// The messages strictjson produces say what was expected and never repeat the
// value found. Some messages are not its own and may contain the value: the
// refusal of a type that decodes itself carries that type's message, and a field
// tagged with the "string" option, or a json.Number given a string that does
// not spell a number, is checked by encoding/json alone and carries its message. A caller that must keep values
// secret does not show these.
//
// Path is empty when the document as a whole is at fault (input that is not
// valid UTF-8 or JSON, data after the value, a root of the wrong JSON type) and
// where strictjson cannot place a failure: a member whose key is the empty
// string, whose path element the syntax cannot show; a field with the "string"
// option; a json.Number given a string that does not spell a number; a refusal by a type that decodes itself
// that offering its value again does not reproduce, because the type decides on
// byte form, member order or state; and the refusal of a map key by a key type
// that decodes itself.
package strictjson

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Error is a strict-decoding failure at a JSON path.
type Error struct {
	// Path locates the offending value, for example "sessions[2].process.pid".
	// It is empty when the document as a whole is at fault.
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
	if err := check(tree, rt.Elem(), "", false); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	err = dec.Decode(v)
	if err == nil {
		return nil
	}
	// What check lets through can still fail where a type decodes itself. The
	// standard library does not say where, so the positions of such types are
	// tried one by one to find the one that refuses its value.
	if located := check(tree, rt.Elem(), "", true); located != nil {
		return located
	}
	return &Error{Msg: err.Error()}
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
	// Numbers stay text: only the keys matter here, and a number that does not
	// fit a float64 is valid JSON that the scan must not trip over.
	dec.UseNumber()
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
	numberType      = reflect.TypeFor[json.Number]()
)

// opaque reports whether t keeps raw JSON or decodes itself.
func opaque(t reflect.Type) bool {
	if t == rawMessageType || t.Kind() == reflect.Interface {
		return true
	}
	pt := reflect.PointerTo(t)
	return pt.Implements(jsonUnmarshaler) || pt.Implements(textUnmarshaler)
}

// check walks the parsed document alongside the Go type that will receive it
// and reports the first, in traversal order, value that cannot be stored in its
// position (object members are walked in sorted key order, so the same input
// always reports the same position): null, a
// value of another JSON type, or a number the Go type cannot hold. Doing this
// here, and not leaving it to encoding/json, is what gives these errors a path:
// the standard library locates a mismatch only by byte offset and names fields
// without the keys of maps and the indexes of lists. The messages say what was
// expected and never repeat the value found.
//
// With probe set, a value in a position that a type decodes itself is also
// offered to that type, which reports its refusal at the path of the value.
// Decode sets it only after decoding has failed, to find where; the offer is made
// on a copy of the value, so it can only name a position, never change what is
// accepted.
func check(tree any, t reflect.Type, path string, probe bool) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if opaque(t) {
		if tree == nil && t != rawMessageType && t.Kind() != reflect.Interface {
			return &Error{Path: path, Msg: "null is not a value"}
		}
		if probe && t != rawMessageType && t.Kind() != reflect.Interface {
			return probeOpaque(tree, t, path)
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
		return checkStruct(obj, t, path, probe)
	case reflect.Slice, reflect.Array:
		arr, ok := tree.([]any)
		if !ok {
			return &Error{Path: path, Msg: "expected an array"}
		}
		for i, e := range arr {
			if err := check(e, t.Elem(), fmt.Sprintf("%s[%d]", path, i), probe); err != nil {
				return err
			}
		}
	case reflect.Map:
		obj, ok := tree.(map[string]any)
		if !ok {
			return &Error{Path: path, Msg: "expected an object"}
		}
		for _, k := range sortedKeys(obj) {
			e := obj[k]
			if msg := keyProblem(k, t.Key()); msg != "" {
				return &Error{Path: join(path, k), Msg: msg}
			}
			if err := check(e, t.Elem(), join(path, k), probe); err != nil {
				return err
			}
		}
	default:
		if msg := scalarProblem(tree, t); msg != "" {
			return &Error{Path: path, Msg: msg}
		}
	}
	return nil
}

// probeOpaque offers the JSON value tree to a fresh value of type t, which
// decodes itself, and reports a refusal at path. A refusal that is itself an
// *Error, because the type decodes with this package, has a path of its own
// below the value.
func probeOpaque(tree any, t reflect.Type, path string) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(tree); err != nil {
		return nil
	}
	err := json.Unmarshal(b.Bytes(), reflect.New(t).Interface())
	if err == nil {
		return nil
	}
	var se *Error
	if errors.As(err, &se) {
		return &Error{Path: below(path, se.Path), Msg: se.Msg}
	}
	return &Error{Path: path, Msg: err.Error()}
}

// scalarProblem says why the JSON scalar tree cannot be stored in a Go value of
// type t, or returns "" when it can or when t is not a scalar kind.
func scalarProblem(tree any, t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		if t == numberType {
			// json.Number takes a number, and a string that spells one: the
			// standard library alone says which strings do, and its message
			// may contain the string.
			switch tree.(type) {
			case json.Number, string:
				return ""
			}
			return "expected a number"
		}
		if _, ok := tree.(string); !ok {
			return "expected a string"
		}
	case reflect.Bool:
		if _, ok := tree.(bool); !ok {
			return "expected a boolean"
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		n, ok := tree.(json.Number)
		if !ok {
			return "expected a number"
		}
		return numberProblem(n.String(), t)
	}
	return ""
}

// numberProblem says why the number written as s does not fit the numeric type
// t, which must be an integer or float kind, or returns "" when it does. It
// applies the standard library's own rules: an integer type takes no fraction
// or exponent, and a value must be in range.
func numberProblem(s string, t reflect.Type) string {
	var err error
	var expected string
	switch k := t.Kind(); {
	case k >= reflect.Int && k <= reflect.Int64:
		_, err = strconv.ParseInt(s, 10, t.Bits())
		expected = "expected an integer"
	case k >= reflect.Uint && k <= reflect.Uintptr:
		_, err = strconv.ParseUint(s, 10, t.Bits())
		expected = "expected a non-negative integer"
	default:
		_, err = strconv.ParseFloat(s, t.Bits())
	}
	switch {
	case err == nil:
		return ""
	case errors.Is(err, strconv.ErrRange):
		return "number out of range"
	default:
		return expected
	}
}

// keyProblem says why the object key k cannot be the key of a Go map whose key
// type is t, or returns "" when it can. Only integer keys are judged here: a
// string key always fits, a key type that decodes itself answers for its own
// keys, and any other key type is one encoding/json refuses to decode into, so
// it is left to refuse.
func keyProblem(k string, t reflect.Type) string {
	if opaque(t) {
		return ""
	}
	switch kind := t.Kind(); {
	case kind >= reflect.Int && kind <= reflect.Int64, kind >= reflect.Uint && kind <= reflect.Uintptr:
		if msg := numberProblem(k, t); msg != "" {
			return "map key: " + msg
		}
	}
	return ""
}

type field struct {
	index    int
	name     string
	optional bool
	pointer  bool
	quoted   bool // the "string" option: a scalar is written inside a JSON string
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
			switch o {
			case "omitempty":
				f.optional = true
			case "string":
				f.quoted = true
			}
		}
		out = append(out, f)
	}
	return out
}

func checkStruct(obj map[string]any, t reflect.Type, path string, probe bool) error {
	fs := fields(t)
	known := make(map[string]bool, len(fs))
	for _, f := range fs {
		known[f.name] = true
	}
	for _, k := range sortedKeys(obj) {
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
		if f.quoted && scalarKind(ft) {
			// encoding/json reads the scalar out of a string, so the JSON
			// type is not the kind of the field. Null is still no value.
			if val == nil {
				return &Error{Path: p, Msg: "null is not a value"}
			}
			continue
		}
		if err := check(val, ft, p, probe); err != nil {
			return err
		}
	}
	return nil
}

// scalarKind reports whether t, or what it points to, is a string, bool or
// number: the kinds to which the "string" option of a field applies.
func scalarKind(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct, reflect.Slice, reflect.Array, reflect.Map, reflect.Interface, reflect.Pointer:
		return false
	}
	return true
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

// sortedKeys is the keys of obj in ascending order, so that a walk of an object
// meets its members, and reports the first problem among them, in the same order
// every time.
func sortedKeys(obj map[string]any) []string {
	return slices.Sorted(maps.Keys(obj))
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// below is the path rel, which starts at the value at path, as a path from the
// document.
func below(path, rel string) string {
	switch {
	case rel == "":
		return path
	case path == "", strings.HasPrefix(rel, "["):
		return path + rel
	}
	return path + "." + rel
}
