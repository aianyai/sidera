package envelope

import (
	"fmt"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Kind says what an envelope asks for: a plain message or one of the control
// signals.
type Kind uint8

const (
	// KindInvalid is the zero value; it is never a valid kind.
	KindInvalid Kind = iota
	// KindMessage is a message for an endpoint.
	KindMessage
	// KindSessionCreate is control.session.create, addressed to a runtime
	// endpoint. Its body, if any, is the initial message of the new session.
	KindSessionCreate
	// KindSessionClose is control.session.close, addressed to a session endpoint.
	KindSessionClose
	// KindSessionInterrupt is control.session.interrupt, addressed to a session
	// endpoint.
	KindSessionInterrupt
	// KindEndpointStatus is control.endpoint.status, addressed to any endpoint.
	KindEndpointStatus
)

// Wire values of the kinds. An independent table, so that reordering the Go
// constants never changes what an envelope on the wire means.
const (
	wireKindMessage          = 1
	wireKindSessionCreate    = 2
	wireKindSessionClose     = 3
	wireKindSessionInterrupt = 4
	wireKindEndpointStatus   = 5
)

var kindNames = map[Kind]string{
	KindMessage:          "message",
	KindSessionCreate:    "control.session.create",
	KindSessionClose:     "control.session.close",
	KindSessionInterrupt: "control.session.interrupt",
	KindEndpointStatus:   "control.endpoint.status",
}

// String returns the name of the kind as the protocol writes it.
func (k Kind) String() string {
	if n, ok := kindNames[k]; ok {
		return n
	}
	return fmt.Sprintf("invalid(%d)", uint8(k))
}

// Valid reports whether k is one of the five kinds.
func (k Kind) Valid() bool { _, ok := kindNames[k]; return ok }

// IsControl reports whether k is a control signal.
func (k Kind) IsControl() bool { return k.Valid() && k != KindMessage }

func (k Kind) toWire() (uint64, error) {
	switch k {
	case KindMessage:
		return wireKindMessage, nil
	case KindSessionCreate:
		return wireKindSessionCreate, nil
	case KindSessionClose:
		return wireKindSessionClose, nil
	case KindSessionInterrupt:
		return wireKindSessionInterrupt, nil
	case KindEndpointStatus:
		return wireKindEndpointStatus, nil
	}
	return 0, fmt.Errorf("%w: %d", ErrKind, uint8(k))
}

func kindFromWire(v uint64) (Kind, error) {
	switch v {
	case wireKindMessage:
		return KindMessage, nil
	case wireKindSessionCreate:
		return KindSessionCreate, nil
	case wireKindSessionClose:
		return KindSessionClose, nil
	case wireKindSessionInterrupt:
		return KindSessionInterrupt, nil
	case wireKindEndpointStatus:
		return KindEndpointStatus, nil
	}
	return KindInvalid, fmt.Errorf("%w: wire value %d", ErrKind, v)
}

// Action returns the action the policy judges an envelope of this kind as, and
// whether k has one. It is the one mapping between what an envelope asks for
// and what authz is asked to decide; callers never map kinds to actions
// themselves.
func (k Kind) Action() (authz.Action, bool) {
	switch k {
	case KindMessage:
		return authz.ActionMessageSend, true
	case KindSessionCreate:
		return authz.ActionSessionCreate, true
	case KindSessionClose:
		return authz.ActionSessionClose, true
	case KindSessionInterrupt:
		return authz.ActionSessionInterrupt, true
	case KindEndpointStatus:
		return authz.ActionEndpointStatus, true
	}
	return 0, false
}

// Signal returns the control signal of the adapter protocol that k stands for,
// and whether k is a control signal.
func (k Kind) Signal() (adapterproto.ControlSignal, bool) {
	switch k {
	case KindSessionCreate:
		return adapterproto.SignalSessionCreate, true
	case KindSessionClose:
		return adapterproto.SignalSessionClose, true
	case KindSessionInterrupt:
		return adapterproto.SignalSessionInterrupt, true
	case KindEndpointStatus:
		return adapterproto.SignalEndpointStatus, true
	}
	return "", false
}

// KindOfSignal returns the kind of the envelope that carries a control signal.
func KindOfSignal(s adapterproto.ControlSignal) (Kind, bool) {
	for k := range kindNames {
		if sig, ok := k.Signal(); ok && sig == s {
			return k, true
		}
	}
	return KindInvalid, false
}
