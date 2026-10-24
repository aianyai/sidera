package peer

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"
)

// The rule for a request id, stated once. It is the schema's: an integer whose
// absolute value is at most MaxRequestNumber, or a string of 1 to
// MaxRequestStringLen characters. A fraction, an exponent and null are not ids,
// because "1", "1.0" and "1e0" are three spellings of one value and would defeat
// the in-flight check and cancellation.
const (
	// MaxRequestNumber is the largest absolute value of a numeric id: the
	// largest integer every JSON implementation represents exactly.
	MaxRequestNumber = 1<<53 - 1
	// MaxRequestStringLen bounds a string id, counted in characters as JSON
	// Schema counts them.
	MaxRequestStringLen = 128
)

// ErrInvalidRequestID is matched by errors.Is for every failure of the id rule.
var ErrInvalidRequestID = errors.New("invalid request id")

func invalidID(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequestID, fmt.Sprintf(format, args...))
}

// RequestID is the identifier of a request: an integer or a string, as in
// JSON-RPC. The zero value is unset and invalid. A RequestID that
// UnmarshalJSON accepted obeys the rule above; one built with NumberID or
// StringID is checked by Validate.
type RequestID struct {
	set   bool
	isStr bool
	str   string
	num   int64
}

// NumberID returns the numeric request id n.
func NumberID(n int64) RequestID { return RequestID{set: true, num: n} }

// StringID returns the string request id s.
func StringID(s string) RequestID { return RequestID{set: true, isStr: true, str: s} }

// IsString reports whether the id is a string.
func (r RequestID) IsString() bool { return r.isStr }

// Number returns the numeric value; it is 0 for a string id.
func (r RequestID) Number() int64 { return r.num }

// String returns the text of a string id, or the decimal of a number.
func (r RequestID) String() string {
	if r.isStr {
		return r.str
	}
	return strconv.FormatInt(r.num, 10)
}

// Validate checks the id is set and within the rule.
func (r RequestID) Validate() error {
	switch {
	case !r.set:
		return invalidID("missing")
	case r.isStr:
		if n := utf8.RuneCountInString(r.str); n < 1 || n > MaxRequestStringLen {
			return invalidID("string of %d characters, want 1..%d", n, MaxRequestStringLen)
		}
	case r.num < -MaxRequestNumber || r.num > MaxRequestNumber:
		return invalidID("%d outside ±%d", r.num, int64(MaxRequestNumber))
	}
	return nil
}

// key identifies the id regardless of how it was spelled on the wire. A string
// and a number never share a key.
func (r RequestID) key() string {
	if r.isStr {
		return "s" + r.str
	}
	return "n" + strconv.FormatInt(r.num, 10)
}

// MarshalJSON refuses an id that breaks the rule.
func (r RequestID) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if r.isStr {
		return marshalValue(r.str)
	}
	return strconv.AppendInt(nil, r.num, 10), nil
}

// UnmarshalJSON decodes a JSON string or integer and applies the rule. b is
// one valid JSON value, as encoding/json hands it over.
func (r *RequestID) UnmarshalJSON(b []byte) error {
	var id RequestID
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return invalidID("%v", err)
		}
		id = StringID(s)
	} else {
		n, err := strconv.ParseInt(string(b), 10, 64)
		if err != nil {
			return invalidID("neither a string nor an integer")
		}
		id = NumberID(n)
	}
	if err := id.Validate(); err != nil {
		return err
	}
	*r = id
	return nil
}

// CancelRequestParams are the params of MethodCancelRequest.
type CancelRequestParams struct {
	RequestID RequestID `json:"request_id"`
}

// Validate checks the id.
func (p CancelRequestParams) Validate() error {
	if err := p.RequestID.Validate(); err != nil {
		return fmt.Errorf("request_id: %w", err)
	}
	return nil
}
