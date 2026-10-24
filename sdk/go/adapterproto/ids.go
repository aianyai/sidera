package adapterproto

import (
	"encoding/base64"
	"regexp"
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

// Patterns of identifiers whose syntax the protocol itself fixes. The same
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

// EndpointRef names an endpoint of this node. The protocol treats it as an
// opaque string the daemon hands out and the adapter hands back unchanged; only
// the daemon checks its syntax.
type EndpointRef string

// Address names a recipient or sender. It is opaque to the SDK for the same
// reason as EndpointRef.
type Address string

// AttemptID numbers an attempt to create a session or to send a prompt: a random
// 128-bit value written as a lowercase UUID version 4. The first attempt of a
// session can be used as the user-message identifier a runtime wants.
type AttemptID string

// LeaseRef is the identifier of one page handed out by a recv: 32 lowercase
// hexadecimal characters, not all zero.
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
