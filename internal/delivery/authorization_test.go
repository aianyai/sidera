package delivery

import (
	"testing"

	"github.com/aianyai/sidera/internal/authz"
)

func TestIntakeAuthorization(t *testing.T) {
	rows := []struct {
		rule string
		in   authz.Verdict
		want Authorization
	}{
		{"an allow is allowed", authz.Verdict{Outcome: authz.OutcomeAllow, Reason: authz.ReasonGranted}, allow},
		{"an allow by reply permission is allowed", authz.Verdict{Outcome: authz.OutcomeAllow, Reason: authz.ReasonReplyPermission}, allow},
		{"a suspension is suspended", authz.Verdict{Outcome: authz.OutcomeSuspend, Reason: authz.ReasonTrustSuspended}, suspend},
		{"a verdict without an outcome is the zero authorization, which Intake refuses", authz.Verdict{}, Authorization{}},
		{"an out-of-range outcome is the zero authorization", authz.Verdict{Outcome: 99, Reason: authz.ReasonGranted}, Authorization{}},
		{"a rejection with no reason is unauthorized", authz.Verdict{Outcome: authz.OutcomeReject}, deny(RejectUnauthorized)},
		{"a rejection with an unknown reason is unauthorized", authz.Verdict{Outcome: authz.OutcomeReject, Reason: 200}, deny(RejectUnauthorized)},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := IntakeAuthorization(r.in); got != r.want {
				t.Errorf("got %+v, want %+v", got, r.want)
			}
		})
	}
}

// rejectMapping is the mapping every rejecting policy reason must have. A
// reason that is missing here fails TestIntakeAuthorizationMapsEveryReason, so
// a new policy reason cannot be left to the unknown-reason fallback unnoticed.
var rejectMapping = map[authz.Reason]RejectReason{
	authz.ReasonTrustDenied:                   RejectUnauthorized,
	authz.ReasonMalformedRequest:              RejectMalformed,
	authz.ReasonExplicitDeny:                  RejectUnauthorized,
	authz.ReasonNotAuthorized:                 RejectUnauthorized,
	authz.ReasonBodyTooLarge:                  RejectTooLarge,
	authz.ReasonPresetNotAllowed:              RejectUnauthorized,
	authz.ReasonEndpointPaused:                RejectEndpointDeclined,
	authz.ReasonEndpointRefusesMultiRecipient: RejectEndpointDeclined,
	authz.ReasonSenderNotAccepted:             RejectEndpointDeclined,
	authz.ReasonEndpointHidden:                RejectEndpointMissing,
}

func TestIntakeAuthorizationRejectReasons(t *testing.T) {
	rows := []struct {
		rule   string
		reason authz.Reason
		want   RejectReason
	}{
		{"trust denied is unauthorized", authz.ReasonTrustDenied, RejectUnauthorized},
		{"a malformed request is malformed", authz.ReasonMalformedRequest, RejectMalformed},
		{"an explicit deny is unauthorized, not revealing which rule", authz.ReasonExplicitDeny, RejectUnauthorized},
		{"no covering allow is unauthorized", authz.ReasonNotAuthorized, RejectUnauthorized},
		{"a body over the cap is too large", authz.ReasonBodyTooLarge, RejectTooLarge},
		{"a preset that is not granted is unauthorized", authz.ReasonPresetNotAllowed, RejectUnauthorized},
		{"a paused endpoint declares it does not accept", authz.ReasonEndpointPaused, RejectEndpointDeclined},
		{"an endpoint refusing several recipients declares it does not accept", authz.ReasonEndpointRefusesMultiRecipient, RejectEndpointDeclined},
		{"a sender the endpoint does not accept is declined by the endpoint", authz.ReasonSenderNotAccepted, RejectEndpointDeclined},
		{"a hidden endpoint is reported as not existing, so it is not revealed", authz.ReasonEndpointHidden, RejectEndpointMissing},
		{"an unknown reason is unauthorized", authz.Reason(250), RejectUnauthorized},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			got := IntakeAuthorization(authz.Verdict{Outcome: authz.OutcomeReject, Reason: r.reason})
			if got != deny(r.want) {
				t.Errorf("got %+v, want a denial with %v", got, r.want)
			}
		})
	}
}

// TestIntakeAuthorizationMapsEveryReason walks every named policy reason, so a
// reason added to the policy package fails here until it is mapped on purpose.
func TestIntakeAuthorizationMapsEveryReason(t *testing.T) {
	named := 0
	for r := authz.Reason(1); r.String() != "invalid"; r++ {
		named++
		v := authz.Verdict{Outcome: r.Outcome(), Reason: r}
		got := IntakeAuthorization(v)
		switch r.Outcome() {
		case authz.OutcomeAllow:
			if got != allow {
				t.Errorf("%v: got %+v, want allow", r, got)
			}
		case authz.OutcomeSuspend:
			if got != suspend {
				t.Errorf("%v: got %+v, want suspend", r, got)
			}
		case authz.OutcomeReject:
			want, mapped := rejectMapping[r]
			if !mapped {
				t.Errorf("%v: rejecting reason has no row in rejectMapping; map it explicitly", r)
				continue
			}
			if got != deny(want) {
				t.Errorf("%v: got %+v, want a denial with %v", r, got, want)
			}
			if got.validate() != nil {
				t.Errorf("%v: the authorization is not valid input for Intake: %v", r, got.validate())
			}
		default:
			t.Errorf("%v belongs to no outcome", r)
		}
	}
	if named == 0 {
		t.Fatal("no policy reason was visited")
	}
}
