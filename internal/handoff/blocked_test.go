package handoff

import (
	"testing"
	"time"
)

func failed(d time.Duration) Attempt {
	return Attempt{At: after(d), Outcome: AttemptFailed, Kind: PromptKindFirst}
}

func unknown(d time.Duration) Attempt {
	return Attempt{At: after(d), Outcome: AttemptUnknown, Kind: PromptKindFirst}
}

func TestDefaultBlockedParams(t *testing.T) {
	if got := DefaultBlockedParams().Threshold; got != 5*time.Minute {
		t.Fatalf("threshold = %v, want 5 minutes", got)
	}
}

func TestAssessBlocked(t *testing.T) {
	offline := Signals{AdapterOffline: true}
	// The cause began with the first failed prompt, one minute in.
	offlineSince1m := Signals{AdapterOffline: true, Since: after(time.Minute)}
	rows := []struct {
		rule string
		in   BlockedInput
		now  time.Time
		want BlockedVerdict
	}{
		// What blocks, and with which cause.
		{"adapter offline for longer than the threshold: blocked, since the first failure",
			BlockedInput{OldestPendingAt: after(0), Signals: offlineSince1m, Attempts: []Attempt{failed(time.Minute), failed(2 * time.Minute)}},
			after(6*time.Minute + time.Nanosecond),
			BlockedVerdict{Status: BlockedNew, Reason: ReasonPersistentFailure, Cause: CauseAdapterOffline, Since: after(time.Minute)}},
		{"runtime authentication invalid: blocked with that cause",
			BlockedInput{OldestPendingAt: after(0), Signals: Signals{AuthInvalid: true, Since: after(time.Minute)}, Attempts: []Attempt{failed(time.Minute)}},
			after(time.Hour),
			BlockedVerdict{Status: BlockedNew, Reason: ReasonPersistentFailure, Cause: CauseAuthInvalid, Since: after(time.Minute)}},
		{"native service unavailable: blocked with that cause",
			BlockedInput{OldestPendingAt: after(0), Signals: Signals{ServiceUnavailable: true, Since: after(time.Minute)}, Attempts: []Attempt{failed(time.Minute)}},
			after(time.Hour),
			BlockedVerdict{Status: BlockedNew, Reason: ReasonPersistentFailure, Cause: CauseServiceUnavailable, Since: after(time.Minute)}},
		{"several causes at once: the adapter being offline is named first",
			BlockedInput{OldestPendingAt: after(0), Signals: Signals{AdapterOffline: true, AuthInvalid: true, ServiceUnavailable: true, Since: after(time.Minute)},
				Attempts: []Attempt{failed(time.Minute)}},
			after(time.Hour),
			BlockedVerdict{Status: BlockedNew, Reason: ReasonPersistentFailure, Cause: CauseAdapterOffline, Since: after(time.Minute)}},
		{"authentication outranks an unavailable service",
			BlockedInput{OldestPendingAt: after(0), Signals: Signals{AuthInvalid: true, ServiceUnavailable: true, Since: after(time.Minute)},
				Attempts: []Attempt{failed(time.Minute)}},
			after(time.Hour),
			BlockedVerdict{Status: BlockedNew, Reason: ReasonPersistentFailure, Cause: CauseAuthInvalid, Since: after(time.Minute)}},

		// The threshold.
		{"failing for exactly the threshold is not yet blocked",
			BlockedInput{OldestPendingAt: after(0), Signals: offlineSince1m, Attempts: []Attempt{failed(time.Minute)}},
			after(6 * time.Minute),
			BlockedVerdict{Status: NotBlocked, Reason: ReasonBelowThreshold, RetryAt: after(6*time.Minute + time.Nanosecond)}},
		{"failing for less than the threshold is not blocked",
			BlockedInput{OldestPendingAt: after(0), Signals: offline, Attempts: []Attempt{failed(time.Minute)}},
			after(2 * time.Minute),
			BlockedVerdict{Status: NotBlocked, Reason: ReasonBelowThreshold, RetryAt: after(5*time.Minute + time.Nanosecond)}},

		// Never blocked: the agent's own behaviour.
		{"busy agent: no prompt was attempted, so nothing failed",
			BlockedInput{OldestPendingAt: after(0)},
			after(3 * time.Hour),
			BlockedVerdict{Status: NotBlocked, Reason: ReasonNoFailures}},
		{"agent that ignores delivered prompts: the prompts reach the session",
			BlockedInput{OldestPendingAt: after(0), Attempts: []Attempt{delivered(time.Minute, PromptKindFirst), delivered(2*time.Minute, PromptKindReprompt)}},
			after(3 * time.Hour),
			BlockedVerdict{Status: NotBlocked, Reason: ReasonNoFailures}},
		{"ignoring agent while a cause is also active: delivered prompts still mean not blocked",
			BlockedInput{OldestPendingAt: after(0), Signals: offline, Attempts: []Attempt{delivered(time.Minute, PromptKindFirst)}},
			after(3 * time.Hour),
			BlockedVerdict{Status: NotBlocked, Reason: ReasonNoFailures}},
		{"nothing waits: nothing is stuck, whatever the signals say",
			BlockedInput{Signals: offline, Attempts: []Attempt{failed(time.Minute)}},
			after(3 * time.Hour),
			BlockedVerdict{Status: NotBlocked, Reason: ReasonNothingPending}},
		{"failures without any known cause are not reported",
			BlockedInput{OldestPendingAt: after(0), Attempts: []Attempt{failed(time.Minute)}},
			after(3 * time.Hour),
			BlockedVerdict{Status: NotBlocked, Reason: ReasonNoKnownCause}},

		// A cause with no failed attempt recorded: the agent was busy.
		{"busy, then the adapter goes offline beyond the threshold with no attempts at all: blocked since it went offline",
			BlockedInput{OldestPendingAt: after(0), Signals: Signals{AdapterOffline: true, Since: after(10 * time.Minute)}},
			after(16*time.Minute + time.Nanosecond),
			BlockedVerdict{Status: BlockedNew, Reason: ReasonPersistentFailure, Cause: CauseAdapterOffline, Since: after(10 * time.Minute)}},
		{"active cause with unknown start is measured from the oldest pending message",
			BlockedInput{OldestPendingAt: after(0), Signals: offline},
			after(5*time.Minute + time.Nanosecond),
			BlockedVerdict{Status: BlockedNew, Reason: ReasonPersistentFailure, Cause: CauseAdapterOffline, Since: after(0)}},
		{"active cause with unknown start is not blocked before the message has waited the threshold",
			BlockedInput{OldestPendingAt: after(0), Signals: offline},
			after(5 * time.Minute),
			BlockedVerdict{Status: NotBlocked, Reason: ReasonBelowThreshold, RetryAt: after(5*time.Minute + time.Nanosecond)}},
		{"busy, then the adapter goes offline but not yet for the threshold: not blocked",
			BlockedInput{OldestPendingAt: after(0), Signals: Signals{AdapterOffline: true, Since: after(10 * time.Minute)}},
			after(15 * time.Minute),
			BlockedVerdict{Status: NotBlocked, Reason: ReasonBelowThreshold, RetryAt: after(15*time.Minute + time.Nanosecond)}},
		{"a cause older than the waiting message counts from the message",
			BlockedInput{OldestPendingAt: after(20 * time.Minute), Signals: Signals{AuthInvalid: true, Since: after(0)}},
			after(24 * time.Minute),
			BlockedVerdict{Status: NotBlocked, Reason: ReasonBelowThreshold, RetryAt: after(25*time.Minute + time.Nanosecond)}},
		{"a cause older than the waiting message blocks once the message has waited the threshold",
			BlockedInput{OldestPendingAt: after(20 * time.Minute), Signals: Signals{AuthInvalid: true, Since: after(0)}},
			after(26 * time.Minute),
			BlockedVerdict{Status: BlockedNew, Reason: ReasonPersistentFailure, Cause: CauseAuthInvalid, Since: after(20 * time.Minute)}},
		{"busy agent with the adapter online is never blocked, however long the message waits",
			BlockedInput{OldestPendingAt: after(0), Signals: Signals{Since: after(time.Minute)}},
			after(3 * time.Hour),
			BlockedVerdict{Status: NotBlocked, Reason: ReasonNoFailures}},
		{"a failed attempt before the cause began starts the run earlier",
			BlockedInput{OldestPendingAt: after(0), Signals: Signals{AdapterOffline: true, Since: after(10 * time.Minute)},
				Attempts: []Attempt{failed(2 * time.Minute)}},
			after(time.Hour),
			BlockedVerdict{Status: BlockedNew, Reason: ReasonPersistentFailure, Cause: CauseAdapterOffline, Since: after(2 * time.Minute)}},
		{"a prompt delivered after the cause began means the cause did not stop prompts",
			BlockedInput{OldestPendingAt: after(0), Signals: Signals{AdapterOffline: true, Since: after(10 * time.Minute)},
				Attempts: []Attempt{delivered(12*time.Minute, PromptKindFirst)}},
			after(time.Hour),
			BlockedVerdict{Status: NotBlocked, Reason: ReasonNoFailures}},
		{"a prompt delivered before the cause began does not hide it",
			BlockedInput{OldestPendingAt: after(0), Signals: Signals{AdapterOffline: true, Since: after(10 * time.Minute)},
				Attempts: []Attempt{delivered(2*time.Minute, PromptKindFirst)}},
			after(time.Hour),
			BlockedVerdict{Status: BlockedNew, Reason: ReasonPersistentFailure, Cause: CauseAdapterOffline, Since: after(10 * time.Minute)}},

		// The failing run.
		{"a delivered prompt ends the run: later failures start a new one",
			BlockedInput{OldestPendingAt: after(0), Signals: offline,
				Attempts: []Attempt{failed(time.Minute), delivered(2*time.Minute, PromptKindFirst), failed(50 * time.Minute)}},
			after(54 * time.Minute),
			BlockedVerdict{Status: NotBlocked, Reason: ReasonBelowThreshold, RetryAt: after(55*time.Minute + time.Nanosecond)}},
		{"a delivered prompt after the failures ends the run",
			BlockedInput{OldestPendingAt: after(0), Signals: offline,
				Attempts: []Attempt{failed(time.Minute), delivered(2*time.Minute, PromptKindFirst)}},
			after(time.Hour),
			BlockedVerdict{Status: NotBlocked, Reason: ReasonNoFailures}},
		{"an unknown outcome neither proves a failure nor ends the run",
			BlockedInput{OldestPendingAt: after(0), Signals: offlineSince1m,
				Attempts: []Attempt{failed(time.Minute), unknown(2 * time.Minute), failed(3 * time.Minute)}},
			after(time.Hour),
			BlockedVerdict{Status: BlockedNew, Reason: ReasonPersistentFailure, Cause: CauseAdapterOffline, Since: after(time.Minute)}},
		{"unknown outcomes alone are no failure: only how long the cause has lasted counts",
			BlockedInput{OldestPendingAt: after(0), Signals: Signals{AdapterOffline: true, Since: after(58 * time.Minute)},
				Attempts: []Attempt{unknown(time.Minute), unknown(2 * time.Minute)}},
			after(time.Hour),
			BlockedVerdict{Status: NotBlocked, Reason: ReasonBelowThreshold, RetryAt: after(63*time.Minute + time.Nanosecond)}},
		{"failures before the oldest waiting message belong to earlier messages: only the cause counts",
			BlockedInput{OldestPendingAt: after(30 * time.Minute), Signals: Signals{AdapterOffline: true, Since: after(55 * time.Minute)},
				Attempts: []Attempt{failed(time.Minute)}},
			after(time.Hour),
			BlockedVerdict{Status: NotBlocked, Reason: ReasonBelowThreshold, RetryAt: after(60*time.Minute + time.Nanosecond)}},

		// Edge-triggered reporting.
		{"already reported: still blocked, nothing new to report, reported cause kept",
			BlockedInput{OldestPendingAt: after(0), Signals: Signals{AuthInvalid: true}, Attempts: []Attempt{failed(time.Minute)},
				Reported: BlockedReport{Cause: CauseAdapterOffline, Since: after(time.Minute)}},
			after(time.Hour),
			BlockedVerdict{Status: BlockedOngoing, Reason: ReasonPersistentFailure, Cause: CauseAdapterOffline, Since: after(time.Minute)}},
		{"oldest waiting message expired and the next arrived recently: the report stands while the cause holds",
			BlockedInput{OldestPendingAt: after(30 * time.Minute), Signals: offline, Attempts: []Attempt{failed(time.Minute)},
				Reported: BlockedReport{Cause: CauseAdapterOffline, Since: after(time.Minute)}},
			after(31 * time.Minute),
			BlockedVerdict{Status: BlockedOngoing, Reason: ReasonPersistentFailure, Cause: CauseAdapterOffline, Since: after(time.Minute)}},
		{"adapter reconnects reporting a new cause that began just now: the report stands",
			BlockedInput{OldestPendingAt: after(0), Signals: Signals{AuthInvalid: true, Since: after(59 * time.Minute)},
				Reported: BlockedReport{Cause: CauseAdapterOffline, Since: after(time.Minute)}},
			after(time.Hour),
			BlockedVerdict{Status: BlockedOngoing, Reason: ReasonPersistentFailure, Cause: CauseAdapterOffline, Since: after(time.Minute)}},
		{"a prompt delivered before the report does not end it while the cause holds",
			BlockedInput{OldestPendingAt: after(0), Signals: offline, Attempts: []Attempt{delivered(2*time.Minute, PromptKindFirst)},
				Reported: BlockedReport{Cause: CauseAdapterOffline, Since: after(10 * time.Minute)}},
			after(time.Hour),
			BlockedVerdict{Status: BlockedOngoing, Reason: ReasonPersistentFailure, Cause: CauseAdapterOffline, Since: after(10 * time.Minute)}},
		{"cause cleared after a report: recovered",
			BlockedInput{OldestPendingAt: after(0), Attempts: []Attempt{failed(time.Minute)},
				Reported: BlockedReport{Cause: CauseAdapterOffline, Since: after(time.Minute)}},
			after(time.Hour),
			BlockedVerdict{Status: BlockedRecovered, Reason: ReasonNoKnownCause, Cause: CauseAdapterOffline, Since: after(time.Minute)}},
		{"prompt delivered after a report: recovered",
			BlockedInput{OldestPendingAt: after(0), Signals: offline,
				Attempts: []Attempt{failed(time.Minute), delivered(30*time.Minute, PromptKindFirst)},
				Reported: BlockedReport{Cause: CauseAdapterOffline, Since: after(time.Minute)}},
			after(time.Hour),
			BlockedVerdict{Status: BlockedRecovered, Reason: ReasonNoFailures, Cause: CauseAdapterOffline, Since: after(time.Minute)}},
		{"messages read by other means after a report: recovered",
			BlockedInput{Signals: offline, Attempts: []Attempt{failed(time.Minute)},
				Reported: BlockedReport{Cause: CauseAdapterOffline, Since: after(time.Minute)}},
			after(time.Hour),
			BlockedVerdict{Status: BlockedRecovered, Reason: ReasonNothingPending, Cause: CauseAdapterOffline, Since: after(time.Minute)}},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := AssessBlocked(r.in, DefaultBlockedParams(), r.now); got != r.want {
				t.Fatalf("got %+v, want %+v", got, r.want)
			}
		})
	}
}

