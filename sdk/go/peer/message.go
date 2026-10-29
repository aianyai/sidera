package peer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/aianyai/sidera/sdk/go/strictjson"
)

const jsonrpcVersion = "2.0"

// MethodCancelRequest is the notification that asks the other side to stop
// working on a request. Its params are {"request_id": <id>}. The read loop
// handles it directly, never through a queue. The cancelled request is still
// answered exactly once, with its normal result or with CodeRequestCancelled.
const MethodCancelRequest = "$/cancel_request"

type kind uint8

const (
	kindRequest kind = iota + 1
	kindNotification
	kindResponse
)

// message is one classified inbound line.
type message struct {
	kind kind

	// id is the id exactly as received. It is nil for notifications and the
	// literal null for a response that carries no id.
	id json.RawMessage
	// key identifies id regardless of spelling; empty when id is null or absent.
	key string

	method string          // requests and notifications
	params json.RawMessage // requests and notifications; nil when absent or null

	result json.RawMessage // successful responses
	err    *Error          // failed responses
	// malformed marks a response with a usable id but a body that is not a
	// valid result or error. The call it belongs to fails instead of waiting.
	malformed bool
}

// classifyError is a line that is not a valid message, together with the
// error response it earns. id is nil when no request id can be echoed safely.
type classifyError struct {
	id  json.RawMessage
	err *Error
}

func invalidRequest(id json.RawMessage, reason string) *classifyError {
	return &classifyError{id: id, err: NewError(CodeInvalidRequest, reason)}
}

// classify turns one line into exactly one message, or into the error
// response the line deserves. It never panics, whatever the bytes.
//
// Parameters are carried along for interpretation after the overload check.
// A dialect may first check the entire document for ambiguous JSON.
func (d Dialect) classify(line []byte) (message, *classifyError) {
	if !utf8.Valid(line) {
		return message{}, &classifyError{err: NewError(CodeParseError, "invalid UTF-8")}
	}
	if !json.Valid(line) {
		return message{}, &classifyError{err: NewError(CodeParseError, "invalid JSON")}
	}
	switch bytes.TrimLeft(line, " \t\r\n")[0] {
	case '{':
	case '[':
		return message{}, invalidRequest(nil, "batch requests are not supported")
	default:
		return message{}, invalidRequest(nil, "a message must be a JSON object")
	}
	// Exact key names: encoding/json struct decoding would also accept
	// "Method" or "JSONRPC". Raw values keep unknown members schema-free.
	var top map[string]json.RawMessage
	if d.RejectDuplicateKeysAndInvalidUnicode {
		if strictjson.Decode(line, &top) != nil {
			return message{}, invalidRequest(unambiguousRequestID(line), "duplicate member or invalid Unicode")
		}
	} else {
		// The adapter convention checks only the frame's own members. The
		// layer interpreting params and results is responsible for those values.
		if strictjson.NoDuplicateTopLevelKeys(line) != nil {
			return message{}, invalidRequest(nil, "a member name occurs twice")
		}
		if err := json.Unmarshal(line, &top); err != nil {
			return message{}, &classifyError{err: NewError(CodeParseError, "invalid JSON")}
		}
	}

	idRaw, hasID := top["id"]
	key, idOK := idKey(idRaw)
	methodRaw, hasMethod := top["method"]
	resultRaw, hasResult := top["result"]
	errRaw, hasErr := top["error"]

	// Only a message with a string "method" is recognisably a request, and only
	// a request is answered with its own id. Anything else that is malformed may
	// be meant as a response to a call of the other side; an error carrying its id
	// would land in that side's own call-id space: it would fail a call of
	// theirs that this side received and may have executed, with a code that
	// says "no effect", and so invite them to send it again. JSON-RPC sends no
	// reply to a response; the error for such a message carries a null id,
	// which is never the answer to a call.
	var replyID json.RawMessage
	if hasStringMethod(methodRaw) && idOK {
		replyID = idRaw
	}

	var version string
	if raw, ok := top["jsonrpc"]; !ok || json.Unmarshal(raw, &version) != nil || version != jsonrpcVersion {
		return message{}, invalidRequest(replyID, `"jsonrpc" must be the string "2.0"`)
	}

	if hasMethod {
		if hasResult || hasErr {
			return message{}, invalidRequest(replyID, `a message with "method" cannot carry "result" or "error"`)
		}
		var method string
		if json.Unmarshal(methodRaw, &method) != nil || method == "" {
			return message{}, invalidRequest(replyID, `"method" must be a non-empty string`)
		}
		params := top["params"]
		if bytes.Equal(params, []byte("null")) {
			params = nil
		}
		switch {
		case !hasID:
			return message{kind: kindNotification, method: method, params: params}, nil
		case !idOK:
			return message{}, invalidRequest(nil, `"id" must be a string or an integer`)
		}
		return message{kind: kindRequest, id: idRaw, key: key, method: method, params: params}, nil
	}

	// A response: no "method", but a "result" or an "error".
	if !hasResult && !hasErr {
		return message{}, invalidRequest(nil, `a message needs "method", "result" or "error"`)
	}
	if !hasID {
		return message{}, invalidRequest(nil, `a response must carry "id"`)
	}
	idNull := bytes.Equal(idRaw, []byte("null"))
	if !idOK && !idNull {
		return message{}, invalidRequest(nil, `"id" must be a string or an integer`)
	}
	m := message{kind: kindResponse, id: idRaw, key: key}
	switch {
	case hasResult && hasErr:
		m.malformed = true
	case hasResult:
		m.result = resultRaw
		if idNull {
			m.malformed = true
		}
	default:
		m.err = parseErrorObject(errRaw)
		m.malformed = m.err == nil
	}
	return m, nil
}

