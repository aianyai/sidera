package manifest

import (
	"encoding/json"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"sort"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// CheckValue checks one configuration value against the field: its kind, the
// options of an enum, the bounds of an integer, the limits of a string, a list
// and a map, and that a directory is an absolute path. The directory is not
// looked for: it may be made later, or removed, and whether it exists is for the
// adapter to report. A failure is a *Error whose Field is the place inside the
// value (empty for the value as a whole, "[2]" for the third item of a list,
// the entry key for an entry of a map).
func (f Field) CheckValue(raw json.RawMessage) error { return f.check(raw, true) }

// CheckValues checks the configuration values of one scope (an instance, or
// one adapter's section of a preset) against the fields of that scope: every
// key is a field, every value passes CheckValue, every required field is
// there. A failure is a *Error whose Field is the key, and the first by key
// order for a value and by field order for a missing one, so that the same
// input always gets the same answer. A scope with no fields takes no values.
func CheckValues(fields []Field, values map[string]json.RawMessage) error {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		i := slices.IndexFunc(fields, func(f Field) bool { return f.Key == k })
		if i < 0 {
			return fail(k, "no such setting")
		}
		if err := fields[i].CheckValue(values[k]); err != nil {
			return under(k, err)
		}
	}
	for _, f := range fields {
		if _, ok := values[f.Key]; f.Required && !ok {
			return fail(f.Key, "a required setting is missing")
		}
	}
	return nil
}

// check checks a value. A default is checked as a value is, so that a manifest
// cannot declare a default the field would refuse, except that it need not be
// an absolute directory (here is false): a manifest is the same on every
// platform, and a path that is absolute on one is not on another. Each kind says
// what it expects in its own error, with an empty path, the index of a list item
// or the key of a map entry.
func (f Field) check(raw json.RawMessage, here bool) error {
	switch f.Type {
	case FieldString, FieldDirectory, FieldEnum:
		var s string
		if err := decodeValue(raw, &s); err != nil {
			return fail("", "must be a string")
		}
		if err := length(s, 0, adapterproto.MaxConfigString); err != nil {
			return err
		}
		if f.Type == FieldEnum && !slices.Contains(f.Options, s) {
			return failBeyond("", "%q is not one of the options", s)
		}
		if f.Type == FieldDirectory && here && !filepath.IsAbs(s) {
			return fail("", "a directory is an absolute path")
		}
	case FieldInteger:
		var n int64
		if err := decodeValue(raw, &n); err != nil {
			return fail("", "must be a plain integer")
		}
		if n < -maxIntValue || n > maxIntValue {
			return fail("", "%d outside the exact integer range", n)
		}
		if f.Min != nil && n < *f.Min {
			return failBeyond("", "%d is below min %d", n, *f.Min)
		}
		if f.Max != nil && n > *f.Max {
			return failBeyond("", "%d is above max %d", n, *f.Max)
		}
	case FieldBoolean:
		var b bool
		if err := decodeValue(raw, &b); err != nil {
			return fail("", "must be true or false")
		}
	case FieldStringList:
		var items []json.RawMessage
		if err := decodeValue(raw, &items); err != nil || items == nil {
			return fail("", "must be a list of strings")
		}
		if len(items) > adapterproto.MaxConfigList {
			return fail("", "%d items, at most %d", len(items), adapterproto.MaxConfigList)
		}
		for i, it := range items {
			var s string
			if err := decodeValue(it, &s); err != nil {
				return fail(idx(i), "a list holds strings only")
			}
			if err := length(s, 0, adapterproto.MaxConfigString); err != nil {
				return under(idx(i), err)
			}
		}
	case FieldMap:
		var entries map[string]json.RawMessage
		if err := decodeValue(raw, &entries); err != nil || entries == nil {
			return fail("", "must be a map")
		}
		if len(entries) > adapterproto.MaxConfigMapEntries {
			return fail("", "%d entries, at most %d", len(entries), adapterproto.MaxConfigMapEntries)
		}
		if f.Value == nil {
			return fail("", "a map field describes its values")
		}
		entry := f.Value.field()
		for _, k := range slices.Sorted(maps.Keys(entries)) {
			if err := adapterproto.ValidateConfigMapKey(k); err != nil {
				return under("", err)
			}
			if err := entry.check(entries[k], here); err != nil {
				// A schema shows that an entry is a plain value. That it is of the
				// kind the value description names is a rule between two parts of
				// a manifest, which a schema cannot state.
				return beyondSchema(under(k, err))
			}
		}
	default:
		return fail("", "unknown field type %q", string(f.Type))
	}
	return nil
}

// beyondSchema marks the error of a check as a rule that JSON Schema cannot
// state. The error is one of this package's, as under returns.
func beyondSchema(err error) error {
	var me *Error
	if errors.As(err, &me) {
		me.Beyond = true
	}
	return err
}
