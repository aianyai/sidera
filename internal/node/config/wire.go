package config

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// encodeJSON is the one encoding of every file: two-space indentation, keys in
// the order of the wire structs or sorted for maps, no escaping of HTML, one
// newline at the end. A file written by the node and a file read back from it
// are the same bytes, and an owner who edits one reads a layout they can follow.
func encodeJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// decodeStrict decodes a document with the strictness of strictjson and turns
// its error into a FieldError that says the document is malformed.
func decodeStrict(data []byte, v any) error {
	err := strictjson.Decode(data, v)
	if err == nil {
		return nil
	}
	var se *strictjson.Error
	if errors.As(err, &se) {
		path := se.Path
		if path == "" {
			path = typeErrorPath(data, v)
		}
		return &FieldError{Field: path, Malformed: true, Err: errors.New(se.Msg)}
	}
	return &FieldError{Malformed: true, Err: err}
}

// typeErrorPath finds the path of a value of the wrong type. strictjson reports
// such a value without a path. The standard library's decoder, run again on the
// same input, reports where the value is but not under which keys (its own
// field path leaves out the keys of maps), so the path is found by walking the
// document up to that offset.
func typeErrorPath(data []byte, v any) string {
	var ute *json.UnmarshalTypeError
	if err := json.Unmarshal(data, v); !errors.As(err, &ute) {
		return ""
	}
	return pathEndingAt(data, ute.Offset)
}

// pathEndingAt returns the JSON path of the value at offset, as the standard
// library reports it: the end of a scalar, or the position just after the
// opening bracket of an object or array. It returns "" when no value is there.
func pathEndingAt(data []byte, offset int64) string {
	type frame struct {
		object    bool
		key       string
		index     int
		expectKey bool
	}
	var stack []frame
	path := func() string {
		var b strings.Builder
		for _, f := range stack {
			if f.object {
				if b.Len() > 0 {
					b.WriteByte('.')
				}
				b.WriteString(f.key)
			} else {
				fmt.Fprintf(&b, "[%d]", f.index)
			}
		}
		return b.String()
	}
	// valueDone records that a value of the enclosing object or array is complete.
	valueDone := func() {
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].expectKey = true
		}
	}
	// valueStarts is called when a value begins; an array element has its index.
	valueStarts := func() {
		if n := len(stack); n > 0 && !stack[n-1].object {
			stack[n-1].index++
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				valueStarts()
				if dec.InputOffset() == offset {
					return path()
				}
				stack = append(stack, frame{object: t == '{', index: -1, expectKey: t == '{'})
			default:
				stack = stack[:len(stack)-1]
				valueDone()
			}
			continue
		case string:
			if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].expectKey {
				stack[n-1].key, stack[n-1].expectKey = t, false
				continue
			}
		}
		valueStarts()
		if dec.InputOffset() == offset {
			return path()
		}
		valueDone()
	}
}

func invalid(field string, err error) error { return &FieldError{Field: field, Err: err} }

func invalidf(field, format string, args ...any) error {
	return &FieldError{Field: field, Err: fmt.Errorf(format, args...)}
}

// under places a field error of a part of a document under the path of the
// part. Other errors pass through.
func under(prefix string, err error) error {
	var fe *FieldError
	if err == nil || !errors.As(err, &fe) {
		return err
	}
	field := prefix
	if fe.Field != "" {
		if field != "" {
			field += "."
		}
		field += fe.Field
	}
	return &FieldError{Field: field, Malformed: fe.Malformed, Err: fe.Err}
}

// fileErrorFor attaches a file to the error of decoding it.
func fileErrorFor(path string, kind FileKind, err error) error {
	var fe *FieldError
	if errors.As(err, &fe) {
		reason := FileInvalid
		if fe.Malformed {
			reason = FileMalformed
		}
		return &FileError{Path: path, Kind: kind, Reason: reason, Field: fe.Field, Err: fe.Err}
	}
	return &FileError{Path: path, Kind: kind, Reason: FileInvalid, Err: err}
}

// checkHeader checks the format of a file and, for a versioned one, its version.
func checkHeader(format int, version uint64, versioned bool) error {
	if format != FormatVersion {
		return invalidf("format", "format %d is not known (this node reads format %d)", format, FormatVersion)
	}
	if versioned && version == 0 {
		return invalidf("version", "versions start at 1")
	}
	return nil
}

// operationWire is last_operation.
type operationWire struct {
	ID          string          `json:"id"`
	Person      string          `json:"person"`
	Fingerprint string          `json:"fingerprint"`
	Result      json.RawMessage `json:"result"`
}

const localPerson = "local"

