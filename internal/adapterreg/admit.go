package adapterreg

import (
	"errors"
	"fmt"
	"slices"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// AdmitReason says why an adapter was not admitted.
type AdmitReason uint8

const (
	// ReasonCapabilitiesInvalid: the capabilities break a rule of the protocol
	// (an out-of-range endpoint limit, an unknown or missing front end, ...).
	ReasonCapabilitiesInvalid AdmitReason = iota + 1
	// ReasonManifestInvalid: the manifest breaks a structural rule.
	ReasonManifestInvalid
	// ReasonNoEndpointKinds: the adapter declares neither runtime nor direct
	// endpoints, so it could never own an endpoint.
	ReasonNoEndpointKinds
	// ReasonBudgetFrontendUndeclared: the manifest gives a budget for a front end
	// the adapter does not declare: cli or mcp without a runtime that lists it,
	// or direct without direct endpoints.
	ReasonBudgetFrontendUndeclared
	// ReasonMarkersInvalid: an acknowledgement marker is empty, does not compile
	// or matches the empty text.
	ReasonMarkersInvalid
)

var admitReasonNames = [...]string{
	ReasonCapabilitiesInvalid: "capabilities_invalid", ReasonManifestInvalid: "manifest_invalid", ReasonNoEndpointKinds: "no_endpoint_kinds",
	ReasonBudgetFrontendUndeclared: "budget_frontend_undeclared", ReasonMarkersInvalid: "markers_invalid",
}

// String returns the name of the reason.
func (r AdmitReason) String() string {
	if r >= ReasonCapabilitiesInvalid && int(r) < len(admitReasonNames) {
		return admitReasonNames[r]
	}
	return fmt.Sprintf("invalid(%d)", uint8(r))
}

// AdmitError is the error Admit returns: a typed reason and the underlying error.
type AdmitError struct {
	Reason AdmitReason
	Err    error
}

func (e *AdmitError) Error() string {
	if e.Err == nil {
		return "adapterreg: not admitted: " + e.Reason.String()
	}
	return "adapterreg: not admitted: " + e.Reason.String() + ": " + e.Err.Error()
}

func (e *AdmitError) Unwrap() error { return e.Err }

// Admission is what the daemon records for an admitted adapter on one connection:
// the endpoint kinds it may own, the control signals each kind accepts, the
// output budgets and the compiled acknowledgement markers. The zero value
// declares nothing and carries the default budget.
type Admission struct {
	caps    adapterproto.Capabilities
	budgets map[manifest.Frontend]handoff.Budget
	markers handoff.MarkerSet
}

// Admit decides whether an adapter that declared caps in initialize and ships
// manifest m may be admitted, and records what it may do.
//
// The capabilities and the manifest are validated again here, so a value built in
// code is judged like one read from the wire. An adapter must declare at least
// one of runtime and direct endpoints; every front end the manifest gives a budget
// for must be declared; and the acknowledgement markers are compiled here, once,
// by handoff, which is the only place that judges them.
func Admit(caps adapterproto.Capabilities, m manifest.Manifest) (Admission, error) {
	if err := caps.Validate(); err != nil {
		return Admission{}, &AdmitError{ReasonCapabilitiesInvalid, err}
	}
	if err := m.Validate(); err != nil {
		return Admission{}, &AdmitError{ReasonManifestInvalid, err}
	}
	if caps.Runtime == nil && caps.Direct == nil {
		return Admission{}, &AdmitError{ReasonNoEndpointKinds, errors.New("neither runtime nor direct endpoints are declared")}
	}
	a := Admission{caps: cloneCaps(caps), budgets: map[manifest.Frontend]handoff.Budget{}}
	for _, b := range m.Budgets {
		if !declaresFrontend(caps, b.Frontend) {
			return Admission{}, &AdmitError{ReasonBudgetFrontendUndeclared, fmt.Errorf("budget for %q, which the adapter does not declare", string(b.Frontend))}
		}
		unit := handoff.UnitChars
		if b.Unit == manifest.UnitTokens {
			unit = handoff.UnitTokens
		}
		a.budgets[b.Frontend] = handoff.Budget{Unit: unit, Limit: b.Limit}
	}
	if m.AckMarkers != nil {
		set, err := handoffMarkers(*m.AckMarkers).Compile()
		if err != nil {
			return Admission{}, &AdmitError{ReasonMarkersInvalid, err}
		}
		a.markers = set
	}
	return a, nil
}

func cloneCaps(c adapterproto.Capabilities) adapterproto.Capabilities {
	out := adapterproto.Capabilities{}
	if c.Runtime != nil {
		r := *c.Runtime
		r.Frontends = slices.Clone(r.Frontends)
		out.Runtime = &r
	}
	if c.Direct != nil {
		d := *c.Direct
		out.Direct = &d
	}
	return out
}

// declaresFrontend reports whether the capabilities include the front end of a
// budget: cli and mcp are the front ends of a runtime, direct is the direct
// endpoints themselves.
func declaresFrontend(c adapterproto.Capabilities, f manifest.Frontend) bool {
	switch f {
	case manifest.FrontendDirect:
		return c.Direct != nil
	case manifest.FrontendCLI:
		return c.Runtime != nil && slices.Contains(c.Runtime.Frontends, adapterproto.FrontendCLI)
	case manifest.FrontendMCP:
		return c.Runtime != nil && slices.Contains(c.Runtime.Frontends, adapterproto.FrontendMCP)
	}
	return false
}

func handoffMarkers(a manifest.AckMarkers) handoff.AckMarkers {
	conv := func(in []manifest.Marker) []handoff.Marker {
		var out []handoff.Marker
		for _, m := range in {
			var hm handoff.Marker
			if m.Literal != nil {
				hm.Literal = *m.Literal
			}
			if m.Regexp != nil {
				hm.Regexp = *m.Regexp
			}
			out = append(out, hm)
		}
		return out
	}
	return handoff.AckMarkers{Replaced: conv(a.Replaced), Truncated: conv(a.Truncated)}
}

// Capabilities returns the declared capabilities.
func (a Admission) Capabilities() adapterproto.Capabilities { return cloneCaps(a.caps) }

// Declares reports whether the adapter may own endpoints of the kind. Declaring
// a runtime brings the runtime kind and the session kind together: session
// endpoints exist only through a runtime.
func (a Admission) Declares(k EndpointKind) bool {
	switch k {
	case KindRuntime, KindSession:
		return a.caps.Runtime != nil
	case KindDirect:
		return a.caps.Direct != nil
	}
	return false
}

// DeclaredKinds lists the kinds the adapter may own, in the order of Kinds.
func (a Admission) DeclaredKinds() []EndpointKind {
	var out []EndpointKind
	for _, k := range Kinds() {
		if a.Declares(k) {
			out = append(out, k)
		}
	}
	return out
}

// MaxDirectEndpoints is the most direct endpoints the adapter may register; zero
// when it declared none.
func (a Admission) MaxDirectEndpoints() int {
	if a.caps.Direct == nil {
		return 0
	}
	return a.caps.Direct.MaxEndpoints
}

// Signals lists the control signals an endpoint of the kind accepts, in the
// order of authz.Actions. The list is written into the node's declaration of the
// endpoint. A signal that authorization allows but the endpoint does not accept
// is answered by the daemon as unsupported, without calling the adapter.
//
//   - runtime: session.create and endpoint.status;
//   - session: message.send (delivered as a prompt), session.close and
//     endpoint.status, and session.interrupt when the adapter declared interrupt;
//   - direct: message.send and endpoint.status.
//
// An undeclared or unknown kind accepts nothing.
func (a Admission) Signals(k EndpointKind) []authz.Action {
	if !a.Declares(k) {
		return nil
	}
	switch k {
	case KindRuntime:
		return []authz.Action{authz.ActionSessionCreate, authz.ActionEndpointStatus}
	case KindSession:
		out := []authz.Action{authz.ActionMessageSend, authz.ActionSessionClose}
		if a.caps.Runtime.Interrupt {
			out = append(out, authz.ActionSessionInterrupt)
		}
		return append(out, authz.ActionEndpointStatus)
	case KindDirect:
		return []authz.Action{authz.ActionMessageSend, authz.ActionEndpointStatus}
	}
	return nil
}

// AcceptsSignal reports whether an endpoint of the kind accepts the action as a
// control signal.
func (a Admission) AcceptsSignal(k EndpointKind, action authz.Action) bool {
	return slices.Contains(a.Signals(k), action)
}

// Budget is the output budget of recv for a front end: the one the manifest
// declares, or handoff's conservative default when it declares none.
func (a Admission) Budget(f manifest.Frontend) handoff.Budget {
	if b, ok := a.budgets[f]; ok {
		return b
	}
	return handoff.DefaultBudget()
}

// Markers returns the compiled acknowledgement markers; the zero set declares
// nothing.
func (a Admission) Markers() handoff.MarkerSet { return a.markers }