// unambiguousRequestID recovers the reply id of a syntactically valid object
// whose document checks failed. Only a unique string method identifies a request:
// a response's id belongs to the other side's call-id space. Walk every top-level
// member so an unrelated duplicate cannot hide a later duplicate id or method.
// Whole raw values keep nested members and large unknown numbers out of the check.
func unambiguousRequestID(line []byte) json.RawMessage {
	dec := json.NewDecoder(bytes.NewReader(line))
	if _, err := dec.Token(); err != nil {
		return nil
	}
	var id, method json.RawMessage
	for dec.More() {
		name, err := dec.Token()
		if err != nil {
			return nil
		}
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return nil
		}
		switch name {
		case "id":
			if id != nil {
				return nil
			}
			id = value
		case "method":
			if method != nil {
				return nil
			}
			method = value
		}
	}
	if !hasStringMethod(method) {
		return nil
	}
	// RequestID owns the id rule; strict decoding also prevents Go's tolerant
	// string decoder from replacing an invalid surrogate in an otherwise legal id.
	var parsed RequestID
	if strictjson.Decode(id, &parsed) != nil {
		return nil
	}
	return id
}

// hasStringMethod identifies a request even when its method name is invalid.
// Both callers have valid JSON, so an opening quote identifies a string value.
func hasStringMethod(method json.RawMessage) bool {
	return len(method) > 0 && method[0] == '"'
}

// parseErrorObject decodes an error member. It requires an integer "code" and
// tolerates a missing "message".
func parseErrorObject(raw json.RawMessage) *Error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil
	}
	var e Error
	if json.Unmarshal(fields["code"], &e.Code) != nil {
		return nil
	}
	if m, ok := fields["message"]; ok && json.Unmarshal(m, &e.Message) != nil {
		return nil
	}
	if d := fields["data"]; len(d) > 0 {
		e.Data = d
	}
	return &e
}

// idKey returns the canonical key of a request or response id and whether it
// is valid under the rule of RequestID. Null, fractions, exponents, objects and
// arrays are not valid ids, so "1" and "1.0" cannot be two spellings of
// different in-flight requests.
func idKey(raw json.RawMessage) (string, bool) {
	var id RequestID
	if len(raw) == 0 || json.Unmarshal(raw, &id) != nil {
		return "", false
	}
	return id.key(), true
}

// structured reports whether params may be handed to a handler: absent, an
// object or an array.
func structured(params json.RawMessage) bool {
	return len(params) == 0 || params[0] == '{' || params[0] == '['
}

