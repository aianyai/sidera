package peer

import (
	"encoding/json"
	"fmt"

	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// Dialect is the protocol convention the peer itself applies: which
// notification cancels a request, whether a cancelled request is answered, and
// the document checks and codes the peer applies on its own. The zero value is
// the adapter protocol's convention, so existing owners are unchanged.
type Dialect struct {
	// RejectDuplicateKeysAndInvalidUnicode refuses duplicate member names and
	// unpaired UTF-16 surrogate escapes anywhere in an inbound frame, before
	// method lookup, overload or cancellation. Unknown members stay accepted.
	// A refused frame gets CodeInvalidRequest with its id only when that member
	// occurs exactly once and is valid. False checks duplicates only at the top
	// level and leaves nested values to the layer that interprets them.
	RejectDuplicateKeysAndInvalidUnicode bool
	// CancelMethod is the notification that cancels an inbound request and that
	// Call sends when its context ends. Empty means MethodCancelRequest.
	CancelMethod string
	// CancelTarget reads the id of the cancelled request from the params of the
	// cancel notification (nil when the notification has none); false means the
	// notification cancels nothing. Nil means CancelRequestParams, decoded
	// strictly. It runs on the read loop, on input from the other side: it must
	// be fast, must not block and must not panic, because the read loop does not
	// recover.
	CancelTarget func(params json.RawMessage) (RequestID, bool)
	// CancelParams builds the params Call sends when its context ends. They must
	// marshal to a JSON object, or be nil, which is sent as {}; New refuses a
	// function whose result does not. Nil means CancelRequestParams.
	CancelParams func(id RequestID) any
	// SilentOnCancel: a request whose cancellation arrived before its answer was
	// queued is not answered at all, and its id is released. False keeps
	// "answered exactly once".
	SilentOnCancel bool
	// OverloadedCode and LineTooLongCode replace CodeOverloaded and
	// CodeLineTooLong in what the peer sends. Zero means those codes.
	OverloadedCode, LineTooLongCode int
}

// resolve fills unset fields with the adapter protocol's convention and checks
// the rest: the cancel method must be a valid method name, and CancelParams
// must build a JSON object, so that a misconfigured dialect fails at New rather
// than silently losing every cancellation Call would send.
func (d Dialect) resolve() (Dialect, error) {
	if d.CancelMethod == "" {
		d.CancelMethod = MethodCancelRequest
	}
	if err := checkMethod(d.CancelMethod); err != nil {
		return Dialect{}, fmt.Errorf("peer: dialect cancel method: %w", err)
	}
	if d.CancelTarget == nil {
		d.CancelTarget = strictCancelTarget
	}
	if d.CancelParams == nil {
		d.CancelParams = func(id RequestID) any { return CancelRequestParams{RequestID: id} }
	}
	if _, err := marshalParams(d.CancelParams(NumberID(1))); err != nil {
		return Dialect{}, fmt.Errorf("peer: dialect cancel params: %w", err)
	}
	if d.OverloadedCode == 0 {
		d.OverloadedCode = CodeOverloaded
	}
	if d.LineTooLongCode == 0 {
		d.LineTooLongCode = CodeLineTooLong
	}
	return d, nil
}

// strictCancelTarget reads CancelRequestParams as strictly as any other
// params: an unknown or repeated member, a member in another case or an id that
// breaks the request id rule names nothing.
func strictCancelTarget(params json.RawMessage) (RequestID, bool) {
	var body CancelRequestParams
	if strictjson.Decode(params, &body) != nil || body.Validate() != nil {
		return RequestID{}, false
	}
	return body.RequestID, true
}
