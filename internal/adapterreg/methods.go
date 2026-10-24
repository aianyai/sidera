package adapterreg

import (
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// Requirement says what a kind of endpoint expects of a method.
type Requirement uint8

const (
	// Forbidden: the daemon refuses the method for this kind with
	// adapterproto.CodeWrongEndpointKind.
	Forbidden Requirement = iota
	// Optional: the method may be used when its capability, if it has one, is
	// declared.
	Optional
	// Required: an adapter that declares the kind implements the method.
	Required
)

// String returns the name of the requirement.
func (r Requirement) String() string {
	switch r {
	case Forbidden:
		return "forbidden"
	case Optional:
		return "optional"
	case Required:
		return "required"
	}
	return "invalid"
}

// gate names the capability a method depends on.
type gate uint8

const (
	gateNone gate = iota
	gateAckOnBehalf
	gateServerRequests
)

type rule struct {
	runtime, session, direct Requirement
	gate                     gate
}

// requirements is the matrix of methods against endpoint kinds. The runtime and
// session columns are one column of the specification, except recv.observed,
// which concerns session endpoints only: a runtime endpoint receives no ordinary
// messages, so there is nothing on it to acknowledge.
var requirements = map[string]rule{
	adapterproto.MethodInitialize:          {Required, Required, Required, gateNone},
	adapterproto.MethodPing:                {Required, Required, Required, gateNone},
	adapterproto.MethodCancelRequest:       {Required, Required, Required, gateNone},
	adapterproto.MethodShutdown:            {Required, Required, Required, gateNone},
	adapterproto.MethodAdapterStart:        {Required, Required, Required, gateNone},
	adapterproto.MethodAdapterProbe:        {Required, Required, Required, gateNone},
	adapterproto.MethodAdapterAvailability: {Required, Required, Required, gateNone},

	adapterproto.MethodSessionCreate:   {Required, Required, Forbidden, gateNone},
	adapterproto.MethodPromptDeliver:   {Required, Required, Forbidden, gateNone},
	adapterproto.MethodPromptReconcile: {Required, Required, Forbidden, gateNone},
	adapterproto.MethodTurnStarted:     {Required, Required, Forbidden, gateNone},
	adapterproto.MethodTurnEnded:       {Required, Required, Forbidden, gateNone},
	adapterproto.MethodSessionClose:    {Required, Required, Forbidden, gateNone},
	// An adapter that ever answers prompt.deliver with accepted must send
	// prompt.persisted afterwards (or leave it to reconciliation).
	adapterproto.MethodPromptPersisted: {Required, Required, Forbidden, gateNone},

	adapterproto.MethodCheckpointPut: {Optional, Optional, Optional, gateNone},

	adapterproto.MethodSessionInterrupt:     {Optional, Optional, Forbidden, gateNone},
	adapterproto.MethodRecvObserved:         {Forbidden, Optional, Forbidden, gateAckOnBehalf},
	adapterproto.MethodNativeRequestHandled: {Required, Required, Forbidden, gateServerRequests},

	// The messages of a session endpoint must be read by the model itself; an
	// adapter reading them for it would swallow them.
	adapterproto.MethodEndpointRegister: {Forbidden, Forbidden, Required, gateNone},
	adapterproto.MethodEndpointRecv:     {Forbidden, Forbidden, Required, gateNone},
	adapterproto.MethodEndpointAck:      {Forbidden, Forbidden, Required, gateNone},
	adapterproto.MethodEndpointSend:     {Forbidden, Forbidden, Required, gateNone},
	adapterproto.MethodEndpointPending:  {Forbidden, Forbidden, Required, gateNone},

	adapterproto.MethodEndpointUnregister:    {Forbidden, Forbidden, Optional, gateNone},
	adapterproto.MethodEndpointMessageStatus: {Forbidden, Forbidden, Optional, gateNone},
}

func (r rule) of(k EndpointKind) Requirement {
	switch k {
	case KindRuntime:
		return r.runtime
	case KindSession:
		return r.session
	case KindDirect:
		return r.direct
	}
	return Forbidden
}

// RequirementOf says what an endpoint of the kind expects of the method. An
// unknown method or kind is Forbidden.
func RequirementOf(k EndpointKind, method string) Requirement {
	r, ok := requirements[method]
	if !ok {
		return Forbidden
	}
	return r.of(k)
}

// MethodAllowed reports whether the method may be used on an endpoint of the kind
// at all, whatever the adapter declared. The direction in which the method may be
// sent is a separate matter, which adapterproto.Methods records.
func MethodAllowed(k EndpointKind, method string) bool {
	return RequirementOf(k, method) != Forbidden
}

// Check decides whether a call of the method on an endpoint of the kind is
// acceptable from this adapter. It returns 0 when it is, and otherwise the error
// code the daemon answers with:
//
//   - peer.CodeMethodNotFound: no such method, or a method only the daemon may
//     call (the method does not exist in this direction);
//   - adapterproto.CodeWrongEndpointKind: the method is forbidden for the kind;
//   - adapterproto.CodeCapabilityNotDeclared: the adapter did not declare the kind,
//     or the capability the method needs (ack on behalf, server requests).
//
// Whether the daemon may send an adapter a method that needs a capability (an
// interrupt) is not judged here, since the daemon is the caller then: it is
// Signals and AcceptsSignal.
func (a Admission) Check(k EndpointKind, method string) int {
	r, ok := requirements[method]
	if !ok {
		return peer.CodeMethodNotFound
	}
	if info, ok := adapterproto.Lookup(method); !ok || !info.Direction.CalledBy(false) {
		return peer.CodeMethodNotFound
	}
	if r.of(k) == Forbidden {
		return adapterproto.CodeWrongEndpointKind
	}
	if !a.Declares(k) {
		return adapterproto.CodeCapabilityNotDeclared
	}
	if a.caps.Runtime != nil {
		switch r.gate {
		case gateAckOnBehalf:
			if !a.caps.Runtime.AckOnBehalf {
				return adapterproto.CodeCapabilityNotDeclared
			}
		case gateServerRequests:
			if !a.caps.Runtime.ServerRequests {
				return adapterproto.CodeCapabilityNotDeclared
			}
		}
	} else if r.gate != gateNone {
		return adapterproto.CodeCapabilityNotDeclared
	}
	return 0
}
