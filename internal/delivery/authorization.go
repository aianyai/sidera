package delivery

import (
	"fmt"

	"github.com/aianyai/sidera/internal/authz"
)

// AuthorizationKind is the three-way verdict of the authorization check,
// computed from the policy's verdict by IntakeAuthorization.
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
// with the reason a refusal is reported to the sender. IntakeAuthorization maps
// the policy's reasons to RejectReason, so the sender hears why and not only
// that.
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

// IntakeAuthorization is the single mapping from the policy's verdict to the
// input of Intake and Handout: an allow is AuthorizationAllow, a suspension is
// AuthorizationSuspend, and a rejection is AuthorizationDeny carrying the
// RejectReason the sender is told.
//
// The reasons map as follows, so the sender hears why it was refused without
// learning more than it may:
//
//   - trust denied, an explicit deny, no covering allow and a preset that is not
//     granted are "unauthorized": the sender is not entitled to know which
//     rule refused it;
//   - a body over the size caps is "too large";
//   - a paused endpoint, one that refuses several recipients and one that does
//     not accept this sender are "endpoint declined": the endpoint's own
//     declaration says no;
//   - a malformed request is "malformed";
//   - an endpoint hidden from the directory is "endpoint missing", so a hidden
//     endpoint cannot be told apart from one that does not exist;
//   - any reason this mapping does not know is "unauthorized": an unlisted
//     refusal must not become more informative than the listed ones.
//
// A verdict whose outcome is none of the three yields the zero Authorization,
// which Intake and Handout refuse as invalid input instead of guessing.
func IntakeAuthorization(v authz.Verdict) Authorization {
	switch v.Outcome {
	case authz.OutcomeAllow:
		return Authorization{Kind: AuthorizationAllow}
	case authz.OutcomeSuspend:
		return Authorization{Kind: AuthorizationSuspend}
	case authz.OutcomeReject:
		return Authorization{Kind: AuthorizationDeny, Reject: rejectReasonFor(v.Reason)}
	}
	return Authorization{}
}

func rejectReasonFor(r authz.Reason) RejectReason {
	switch r {
	case authz.ReasonBodyTooLarge:
		return RejectTooLarge
	case authz.ReasonMalformedRequest:
		return RejectMalformed
	case authz.ReasonEndpointPaused, authz.ReasonEndpointRefusesMultiRecipient, authz.ReasonSenderNotAccepted:
		return RejectEndpointDeclined
	case authz.ReasonEndpointHidden:
		return RejectEndpointMissing
	case authz.ReasonTrustDenied, authz.ReasonExplicitDeny, authz.ReasonNotAuthorized, authz.ReasonPresetNotAllowed:
		return RejectUnauthorized
	}
	return RejectUnauthorized
}