func TestAssessBlockedThresholdIsAParameter(t *testing.T) {
	in := BlockedInput{OldestPendingAt: after(0), Signals: Signals{AdapterOffline: true}, Attempts: []Attempt{failed(0)}}
	if got := AssessBlocked(in, BlockedParams{Threshold: time.Hour}, after(30*time.Minute)); got.Status != NotBlocked {
		t.Fatalf("with a one hour threshold, 30 minutes of failures: got %+v", got)
	}
	if got := AssessBlocked(in, BlockedParams{Threshold: time.Minute}, after(30*time.Minute)); got.Status != BlockedNew {
		t.Fatalf("with a one minute threshold, 30 minutes of failures: got %+v", got)
	}
}

// A blocked period is reported once and its recovery once, however often it is
// evaluated, when the caller stores what each verdict says to store.
func TestAssessBlockedReportsOnceAndRecoversOnce(t *testing.T) {
	var reported BlockedReport
	statuses := map[BlockedStatus]int{}
	in := BlockedInput{OldestPendingAt: after(0), Signals: Signals{AdapterOffline: true},
		Attempts: []Attempt{failed(0)}}

	step := func(now time.Duration) {
		in.Reported = reported
		v := AssessBlocked(in, DefaultBlockedParams(), after(now))
		statuses[v.Status]++
		switch v.Status {
		case BlockedNew:
			reported = BlockedReport{Cause: v.Cause, Since: v.Since}
		case BlockedRecovered:
			reported = BlockedReport{}
		}
	}

	for _, m := range []time.Duration{1, 3, 5, 6, 10, 20, 30} { // adapter offline throughout
		step(m * time.Minute)
	}
	in.Signals = Signals{} // the adapter reconnects
	in.Attempts = append(in.Attempts, delivered(31*time.Minute, PromptKindFirst))
	for _, m := range []time.Duration{31, 32, 40} {
		step(m * time.Minute)
	}

	if statuses[BlockedNew] != 1 || statuses[BlockedRecovered] != 1 {
		t.Fatalf("statuses = %v, want exactly one BlockedNew and one BlockedRecovered", statuses)
	}
	if statuses[BlockedOngoing] != 3 {
		t.Fatalf("ongoing = %d, want 3 (the evaluations at 10, 20 and 30 minutes)", statuses[BlockedOngoing])
	}
}

