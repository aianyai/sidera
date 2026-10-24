package delivery

import (
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/timing"
)

var (
	allow   = Authorization{Kind: AuthorizationAllow}
	suspend = Authorization{Kind: AuthorizationSuspend}
)

// roomy is an endpoint backlog that has plenty of room: the traffic the limit
// governs is not refused for it.
var roomy = Backlog{Queued: 0, Limit: 200}

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
			v, err := Intake(now, r.ab, authz.TrafficMessage, r.auth, roomy)
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
			v, err := Intake(now, now.Add(DefaultAcceptBefore), authz.TrafficMessage, deny(why), roomy)
			if err != nil || v.Outcome != IntakeReject || v.Reject != why {
				t.Errorf("got %+v, %v", v, err)
			}
		})
	}
	t.Run("a denial without a reason is invalid input", func(t *testing.T) {
		if _, err := Intake(now, now.Add(time.Minute), authz.TrafficMessage, deny(RejectNone), roomy); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("an unset authorization is invalid input", func(t *testing.T) {
		if _, err := Intake(now, now.Add(time.Minute), authz.TrafficMessage, Authorization{}, roomy); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestIntakeBacklogLimit(t *testing.T) {
	now := at(time.Hour)
	ab := now.Add(DefaultAcceptBefore)
	full := Backlog{Queued: 200, Limit: 200}
	rows := []struct {
		rule    string
		traffic authz.TrafficKind
		ab      time.Time
		auth    Authorization
		backlog Backlog
		want    IntakeOutcome
		reject  RejectReason
	}{
		{"an empty backlog accepts a message", authz.TrafficMessage, ab, allow, Backlog{Queued: 0, Limit: 200}, IntakeAccept, RejectNone},
		{"one below the limit still accepts a message", authz.TrafficMessage, ab, allow, Backlog{Queued: 199, Limit: 200}, IntakeAccept, RejectNone},
		{"at the limit a message is rejected as backlog full", authz.TrafficMessage, ab, allow, full, IntakeReject, RejectBacklogFull},
		{"above the limit a message is rejected as backlog full", authz.TrafficMessage, ab, allow, Backlog{Queued: 201, Limit: 200}, IntakeReject, RejectBacklogFull},
		{"an owner's lower limit applies: one below accepts", authz.TrafficMessage, ab, allow, Backlog{Queued: 0, Limit: 1}, IntakeAccept, RejectNone},
		{"an owner's lower limit applies: at it rejects", authz.TrafficMessage, ab, allow, Backlog{Queued: 1, Limit: 1}, IntakeReject, RejectBacklogFull},
		{"an owner's higher limit applies", authz.TrafficMessage, ab, allow, Backlog{Queued: 200, Limit: 400}, IntakeAccept, RejectNone},
		{"a control signal is accepted at a full backlog", authz.TrafficControlSignal, ab, allow, full, IntakeAccept, RejectNone},
		{"system feedback is accepted at a full backlog", authz.TrafficSystemFeedback, ab, allow, full, IntakeAccept, RejectNone},
		{"system feedback is accepted far above the limit", authz.TrafficSystemFeedback, ab, allow, Backlog{Queued: 230, Limit: 200}, IntakeAccept, RejectNone},
		{"exempt traffic needs no backlog input", authz.TrafficSystemFeedback, ab, allow, Backlog{}, IntakeAccept, RejectNone},
		{"a control signal needs no backlog input", authz.TrafficControlSignal, ab, allow, Backlog{}, IntakeAccept, RejectNone},
		{"a denial is reported as such, not as backlog full, so a refused sender learns nothing of the queue", authz.TrafficMessage, ab, deny(RejectUnauthorized), full, IntakeReject, RejectUnauthorized},
		{"a suspension is not final and stores nothing, so it does not consult the backlog", authz.TrafficMessage, ab, suspend, full, IntakeSuspended, RejectNone},
		{"an expired request is rejected as expired at a full backlog", authz.TrafficMessage, now.Add(-time.Second), allow, full, IntakeReject, RejectExpired},
		{"a deadline out of range is rejected as such at a full backlog", authz.TrafficMessage, now.Add(3 * time.Hour), allow, full, IntakeReject, RejectDeadlineOutOfRange},
		{"a missing deadline is malformed at a full backlog", authz.TrafficMessage, time.Time{}, allow, full, IntakeReject, RejectMalformed},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			v, err := Intake(now, r.ab, r.traffic, r.auth, r.backlog)
			if err != nil {
				t.Fatal(err)
			}
			if v.Outcome != r.want || v.Reject != r.reject {
				t.Errorf("got %+v", v)
			}
		})
	}
}

func TestIntakeBacklogInputs(t *testing.T) {
	now := at(time.Hour)
	ab := now.Add(DefaultAcceptBefore)
	rows := []struct {
		rule    string
		traffic authz.TrafficKind
		auth    Authorization
		ab      time.Time
		backlog Backlog
	}{
		{"a message with no limit set is invalid input", authz.TrafficMessage, allow, ab, Backlog{Queued: 3}},
		{"a negative limit is invalid input", authz.TrafficMessage, allow, ab, Backlog{Queued: 0, Limit: -1}},
		{"a negative backlog count is invalid input", authz.TrafficMessage, allow, ab, Backlog{Queued: -1, Limit: 200}},
		{"an invalid limit is invalid input even when the request would be rejected anyway", authz.TrafficMessage, allow, now.Add(-time.Minute), Backlog{}},
		{"an invalid limit is invalid input even when authorization is denied", authz.TrafficMessage, deny(RejectUnauthorized), ab, Backlog{}},
		{"the unset traffic kind is invalid input", authz.TrafficKind(0), allow, ab, roomy},
		{"an unknown traffic kind is invalid input", authz.TrafficKind(99), allow, ab, roomy},
		{"trust maintenance is not a delivery to an endpoint", authz.TrafficTrustMaintenance, allow, ab, roomy},
		{"a status report is taken by ApplyReport, not judged by Intake", authz.TrafficStatusReport, allow, ab, roomy},
		{"a control reply is taken by HandleControlReply, not judged by Intake", authz.TrafficControlReply, allow, ab, roomy},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if _, err := Intake(now, r.ab, r.traffic, r.auth, r.backlog); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestEndpointBacklogLimit(t *testing.T) {
	if DefaultEndpointBacklogLimit != 200 {
		t.Errorf("DefaultEndpointBacklogLimit = %d, want 200", DefaultEndpointBacklogLimit)
	}
	rows := []struct {
		rule  string
		limit int
		ok    bool
	}{
		{"the default is a valid limit", DefaultEndpointBacklogLimit, true},
		{"one is the smallest limit", 1, true},
		{"an owner may raise the limit above the default", 400, true},
		{"zero is no limit set", 0, false},
		{"a negative limit is refused", -5, false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			err := ValidateEndpointBacklogLimit(r.limit)
			if (err == nil) != r.ok || (err != nil && !errors.Is(err, ErrBacklogLimit)) {
				t.Errorf("ValidateEndpointBacklogLimit(%d) = %v", r.limit, err)
			}
		})
	}
}

func TestOccupiesBacklogSlot(t *testing.T) {
	ab := at(10 * time.Minute)
	queued := targetRecord(t, t0, DefaultEndpointMaxQueue)
	leased := handOut(t, queued, at(time.Minute), 2, 1)
	notified, _ := mustApply(t, queued, at(time.Minute), Event{Type: EventNotified})
	delivered := deliverBody(t, queued, at(time.Minute))
	rejected, _ := mustApply(t, queued, at(time.Minute), Event{Type: EventRejected, Reject: RejectEndpointClosed})
	unknown, _ := mustApply(t, leased, at(time.Minute), Event{Type: EventRejected, Reject: RejectEndpointClosed})
	if unknown.State != StateDeliveryUnknown {
		t.Fatalf("setup: state %s", unknown.State)
	}
	control := NewTargetControlRecord(ab, ReportBy(ab))
	rows := []struct {
		rule    string
		rec     Record
		traffic authz.TrafficKind
		want    bool
	}{
		{"a queued message occupies a slot", queued, authz.TrafficMessage, true},
		{"a message leased to an agent still occupies its slot until it is acknowledged", leased, authz.TrafficMessage, true},
		{"a message that was prompted for still occupies a slot", notified, authz.TrafficMessage, true},
		{"a control record occupies no slot", control, authz.TrafficControlSignal, false},
		{"a control record occupies no slot whatever traffic the caller names", control, authz.TrafficMessage, false},
		{"system feedback occupies no slot, so it never takes room from ordinary messages", queued, authz.TrafficSystemFeedback, false},
		{"a delivered message occupies no slot", delivered, authz.TrafficMessage, false},
		{"a rejected message occupies no slot", rejected, authz.TrafficMessage, false},
		{"a message of unknown result occupies no slot: it is no longer queued", unknown, authz.TrafficMessage, false},
		{"the sender's record occupies no slot of the recipient", NewSourceRecord(ab), authz.TrafficMessage, false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := r.rec.OccupiesBacklogSlot(r.traffic); got != r.want {
				t.Errorf("OccupiesBacklogSlot = %v, want %v", got, r.want)
			}
		})
	}
}
