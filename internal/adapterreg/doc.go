// Package adapterreg holds the daemon's decisions about adapters that need no I/O:
// whether an adapter's declaration is acceptable, which endpoint kinds and
// control signals it gets, which methods are valid for which kind of endpoint,
// and how what an adapter reports maps onto the contracts the delivery and
// handoff packages define.
//
// The messages themselves, the manifest and the version negotiation live in the
// SDK module (sdk/go), which the daemon shares with adapters. This package never
// frames, sends or stores anything; it takes values the protocol layer has
// decoded and returns verdicts with typed reasons.
//
// The package is pure: no I/O, no clock reads (callers pass the moments they
// observed), no global state.
package adapterreg

import (
	"fmt"

	"github.com/aianyai/sidera/internal/authz"
)

// EndpointKind says how an endpoint is read.
type EndpointKind uint8

// The kinds of endpoint an adapter can own. The zero value is not a kind.
const (
	// KindRuntime is an endpoint that creates sessions and takes no ordinary
	// messages. There is one per adapter that declares a runtime.
	KindRuntime EndpointKind = iota + 1
	// KindSession is an endpoint whose messages the native agent reads itself
	// with recv; the adapter only wakes it and may acknowledge for it. It exists
	// only through session creation, so it never exists without a runtime.
	KindSession
	// KindDirect is an endpoint the adapter reads itself, such as one a chat
	// bridge keeps for a person. The channel does not care whether a person or
	// an agent is on the other side.
	KindDirect
)

var kindNames = [...]string{KindRuntime: "runtime", KindSession: "session", KindDirect: "direct"}

// Kinds lists every endpoint kind in a stable order.
func Kinds() []EndpointKind { return []EndpointKind{KindRuntime, KindSession, KindDirect} }

// Valid reports whether k is one of the kinds.
func (k EndpointKind) Valid() bool { return k >= KindRuntime && k <= KindDirect }

// String returns the name of the kind.
func (k EndpointKind) String() string {
	if k.Valid() {
		return kindNames[k]
	}
	return fmt.Sprintf("invalid(%d)", uint8(k))
}

// TargetKind is the kind authorization uses for a request aimed at an endpoint
// of this kind.
func (k EndpointKind) TargetKind() authz.TargetKind {
	if k == KindRuntime {
		return authz.TargetRuntime
	}
	return authz.TargetEndpoint
}