// RetryAt is the first instant at which the run is longer than the threshold: asked
// again then, with nothing else changed, the verdict is a new block; asked at the
// instant before, it is not. Without a cause there is nothing to wait for.
func TestAssessBlockedRetryAtIsWhenTheRunPassesTheThreshold(t *testing.T) {
	ins := map[string]BlockedInput{
		"a failed prompt and the cause":     {OldestPendingAt: after(0), Signals: Signals{AdapterOffline: true, Since: after(time.Minute)}, Attempts: []Attempt{failed(time.Minute)}},
		"a cause of unknown start":          {OldestPendingAt: after(3 * time.Minute), Signals: Signals{AuthInvalid: true}},
		"a cause that began after the wait": {OldestPendingAt: after(0), Signals: Signals{ServiceUnavailable: true, Since: after(9 * time.Minute)}},
	}
	for name, in := range ins {
		t.Run(name, func(t *testing.T) {
			first := AssessBlocked(in, DefaultBlockedParams(), after(4*time.Minute))
			if first.RetryAt.IsZero() {
				t.Fatalf("no retry instant for a run that is short: %+v", first)
			}
			if got := AssessBlocked(in, DefaultBlockedParams(), first.RetryAt.Add(-time.Nanosecond)); got.RetryAt != first.RetryAt || got.Reason != ReasonBelowThreshold {
				t.Errorf("an instant before RetryAt: %+v, want still below the threshold", got)
			}
			in.Reported = BlockedReport{}
			if got := AssessBlocked(in, DefaultBlockedParams(), first.RetryAt); got.Status != BlockedNew || !got.RetryAt.IsZero() {
				t.Errorf("at RetryAt: %+v, want a new block and no further retry", got)
			}
		})
	}
	noCause := BlockedInput{OldestPendingAt: after(0), Attempts: []Attempt{failed(time.Minute)}}
	if got := AssessBlocked(noCause, DefaultBlockedParams(), after(2*time.Minute)); !got.RetryAt.IsZero() {
		t.Errorf("a short run with no cause has retry instant %v, want none", got.RetryAt)
	}
}
