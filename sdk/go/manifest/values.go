package manifest

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"sort"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// CheckValue checks one configuration value against the field: its kind, the
// options of an enum, the bounds of an integer, the limits of a string and a
// list, and that a directory is an absolute path. The directory is not looked
// for: it may be made later, or removed, and whether it exists is for the
// adapter to report. A failure is a *Error whose Field is the place inside the
// value (empty for the value as a whole, "[2]" for the third item of a list).
func (f Field) CheckValue(raw json.RawMessage) error {
	if err := f.checkKind(raw); err != nil {
		return err
	}
	if f.Type == FieldDirectory {
		var s string
		// checkKind has shown that it is a string.
		_ = json.Unmarshal(raw, &s)
		if !filepath.IsAbs(s) {
			return fail("", "a directory is an absolute path")
		}
	}
	return nil
}

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

// checkKind checks everything about a value that does not depend on the
// machine it is checked on. It is the rule for a default as well as for a
// value, so that a manifest cannot declare a default the field would refuse.
// Each kind says what it expects in its own error, with an empty path or the
// index of a list item.
func (f Field) checkKind(raw json.RawMessage) error {
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
	default:
		return fail("", "unknown field type %q", string(f.Type))
	}
	return nil
}
