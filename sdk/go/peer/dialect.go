package peer

import "encoding/json"

// Dialect is the protocol convention the peer itself applies: which
// notification cancels a request, whether a cancelled request is answered, and
// the codes the peer sends on its own. The zero value is the adapter protocol's
// convention, so existing owners are unchanged.
type Dialect struct {
	// CancelMethod is the notification that cancels an inbound request and that
	// Call sends when its context ends. Empty means MethodCancelRequest.
	CancelMethod string
	// CancelTarget reads the id of the cancelled request from the params of the
	// cancel notification (nil when the notification has none); false means the
	// notification cancels nothing. Nil means CancelRequestParams, decoded
	// strictly.
	CancelTarget func(params json.RawMessage) (RequestID, bool)
	// CancelParams builds the params Call sends when its context ends. Nil means
	// CancelRequestParams.
	CancelParams func(id RequestID) any
	// SilentOnCancel: a request whose cancellation arrived before its answer was
	// queued is not answered at all, and its id is released. False keeps
	// "answered exactly once".
	SilentOnCancel bool
	// OverloadedCode and LineTooLongCode replace CodeOverloaded and
	// CodeLineTooLong in what the peer sends. Zero means those codes.
	OverloadedCode, LineTooLongCode int
}
