package adapterproto

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"unicode/utf8"
)

// ErrBeyondSchema is matched by errors.Is for a validation failure of a rule
// that JSON Schema cannot state, such as "min is not greater than max" or
// "the checkpoint fits in 16 KiB". Everything else Validate rejects, the schema
// rejects too; the conformance tests rely on the difference.
var ErrBeyondSchema = errors.New("rule beyond JSON Schema")

// ValidationError says which field of a message breaks which rule.
type ValidationError struct {
	// Field is the path of the field, for example "sessions[1].process.pid".
	Field string
	Msg   string
	// Beyond is true when the broken rule cannot be stated in JSON Schema.
	Beyond bool
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return "adapterproto: " + e.Msg
	}
	return "adapterproto: " + e.Field + ": " + e.Msg
}

// Is reports a match with ErrBeyondSchema for rules the schema cannot state.
func (e *ValidationError) Is(target error) bool { return target == ErrBeyondSchema && e.Beyond }

// fieldPrefix matches the "field: " that starts a message written for a rule
// about one field.
var fieldPrefix = regexp.MustCompile(`^([a-z_][a-z0-9_.\[\]]*): `)

// newError builds an error from a message that may start with "field: ".
func newError(msg string, beyond bool) *ValidationError {
	if m := fieldPrefix.FindStringSubmatch(msg); m != nil {
		return &ValidationError{Field: m[1], Msg: msg[len(m[0]):], Beyond: beyond}
	}
	return &ValidationError{Msg: msg, Beyond: beyond}
}

func bad(format string, args ...any) error { return newError(fmt.Sprintf(format, args...), false) }

func beyond(format string, args ...any) error { return newError(fmt.Sprintf(format, args...), true) }

// at places err, an error from validating a part, under the field name of that
// part in its parent.
func at(field string, err error) error {
	if err == nil {
		return nil
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		return &ValidationError{Field: field, Msg: err.Error()}
	}
	f := field
	if ve.Field != "" {
		sep := "."
		if ve.Field[0] == '[' {
			sep = ""
		}
		f = field + sep + ve.Field
	}
	return &ValidationError{Field: f, Msg: ve.Msg, Beyond: ve.Beyond}
}

// asValidation gives an error from the Validate method of a type owned by
// another package, which has no field path of its own, the shape of the rest. A
// message "field: reason" is split into its field and reason.
func asValidation(err error) error {
	var ve *ValidationError
	if err == nil || errors.As(err, &ve) {
		return err
	}
	return newError(err.Error(), false)
}

func index(i int) string { return fmt.Sprintf("[%d]", i) }

// checker collects the first failure of a Validate method.
type checker struct{ err error }

func (c *checker) field(name string, err error) {
	if c.err == nil {
		c.err = at(name, err)
	}
}

func (c *checker) ok(cond bool, format string, args ...any) {
	if !cond && c.err == nil {
		c.err = bad(format, args...)
	}
}

func (c *checker) beyond(cond bool, format string, args ...any) {
	if !cond && c.err == nil {
		c.err = beyond(format, args...)
	}
}

// list validates every element of a list under the field name.
func list[T interface{ Validate() error }](c *checker, name string, items []T) {
	for i, it := range items {
		c.field(name+index(i), it.Validate())
	}
}

// strLen checks a string length in characters, as JSON Schema counts them.
func strLen(s string, min, max int) error {
	n := utf8.RuneCountInString(s)
	if n < min || n > max {
		if min == 0 {
			return bad("length %d outside 0..%d", n, max)
		}
		return bad("length %d outside %d..%d", n, min, max)
	}
	return nil
}

func intRange[T ~int | ~int64](v T, min, max T) error {
	if v < min || v > max {
		return bad("%d outside %d..%d", v, min, max)
	}
	return nil
}

func matches(re *regexp.Regexp, s string) error {
	if !re.MatchString(s) {
		return bad("%q does not match %s", s, re.String())
	}
	return nil
}

// present checks a required list: nil means the field was missing.
func present[T any](items []T) error {
	if items == nil {
		return bad("required list is missing; use an empty list for none")
	}
	return nil
}

func unique[T comparable](items []T) error {
	seen := make(map[T]bool, len(items))
	for _, it := range items {
		if seen[it] {
			return bad("duplicate %v", it)
		}
		seen[it] = true
	}
	return nil
}

// duplicates returns the values that occur more than once.
func duplicates[T comparable](items []T) []T {
	seen := make(map[T]bool, len(items))
	var dup []T
	for _, it := range items {
		if seen[it] {
			dup = append(dup, it)
		}
		seen[it] = true
	}
	return dup
}

// enumOK checks that v is one of the values of its enum.
func enumOK[E ~string](v E, set []E) error {
	if v == "" {
		return bad("missing value")
	}
	if !slices.Contains(set, v) {
		return bad("unknown value %q", string(v))
	}
	return nil
}

// enumIn checks v against a subset of its enum, for results whose reason is
// restricted by the outcome.
func enumIn[E ~string](v E, subset ...E) error {
	if !slices.Contains(subset, v) {
		return bad("value %q not allowed here", string(v))
	}
	return nil
}

func strs[E ~string](set []E) []string {
	out := make([]string, len(set))
	for i, v := range set {
		out[i] = string(v)
	}
	return out
}