func (w *operationWire) operation() (*Operation, error) {
	var op Operation
	idBytes, err := decodeHex(w.ID, len(op.ID))
	if err != nil {
		return nil, invalid("last_operation.id", err)
	}
	copy(op.ID[:], idBytes)
	if op.ID.IsZero() {
		return nil, invalidf("last_operation.id", "an operation id is never zero")
	}
	if w.Person != localPerson {
		p, err := ident.ParsePersonID(w.Person)
		if err != nil {
			return nil, invalidf("last_operation.person", "%q is neither %q nor a person id", w.Person, localPerson)
		}
		op.Person = p
	}
	fp, err := decodeHex(w.Fingerprint, len(op.Fingerprint))
	if err != nil {
		return nil, invalid("last_operation.fingerprint", err)
	}
	copy(op.Fingerprint[:], fp)
	result, err := compactObject(w.Result)
	if err != nil {
		return nil, invalid("last_operation.result", err)
	}
	op.Result = result
	return &op, nil
}

func operationWireOf(op *Operation) (*operationWire, error) {
	if op == nil {
		return nil, nil
	}
	person := localPerson
	if !op.Person.IsZero() {
		person = op.Person.String()
	}
	result, err := compactObject(op.Result)
	if err != nil {
		return nil, fmt.Errorf("last operation result: %w", err)
	}
	return &operationWire{
		ID:          hex.EncodeToString(op.ID[:]),
		Person:      person,
		Fingerprint: hex.EncodeToString(op.Fingerprint[:]),
		Result:      result,
	}, nil
}

// decodeHex decodes exactly n bytes written as lowercase hexadecimal.
func decodeHex(s string, n int) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != n || hex.EncodeToString(b) != s {
		return nil, fmt.Errorf("%q is not %d bytes in lowercase hexadecimal", s, n)
	}
	return b, nil
}

// compactObject returns raw as compact JSON, which must be one object.
func compactObject(raw json.RawMessage) (json.RawMessage, error) {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil || b.Len() == 0 || b.Bytes()[0] != '{' {
		return nil, errors.New("must be a JSON object")
	}
	return b.Bytes(), nil
}

// compactValue returns raw as compact JSON that is not null.
func compactValue(raw json.RawMessage) (json.RawMessage, error) {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil || bytes.Equal(b.Bytes(), []byte("null")) {
		return nil, errors.New("must be a JSON value that is not null")
	}
	return b.Bytes(), nil
}

// checkValues checks and compacts the values of a map of configuration values:
// the keys have the syntax of a configuration key and no value is null. The
// manifest says which keys exist and what they may hold; that is checked when
// the adapter is started.
func checkValues(values map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if values == nil {
		return nil, nil
	}
	out := make(map[string]json.RawMessage, len(values))
	for _, k := range sortedKeys(values) {
		if err := adapterproto.ValidateConfigKey(k); err != nil {
			return nil, invalid(k, err)
		}
		v, err := compactValue(values[k])
		if err != nil {
			return nil, invalid(k, err)
		}
		out[k] = v
	}
	return out, nil
}

// checkName checks a name that becomes a file name or a lock name: it has the
// syntax of an adapter id.
func checkName(field, name string) error {
	if err := adapterproto.ValidateAdapterID(name); err != nil {
		return invalid(field, err)
	}
	return nil
}

// checkKey checks the key of a direct endpoint, which an adapter chooses.
func checkKey(field, key string) error {
	if n := utf8.RuneCountInString(key); n < 1 || n > adapterproto.MaxIdentLen {
		return invalidf(field, "a key has 1 to %d characters, this one has %d", adapterproto.MaxIdentLen, n)
	}
	return nil
}

// parseEndpoint parses an endpoint id of a file. The system endpoint is not a
// registered endpoint and is never written.
func parseEndpoint(field, s string) (ident.EndpointID, error) {
	id, err := ident.ParseEndpointID(s)
	if err != nil {
		return ident.EndpointID{}, invalid(field, err)
	}
	if id.IsSystem() {
		return ident.EndpointID{}, invalidf(field, "the system endpoint is not an endpoint of an instance")
	}
	return id, nil
}

// secondsToDuration saturates instead of overflowing, so that a number of
// seconds no duration holds is refused by the owner of the limit, not turned
// into a small one.
func secondsToDuration(s int64) time.Duration {
	switch {
	case s > math.MaxInt64/int64(time.Second):
		return time.Duration(math.MaxInt64)
	case s < math.MinInt64/int64(time.Second):
		return time.Duration(math.MinInt64)
	}
	return time.Duration(s) * time.Second
}

// durationSeconds is the whole seconds of d; a file holds no fractions.
func durationSeconds(field string, d time.Duration) (int64, error) {
	if d%time.Second != 0 {
		return 0, fmt.Errorf("%s: %v is not a whole number of seconds", field, d)
	}
	return int64(d / time.Second), nil
}

func versionOf(v uint64) admin.Version { return admin.Version(v) }
