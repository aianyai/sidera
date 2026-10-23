package peer

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Error codes. The first group is the JSON-RPC 2.0 reserved range; the second
// is the implementation-defined server range (-32000..-32099) that this layer
// uses. Codes from CodeApplicationMin to CodeApplicationMax are left to the
// protocol built on the peer.
const (
	CodeParseError     = -32700 // invalid JSON or invalid UTF-8; the connection continues
	CodeInvalidRequest = -32600 // not an object, a batch, missing "jsonrpc", bad or duplicate id
	CodeMethodNotFound = -32601 // unknown method, or a method that does not belong to this direction
	CodeInvalidParams  = -32602 // params violate the method's schema
	CodeInternalError  = -32603 // the handler failed; the result is unknown

	CodeOverloaded                 = -32001 // refused before queuing; no handler ran
	CodeShuttingDown               = -32002 // shutdown has begun
	CodeNotInitialized             = -32003 // received before initialization
	CodeLineTooLong                = -32004 // sent with a null id, then the connection is closed
	CodeUnsupportedProtocolVersion = -32022 // version negotiation failed
	CodeRequestCancelled           = -32800 // cancelled; the result is unknown unless the method says otherwise
)

// The application error range. Codes in it mean the request had no effect.
const (
	CodeApplicationMin = 1001
	CodeApplicationMax = 1099
)

// IsApplicationCode reports whether code lies in the application range.
func IsApplicationCode(code int) bool {
	return code >= CodeApplicationMin && code <= CodeApplicationMax
}

var codeNames = map[int]string{
	CodeParseError:                 "parse_error",
	CodeInvalidRequest:             "invalid_request",
	CodeMethodNotFound:             "method_not_found",
	CodeInvalidParams:              "invalid_params",
	CodeInternalError:              "internal_error",
	CodeOverloaded:                 "overloaded",
	CodeShuttingDown:               "shutting_down",
	CodeNotInitialized:             "not_initialized",
	CodeLineTooLong:                "line_too_long",
	CodeUnsupportedProtocolVersion: "unsupported_protocol_version",
	CodeRequestCancelled:           "request_cancelled",
}

// CodeName returns the name of a code defined by this package, or "" for any
// other code.
func CodeName(code int) string { return codeNames[code] }

// noEffectCode reports whether an error response with this code proves that
// the request produced no effect on the other side. Every other code
// (-32603, -32800 and anything unknown) leaves the result unknown.
func noEffectCode(code int) bool {
	switch code {
	case CodeParseError, CodeInvalidRequest, CodeMethodNotFound, CodeInvalidParams,
		CodeOverloaded, CodeShuttingDown, CodeNotInitialized, CodeLineTooLong,
		CodeUnsupportedProtocolVersion:
		return true
	}
	return IsApplicationCode(code)
}

// Error is a JSON-RPC error object. A handler returns one to answer with that
// code; Call returns the one the other side sent.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// NewError returns an error object without data.
func NewError(code int, message string) *Error {
	return &Error{Code: code, Message: message}
}

// WithData returns a copy of e carrying data marshaled to JSON. Data that
// cannot be marshaled is left out rather than failing the reply.
func (e *Error) WithData(data any) *Error {
	c := *e
	c.Data = nil
	if raw, err := marshalValue(data); err == nil {
		c.Data = raw
	}
	return &c
}

func (e *Error) Error() string {
	if name := CodeName(e.Code); name != "" {
		return fmt.Sprintf("peer: remote error %d (%s): %s", e.Code, name, clip(e.Message, 200))
	}
	return fmt.Sprintf("peer: remote error %d: %s", e.Code, clip(e.Message, 200))
}

// Is lets errors.Is(err, ErrOverloaded) match a remote -32001 as well as the
// local refusal, since in both cases nothing ran.
func (e *Error) Is(target error) bool {
	return target == ErrOverloaded && e.Code == CodeOverloaded
}

// Errors returned by Call and Notify.
var (
	// ErrClosed ends every outbound call that is pending when the peer closes,
	// and is returned by calls made after that. The result of such a call is
	// unknown. Err reports why the peer closed.
	ErrClosed = errors.New("peer: closed")

	// ErrOverloaded means the outbound queue was full. Nothing was written, so
	// the request had no effect and may be retried.
	ErrOverloaded = errors.New("peer: outbound queue full")

	// ErrNotSent marks failures that happened before anything was queued: a
	// done context, parameters that do not marshal, a message that is too
	// large. The request had no effect.
	ErrNotSent = errors.New("peer: request not sent")

	// ErrMessageTooLarge is returned when the encoded message would exceed
	// Params.MaxLineBytes, which the other side is assumed to enforce too.
	ErrMessageTooLarge = fmt.Errorf("%w: message exceeds the line limit", ErrNotSent)

	// ErrInvalidResponse is returned by Call when the other side answered with
	// a message that has the right id but is not a valid response. The result
	// is unknown.
	ErrInvalidResponse = errors.New("peer: malformed response")
)

// Reasons a peer ended, as reported by Err.
var (
	// ErrRemoteClosed: the other side closed its end at a message boundary.
	ErrRemoteClosed = errors.New("peer: remote closed the connection")

	// ErrLineTooLong: a message exceeded Params.MaxLineBytes. The peer sent
	// -32004 with a null id and closed the connection.
	ErrLineTooLong = errors.New("peer: line too long")

	// ErrNotificationOverflow: notifications arrived faster than they were
	// handled and the queue filled. Dropping one could leave the owner with a
	// wrong picture of the other side, so the connection is closed instead.
	ErrNotificationOverflow = errors.New("peer: notification queue overflow")

	// ErrWriteTimeout: a line was not written within Params.WriteTimeout and
	// the transport was closed.
	ErrWriteTimeout = errors.New("peer: write timeout")
)

// NoEffect reports whether err proves that the request had no effect on the
// other side, so repeating it is safe without reconciliation. That holds for a
// local refusal before anything was written (ErrOverloaded, ErrNotSent) and for
// the error codes -32700, -32600, -32601, -32602, -32001, -32002, -32003,
// -32004, -32022 and the application range.
//
// Every other failure leaves the outcome unknown: -32603, -32800, a cancelled
// or expired context after the request was queued, ErrClosed, a malformed
// response, and any code this package does not know. See ResultUnknown.
func NoEffect(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrOverloaded) || errors.Is(err, ErrNotSent) {
		return true
	}
	var re *Error
	if errors.As(err, &re) {
		return noEffectCode(re.Code)
	}
	return false
}

// ResultUnknown reports whether err leaves it unknown whether the request took
// effect. A caller of a method with side effects reconciles or retries
// idempotently after such an error. It is the complement of NoEffect for a
// non-nil err.
func ResultUnknown(err error) bool { return err != nil && !NoEffect(err) }

// clip shortens remote-controlled text before it reaches an error string or a
// log line.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
