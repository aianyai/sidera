package adapterproto

import (
	"encoding/json"
	"strings"

	"github.com/aianyai/sidera/sdk/go/peer"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// Application error codes: the protocol's own, in the range package peer sets
// aside for them (peer.CodeApplicationMin to peer.CodeApplicationMax). The
// JSON-RPC codes, the server-range codes the framing uses and the rule for which
// of them leave the outcome of a request unknown are package peer's.
const (
	CodeUnknownEndpoint       = 1001 // the endpoint does not exist or is not this connection's
	CodeWrongEndpointKind     = 1002 // the endpoint's kind cannot be used with this method
	CodeCapabilityNotDeclared = 1003 // the method needs a capability that was not declared
	CodeUnknownAttempt        = 1004 // the attempt identifier is unknown
	CodeAttemptConflict       = 1005 // the same attempt identifier with different parameters
	CodeCheckpointTooLarge    = 1006 // the checkpoint exceeds MaxCheckpointBytes
	CodeEndpointLimit         = 1007 // the direct-endpoint limit is reached
	CodeInvalidAddress        = 1008 // data: {field}
	CodeSendRefused           = 1009 // data: {reason}
	CodeLeaseUnknown          = 1010 // the lease does not belong to the endpoint
	CodeInstanceLocked        = 1011 // another process holds the instance lock
)

// ErrorCode pairs a code with its name.
type ErrorCode struct {
	Code int
	Name string
}

// ApplicationCodes lists every application code of the protocol.
func ApplicationCodes() []ErrorCode {
	return []ErrorCode{
		{CodeUnknownEndpoint, "unknown_endpoint"},
		{CodeWrongEndpointKind, "wrong_endpoint_kind"},
		{CodeCapabilityNotDeclared, "capability_not_declared"},
		{CodeUnknownAttempt, "unknown_attempt"},
		{CodeAttemptConflict, "attempt_conflict"},
		{CodeCheckpointTooLarge, "checkpoint_too_large"},
		{CodeEndpointLimit, "endpoint_limit"},
		{CodeInvalidAddress, "invalid_address"},
		{CodeSendRefused, "send_refused"},
		{CodeLeaseUnknown, "lease_unknown"},
		{CodeInstanceLocked, "instance_locked"},
	}
}

// KnownApplicationCode reports whether code is one of ApplicationCodes.
func KnownApplicationCode(code int) bool {
	for _, c := range ApplicationCodes() {
		if c.Code == code {
			return true
		}
	}
	return false
}

// knownCode reports whether code is one the protocol defines, whichever package
// owns its definition.
func knownCode(code int) bool { return peer.CodeName(code) != "" || KnownApplicationCode(code) }

// MaxMessageLen bounds the text of an error message.
const MaxMessageLen = 1024

// InvalidAddressData is the data of an invalid-address error: which field of the
// request held the bad address.
type InvalidAddressData struct {
	Field string `json:"field"`
}

// Validate checks the data.
func (d InvalidAddressData) Validate() error { return strLen(d.Field, 1, MaxIdentLen) }

// SendRefusedData is the data of a send-refused error: a short snake_case word
// for why the local intake refused the message, for example "too_many_recipients".
type SendRefusedData struct {
	Reason string `json:"reason"`
}

// Validate checks the data.
func (d SendRefusedData) Validate() error {
	if err := strLen(d.Reason, 1, 64); err != nil {
		return err
	}
	return matches(reasonWordRE, d.Reason)
}

// ErrorObject is the "error" member of an error response: the JSON-RPC error
// object of package peer, which owns its shape, with the protocol's rules for
// it. Data is required, in a defined shape, for the codes that define one:
// peer.CodeUnsupportedProtocolVersion, CodeInvalidAddress and CodeSendRefused.
// Any other code may carry data, which must be a JSON object: libraries attach
// details to -32602 and -32603 routinely, and a reader that refused them would
// turn a useful error into a protocol violation. Convert with (*peer.Error)(&e) to hand one to a peer.
type ErrorObject peer.Error

// Validate checks the code, the message and the shape of the data.
func (e ErrorObject) Validate() error {
	var c checker
	c.ok(knownCode(e.Code), "unknown error code %d", e.Code)
	c.field("message", strLen(e.Message, 1, MaxMessageLen))
	if c.err != nil {
		return c.err
	}
	var data interface{ Validate() error }
	switch e.Code {
	case peer.CodeUnsupportedProtocolVersion:
		data = new(UnsupportedVersionData)
	case CodeInvalidAddress:
		data = new(InvalidAddressData)
	case CodeSendRefused:
		data = new(SendRefusedData)
	default:
		if e.Data == nil {
			return nil
		}
		return at("data", decodeInto(e.Data, new(objectData)))
	}
	if e.Data == nil {
		return at("data", bad("error %d requires data", e.Code))
	}
	return at("data", decodeInto(e.Data, data))
}

// objectData is the data of a code that defines no shape for it: any JSON
// object.
type objectData map[string]json.RawMessage

// Validate accepts every object.
func (objectData) Validate() error { return nil }

// decodeInto strictly decodes raw into v, a pointer to a type with Validate, and
// validates it.
func decodeInto(raw []byte, v interface{ Validate() error }) error {
	if err := strictjson.Decode(raw, v); err != nil {
		return bad("%v", strings.TrimPrefix(err.Error(), "strictjson: "))
	}
	return v.Validate()
}

// NewUnsupportedVersionError builds the error response member for a failed
// negotiation.
func NewUnsupportedVersionError(e *NoCommonVersionError) ErrorObject {
	data, err := json.Marshal(e.Data)
	if err != nil {
		panic(err) // two ranges of integers always encode
	}
	return ErrorObject{Code: peer.CodeUnsupportedProtocolVersion, Message: "no common protocol version", Data: data}
}
