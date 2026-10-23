package adapterproto

import (
	"encoding/json"
	"strings"

	"github.com/aianyai/sidera/sdk/go/internal/strictjson"
)

// Error codes. The codes from -32700 to -32600 are the JSON-RPC 2.0 ones; those
// from -32099 to -32000 are the server-defined range; the 1000s are the
// protocol's own.
//
// An error response means the request had no effect, except -32603 (the handler
// failed in an unknown state), -32800 (cancelled) and the absence of a response
// at all (timeout, closed connection), which leave the outcome unknown. Only for
// the methods with side effects does an unknown outcome call for reconciliation
// or an idempotent retry.
const (
	CodeParseError                 = -32700 // invalid JSON or invalid UTF-8; the connection continues
	CodeInvalidRequest             = -32600 // not an object, a batch, no "jsonrpc", bad or reused id
	CodeMethodNotFound             = -32601 // unknown method, or one not valid in this direction
	CodeInvalidParams              = -32602 // breaks the schema, or has an unknown field
	CodeInternalError              = -32603 // the handler failed; the outcome is unknown
	CodeOverloaded                 = -32001 // refused before queueing; the handler did not run
	CodeShuttingDown               = -32002 // shutdown has begun
	CodeNotInitialized             = -32003 // before initialize
	CodeLineTooLong                = -32004 // sent just before the connection is closed
	CodeUnsupportedProtocolVersion = -32022 // data: UnsupportedVersionData
	CodeRequestCancelled           = -32800 // cancelled; the outcome is unknown unless the method says otherwise

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

// ErrorCodes lists every code of the protocol.
func ErrorCodes() []ErrorCode {
	return []ErrorCode{
		{CodeParseError, "parse_error"},
		{CodeInvalidRequest, "invalid_request"},
		{CodeMethodNotFound, "method_not_found"},
		{CodeInvalidParams, "invalid_params"},
		{CodeInternalError, "internal_error"},
		{CodeOverloaded, "overloaded"},
		{CodeShuttingDown, "shutting_down"},
		{CodeNotInitialized, "not_initialized"},
		{CodeLineTooLong, "line_too_long"},
		{CodeUnsupportedProtocolVersion, "unsupported_protocol_version"},
		{CodeRequestCancelled, "request_cancelled"},
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

// KnownCode reports whether code is one of ErrorCodes.
func KnownCode(code int) bool {
	for _, c := range ErrorCodes() {
		if c.Code == code {
			return true
		}
	}
	return false
}

// EffectUnknown reports whether an error response with this code leaves it open
// whether the request had an effect. Every other code means it had none.
func EffectUnknown(code int) bool {
	return code == CodeInternalError || code == CodeRequestCancelled
}

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

// ErrorObject is the "error" member of an error response.
type ErrorObject struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	// Data is present exactly for the codes that define a data shape:
	// CodeUnsupportedProtocolVersion, CodeInvalidAddress and CodeSendRefused.
	Data json.RawMessage `json:"data,omitempty"`
}

// Validate checks the code, the message and the shape of the data.
func (e ErrorObject) Validate() error {
	var c checker
	c.ok(KnownCode(e.Code), "unknown error code %d", e.Code)
	c.field("message", strLen(e.Message, 1, MaxMessageLen))
	if c.err != nil {
		return c.err
	}
	var data interface{ Validate() error }
	switch e.Code {
	case CodeUnsupportedProtocolVersion:
		data = new(UnsupportedVersionData)
	case CodeInvalidAddress:
		data = new(InvalidAddressData)
	case CodeSendRefused:
		data = new(SendRefusedData)
	default:
		c.ok(e.Data == nil, "error %d carries no data", e.Code)
		return c.err
	}
	if e.Data == nil {
		return at("data", bad("error %d requires data", e.Code))
	}
	return at("data", decodeInto(e.Data, data))
}

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
	return ErrorObject{Code: CodeUnsupportedProtocolVersion, Message: "no common protocol version", Data: data}
}