// wire is the shape of every outbound message.
type wire struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// marshalValue encodes v as compact JSON. HTML escaping is off: it would only
// make lines longer.
func marshalValue(v any) (json.RawMessage, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// encodeLine encodes w as one frame, including the terminating newline.
func encodeLine(w wire) ([]byte, error) {
	w.JSONRPC = jsonrpcVersion
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(w); err != nil {
		return nil, err
	}
	line := b.Bytes()
	// The encoder never emits a raw newline inside a message; a violation
	// would corrupt the framing, so it is refused rather than sent.
	if bytes.IndexByte(line[:len(line)-1], '\n') >= 0 {
		return nil, errors.New("peer: encoded message contains a newline")
	}
	return line, nil
}

// The peer sends only frames that the message schema of the protocol accepts,
// so that every reader, in any language, takes what it sends.
const (
	// maxMethodLen bounds a method name sent, in characters as the schema counts.
	maxMethodLen = 128
	// maxErrorMessageLen bounds the text of an error sent, in characters.
	maxErrorMessageLen = 1024
)

var emptyObject = json.RawMessage("{}")

// isObject reports whether raw, one valid JSON value, is an object.
func isObject(raw []byte) bool { return len(raw) > 0 && raw[0] == '{' }

// checkMethod refuses a method name the schema would not accept.
func checkMethod(method string) error {
	if n := utf8.RuneCountInString(method); n < 1 || n > maxMethodLen {
		return fmt.Errorf("%w: method of %d characters, want 1 to %d", ErrNotSent, n, maxMethodLen)
	}
	return nil
}

// marshalParams encodes call parameters. The schema wants params to be an
// object, so nil and null are sent as {} and anything that is not an object is
// refused.
func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return emptyObject, nil
	}
	raw, err := marshalValue(params)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(raw, []byte("null")) {
		return emptyObject, nil
	}
	if !isObject(raw) {
		return nil, fmt.Errorf("params must be a JSON object")
	}
	return raw, nil
}

func requestLine(id RequestID, method string, params json.RawMessage) ([]byte, error) {
	rawID, err := id.MarshalJSON()
	if err != nil {
		return nil, err
	}
	return encodeLine(wire{ID: rawID, Method: method, Params: params})
}

func notificationLine(method string, params json.RawMessage) ([]byte, error) {
	return encodeLine(wire{Method: method, Params: params})
}

// resultLine encodes a success response. The schema wants the result to be an
// object; anything else is an error, which the caller answers with -32603.
func resultLine(id json.RawMessage, result any) ([]byte, error) {
	raw, err := marshalValue(result)
	if err != nil {
		return nil, err
	}
	if !isObject(raw) {
		return nil, errors.New("result is not a JSON object")
	}
	return encodeLine(wire{ID: id, Result: raw})
}

var nullID = json.RawMessage("null")

// errorLine encodes an error response. A nil id is sent as null. The error is
// first made to fit the schema (see conform).
func errorLine(id json.RawMessage, e *Error) []byte {
	if len(id) == 0 {
		id = nullID
	}
	e = conform(e)
	line, err := encodeLine(wire{ID: id, Error: e})
	if err != nil {
		// Only unmarshalable data can fail; the code and message always fit.
		line, _ = encodeLine(wire{ID: id, Error: NewError(e.Code, e.Message)})
	}
	return line
}

// conform returns an error object that the message schema accepts, whatever a
// handler built: a nil error becomes an internal error, an empty message gets
// the name of the code as its text, a long message is cut to the schema's limit
// and data that is not a JSON object is left out.
func conform(e *Error) *Error {
	if e == nil {
		return NewError(CodeInternalError, "internal error")
	}
	c := Error{Code: e.Code, Message: e.Message}
	if c.Message == "" {
		c.Message = CodeName(c.Code)
		if c.Message == "" {
			c.Message = "error"
		}
	}
	if utf8.RuneCountInString(c.Message) > maxErrorMessageLen {
		c.Message = string([]rune(c.Message)[:maxErrorMessageLen])
	}
	if isObject(e.Data) && json.Valid(e.Data) {
		c.Data = e.Data
	}
	return &c
}
