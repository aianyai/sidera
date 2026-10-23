package adapterproto

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strconv"
)

// Limits on identifier-like strings. The schema states the same numbers.
const (
	// MaxIdentLen bounds an endpoint reference, an address and the other
	// identifiers the daemon or the adapter picks.
	MaxIdentLen = 128
	// MaxRefLen bounds the references that only the adapter interprets: native
	// session references and turn references.
	MaxRefLen = 256
	// MaxDetailLen bounds free-text detail fields.
	MaxDetailLen = 1024
)

// Patterns of identifiers whose text form the protocol itself fixes. The same
// expressions appear in the schema, which a test compares.
const (
	attemptIDPattern = `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`
	leaseRefPattern  = `^[0-9a-f]{32}$`
	// tokenPattern is 32 bytes in unpadded base64url: 42 full characters and a
	// last one that carries 4 bits, so its low two bits are zero.
	tokenPattern = `^[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$`
)

var (
	attemptIDRE = regexp.MustCompile(attemptIDPattern)
	leaseRefRE  = regexp.MustCompile(leaseRefPattern)
	tokenRE     = regexp.MustCompile(tokenPattern)
)

const zeroLeaseRef = "00000000000000000000000000000000"

// EndpointRef names an endpoint of this node. It is the text form of an endpoint
// identifier, but the protocol treats it as an opaque string the daemon hands
// out and the adapter hands back unchanged; only the daemon checks its syntax.
type EndpointRef string

// Address is the text form of a recipient or sender, "<node>/<endpoint>". It is
// opaque to the SDK for the same reason as EndpointRef.
type Address string

// AttemptID numbers an attempt to create a session or to send a prompt: a random
// 128-bit value written as a lowercase UUID version 4. The first attempt of a
// session can be used as the user-message identifier a runtime wants.
type AttemptID string

// LeaseRef is the identifier of one page handed out by a recv: the lowercase
// hexadecimal text of a delivery lease identifier.
type LeaseRef string

// Token is an endpoint or probe token: 32 random bytes in unpadded base64url.
type Token string

// NativeRef is the adapter's own reference to a native session, for example a
// runtime session identifier. Only the adapter interprets it.
type NativeRef string

// TurnRef is the adapter's reference to one turn. Only the adapter interprets it.
type TurnRef string

// MessageID identifies a message in the channel; opaque.
type MessageID string

func (e EndpointRef) Validate() error { return strLen(string(e), 1, MaxIdentLen) }
func (a Address) Validate() error     { return strLen(string(a), 1, MaxIdentLen) }
func (n NativeRef) Validate() error   { return strLen(string(n), 1, MaxRefLen) }
func (t TurnRef) Validate() error     { return strLen(string(t), 1, MaxRefLen) }
func (m MessageID) Validate() error   { return strLen(string(m), 1, MaxIdentLen) }

func (a AttemptID) Validate() error { return matches(attemptIDRE, string(a)) }

func (l LeaseRef) Validate() error {
	if err := matches(leaseRefRE, string(l)); err != nil {
		return err
	}
	if l == zeroLeaseRef {
		return bad("the all-zero lease names no lease")
	}
	return nil
}

func (t Token) Validate() error {
	if err := matches(tokenRE, string(t)); err != nil {
		return err
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(string(t))
	if err != nil || len(b) != 32 {
		return bad("token is not 32 bytes of unpadded base64url")
	}
	return nil
}

// RequestID is the identifier of a request: a string or an integer, as in
// JSON-RPC. The zero value is unset and invalid.
type RequestID struct {
	set   bool
	isStr bool
	str   string
	num   int64
}

// MaxRequestNumber is the largest absolute value of a numeric request identifier,
// the largest integer every JSON implementation represents exactly.
const MaxRequestNumber = 1<<53 - 1

// NumberID returns the numeric request identifier n.
func NumberID(n int64) RequestID { return RequestID{set: true, num: n} }

// StringID returns the string request identifier s.
func StringID(s string) RequestID { return RequestID{set: true, isStr: true, str: s} }

// IsString reports whether the identifier is a string.
func (r RequestID) IsString() bool { return r.isStr }

// Number returns the numeric value; it is 0 for a string identifier.
func (r RequestID) Number() int64 { return r.num }

// String returns the text of a string identifier, or the decimal of a number.
func (r RequestID) String() string {
	if r.isStr {
		return r.str
	}
	return strconv.FormatInt(r.num, 10)
}

// Validate checks the identifier is set and within bounds.
func (r RequestID) Validate() error {
	switch {
	case !r.set:
		return bad("request id is missing")
	case r.isStr:
		return strLen(r.str, 1, MaxIdentLen)
	}
	return intRange(r.num, -MaxRequestNumber, MaxRequestNumber)
}

func (r RequestID) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if r.isStr {
		return json.Marshal(r.str)
	}
	return strconv.AppendInt(nil, r.num, 10), nil
}

func (r *RequestID) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return bad("request id: %v", err)
		}
		*r = StringID(s)
		return nil
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return bad("request id is neither a string nor a plain integer")
	}
	*r = NumberID(n)
	return nil
}
