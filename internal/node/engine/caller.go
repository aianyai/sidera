package engine

import "github.com/aianyai/sidera/sdk/go/adapterproto"

// Caller says who is acting, so that a method can resolve it in the same loop
// command that does the work: between two commands a token can be revoked or an
// endpoint closed, and a method that was handed an already resolved endpoint
// would act for one that is gone. Exactly one constructor is used; the zero
// Caller is refused as unauthenticated, so a caller that forgets it fails
// closed.
type Caller struct {
	kind     callerKind
	token    adapterproto.Token
	instance string
	endpoint adapterproto.EndpointRef
}

type callerKind uint8

const (
	callerNone callerKind = iota
	callerToken
	callerAdapter
)

// ByToken is an agent on the Agent interface: the session endpoint that holds
// the token. A token of any other kind of endpoint is no token.
func ByToken(t adapterproto.Token) Caller { return Caller{kind: callerToken, token: t} }

// ByAdapter is an adapter acting for one of its own direct endpoints. The
// endpoint must belong to the instance, or the call is refused as an unknown
// endpoint.
func ByAdapter(instance string, ep adapterproto.EndpointRef) Caller {
	return Caller{kind: callerAdapter, instance: instance, endpoint: ep}
}
