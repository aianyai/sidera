package delivery

import (
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/timing"
)

var (
	allow   = Authorization{Kind: AuthorizationAllow}
	suspend = Authorization{Kind: AuthorizationSuspend}
)

func deny(why RejectReason) Authorization {
	return Authorization{Kind: AuthorizationDeny, Reject: why}
}

func TestIntake(t *testing.T) {
	now := at(time.Hour)
	rows := []struct {
		rule   string
		ab     time.Time
		auth   Authorization
		want   IntakeOutcome
		reject RejectReason
	}{
		{"a valid request with allowed authorization is accepted", now.Add(DefaultAcceptBefore), allow, IntakeAccept, RejectNone},
		{"a deadline exactly now is still acceptable", now, allow, IntakeAccept, RejectNone},
		{"a deadline already past is rejected as expired", now.Add(-time.Second), allow, IntakeReject, RejectExpired},
		{"a deadline at now plus cap plus tolerance is acceptable", now.Add(MaxAcceptBefore + timing.ClockTolerance), allow, IntakeAccept, RejectNone},
		{"a deadline beyond now plus cap plus tolerance is rejected", now.Add(MaxAcceptBefore + timing.ClockTolerance + time.Second), allow, IntakeReject, RejectDeadlineOutOfRange},
		{"a missing deadline is malformed", time.Time{}, allow, IntakeReject, RejectMalformed},
		{"denied authorization is rejected as unauthorized", now.Add(DefaultAcceptBefore), deny(RejectUnauthorized), IntakeReject, RejectUnauthorized},
		{"suspended authorization is a non-final result, not a rejection", now.Add(DefaultAcceptBefore), suspend, IntakeSuspended, RejectNone},
		{"an expired request is rejected even if authorization is suspended", now.Add(-time.Minute), suspend, IntakeReject, RejectExpired},
		{"an out-of-range deadline is rejected even if authorization is suspended", now.Add(3 * time.Hour), suspend, IntakeReject, RejectDeadlineOutOfRange},
		{"an expired request is rejected as expired, not as unauthorized", now.Add(-time.Minute), deny(RejectUnauthorized), IntakeReject, RejectExpired},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			v, err := Intake(now, r.ab, r.auth)
			if err != nil {
				t.Fatal(err)
			}
			if v.Outcome != r.want || v.Reject != r.reject {
				t.Errorf("got %+v", v)
			}
		})
	}
	denials := []RejectReason{
		RejectUnauthorized, RejectEndpointMissing, RejectEndpointClosed, RejectEndpointDeclined,
		RejectTooLarge, RejectExpired, RejectBacklogExpired, RejectSecondCheckFailed,
		RejectMalformed, RejectDeadlineOutOfRange,
	}
	for _, why := range denials {
		t.Run("a denial is rejected with the reason it carries: "+why.String(), func(t *testing.T) {
			v, err := Intake(now, now.Add(DefaultAcceptBefore), deny(why))
			if err != nil || v.Outcome != IntakeReject || v.Reject != why {
				t.Errorf("got %+v, %v", v, err)
			}
		})
	}
	t.Run("a denial without a reason is invalid input", func(t *testing.T) {
		if _, err := Intake(now, now.Add(time.Minute), deny(RejectNone)); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("an unset authorization is invalid input", func(t *testing.T) {
		if _, err := Intake(now, now.Add(time.Minute), Authorization{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("err = %v", err)
		}
	})
}
