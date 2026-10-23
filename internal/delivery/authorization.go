package delivery

import "fmt"

// AuthorizationKind is the three-way verdict of the authorization check,
// computed by the caller from the policy and the state of the trust chain.
type AuthorizationKind uint8

const (
	// AuthorizationAllow: the action is permitted.
	AuthorizationAllow AuthorizationKind = iota + 1
	// AuthorizationSuspend: the action would be permitted but a node of the
	// trust chain is offline. The check is not final and neither side may
	// treat it as a refusal.
	AuthorizationSuspend
	// AuthorizationDeny: the action is refused.
	AuthorizationDeny
)

func (k AuthorizationKind) valid() bool { return k >= AuthorizationAllow && k <= AuthorizationDeny }

// Authorization is the verdict of the authorization check at intake together
// with the reason a refusal is reported to the sender. The caller maps its
// policy's reasons to RejectReason, so the sender hears why and not only that.
type Authorization struct {
	Kind AuthorizationKind
	// Reject is the reason of a refusal; it is required when Kind is
	// AuthorizationDeny and unused otherwise.
	Reject RejectReason
}

func (a Authorization) validate() error {
	switch {
	case !a.Kind.valid():
		return fmt.Errorf("%w: authorization kind %d", ErrInvalidInput, a.Kind)
	case a.Kind == AuthorizationDeny && a.Reject == RejectNone:
		return fmt.Errorf("%w: a denial needs a rejection reason", ErrInvalidInput)
	}
	return nil
}
