package delivery

import (
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/timing"
)

func TestReportByDerivation(t *testing.T) {
	ab := at(10 * time.Minute)
	rows := []struct {
		rule string
		got  time.Duration
		want time.Duration
	}{
		{"max redelivery period is lease count times the lease life cap", MaxRedeliveryPeriod, 4 * 15 * time.Minute},
		{"report_by offset is queue cap plus redelivery period plus tolerance", ReportBy(ab).Sub(ab), 2*time.Hour + 60*time.Minute + 2*time.Minute},
		{"default report_by is about 3h12m after sending", ReportBy(ab).Sub(t0), 10*time.Minute + 3*time.Hour + 2*time.Minute},
		{"settle deadline is the safety margin before report_by", ReportBy(ab).Sub(SettleBy(ReportBy(ab))), 5 * time.Minute},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if r.got != r.want {
				t.Errorf("got %v, want %v", r.got, r.want)
			}
		})
	}
}

func TestLeaseCount(t *testing.T) {
	if MaxLeases != 4 {
		t.Errorf("MaxLeases = %d, want the first hand-out plus three redeliveries", MaxLeases)
	}
}

func TestDecidedValues(t *testing.T) {
	rows := []struct {
		rule      string
		got, want time.Duration
	}{
		{"default accept_before", DefaultAcceptBefore, 10 * time.Minute},
		{"accept_before cap", MaxAcceptBefore, time.Hour},
		{"unreachable hint threshold", UnreachableHintAfter, 2 * time.Minute},
		{"default endpoint queue time", DefaultEndpointMaxQueue, 30 * time.Minute},
		{"protocol queue time cap", MaxQueueCap, 2 * time.Hour},
		{"lease duration", LeaseDuration, 5 * time.Minute},
		{"lease life cap", LeaseRenewalCap, 15 * time.Minute},
		{"report_by safety margin", ReportBySafetyMargin, 5 * time.Minute},
		{"clock tolerance", timing.ClockTolerance, 2 * time.Minute},
		{"max redeliveries", time.Duration(MaxRedeliveries), 3},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if r.got != r.want {
				t.Errorf("got %v, want %v", r.got, r.want)
			}
		})
	}
}

func TestRetention(t *testing.T) {
	ab := at(10 * time.Minute)
	rb := ReportBy(ab)
	rows := []struct {
		rule string
		got  time.Time
		want time.Time
	}{
		{"dedup record is kept until report_by", DedupRetainUntil(rb), rb},
		{"local idempotency key is kept at least as long as the dedup record", IdempotencyKeyRetainUntil(rb), DedupRetainUntil(rb)},
		{"a report created at deadline time is kept until report_by", StatusReportRetainUntil(ab, rb), rb},
		{"a report created before the deadline is kept until report_by", StatusReportRetainUntil(t0, rb), rb},
		{"a report created late is kept the whole window from its creation", StatusReportRetainUntil(rb.Add(-time.Hour), rb), rb.Add(-time.Hour).Add(ReportByOffset)},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if !r.got.Equal(r.want) {
				t.Errorf("got %v, want %v", r.got, r.want)
			}
		})
	}
	t.Run("no retention is shorter than report_by", func(t *testing.T) {
		for _, created := range []time.Time{t0, ab, rb, rb.Add(time.Hour)} {
			if StatusReportRetainUntil(created, rb).Before(rb) {
				t.Errorf("report created %v is kept less than until report_by", created)
			}
		}
	})
}

func TestValidateEndpointMaxQueue(t *testing.T) {
	rows := []struct {
		rule string
		in   time.Duration
		ok   bool
	}{
		{"default is allowed", DefaultEndpointMaxQueue, true},
		{"the protocol cap is allowed", MaxQueueCap, true},
		{"above the protocol cap is refused", MaxQueueCap + time.Second, false},
		{"zero is refused", 0, false},
		{"negative is refused", -time.Minute, false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			err := ValidateEndpointMaxQueue(r.in)
			if (err == nil) != r.ok {
				t.Errorf("err = %v, want ok=%v", err, r.ok)
			}
			if err != nil && !errors.Is(err, ErrMaxQueue) {
				t.Errorf("err = %v, want ErrMaxQueue", err)
			}
		})
	}
}

func TestBacklogDeadline(t *testing.T) {
	ab := at(10 * time.Minute)
	rb := ReportBy(ab)
	rows := []struct {
		rule       string
		acceptedAt time.Time
		reportBy   time.Time
		maxQueue   time.Duration
		want       time.Time
	}{
		{"endpoint limit counts from acceptance", at(time.Minute), rb, 30 * time.Minute, at(31 * time.Minute)},
		{"endpoint limit past the settle deadline is cut to it", at(time.Minute), rb, 10 * time.Hour, SettleBy(rb)},
		{"a short report_by cuts the endpoint limit", at(time.Minute), ab.Add(time.Hour), 2 * time.Hour, ab.Add(55 * time.Minute)},
		{"limit exactly at the settle deadline", SettleBy(rb).Add(-time.Hour), rb, time.Hour, SettleBy(rb)},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := BacklogDeadline(r.acceptedAt, r.reportBy, r.maxQueue); !got.Equal(r.want) {
				t.Errorf("got %v, want %v", got, r.want)
			}
		})
	}
}
