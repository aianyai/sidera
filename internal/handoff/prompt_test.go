package handoff

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

// after returns the instant d after t0.
func after(d time.Duration) time.Time { return t0.Add(d) }

var testPromptParams = PromptParams{MaxPerHour: 6, MinInterval: 5 * time.Minute}

func delivered(d time.Duration, k PromptKind) Attempt {
	return Attempt{At: after(d), Outcome: AttemptDelivered, Kind: k}
}

func TestPromptText(t *testing.T) {
	rows := []struct {
		rule  string
		count int
		want  string
	}{
		{"a single message is named in the singular", 1, "You have 1 new message. Read it with recv."},
		{"several messages are counted", 3, "You have 3 new messages. Read them with recv."},
		{"five messages arriving together are one prompt", 5, "You have 5 new messages. Read them with recv."},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := PromptText(r.count); got != r.want {
				t.Fatalf("PromptText(%d) = %q, want %q", r.count, got, r.want)
			}
		})
	}
}

func TestNewSessionPromptText(t *testing.T) {
	const want = "This is a new session. When you are told that new messages have arrived, read them with recv."
	if NewSessionPromptText != want {
		t.Fatalf("NewSessionPromptText = %q, want %q", NewSessionPromptText, want)
	}
	// Like every prompt it carries no message body and names the tool.
	if !strings.Contains(NewSessionPromptText, "recv") {
		t.Fatal("the prompt does not name recv")
	}
	for _, n := range []int{1, 2} {
		if NewSessionPromptText == PromptText(n) {
			t.Fatalf("the new-session prompt equals PromptText(%d)", n)
		}
	}
}

// The new-session prompt is not recorded as an attempt, so the first real
// message after creation is prompted at once. Recording it would make that
// message wait for the minimum interval behind an unread prompt.
func TestNewSessionPromptIsNotAnAttempt(t *testing.T) {
	arrived := PromptInput{Trigger: TriggerArrival, Pending: 1, LastArrivalAt: after(time.Minute)}
	now := after(time.Minute)

	t.Run("not recorded, as the contract says: the first message is prompted at once", func(t *testing.T) {
		got, err := DecidePrompt(arrived, testPromptParams, now)
		if err != nil {
			t.Fatal(err)
		}
		want := PromptDecision{Action: PromptSend, Reason: PromptFirst, Kind: PromptKindFirst, Count: 1}
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("recorded as a delivered attempt: the first message would wait", func(t *testing.T) {
		in := arrived
		in.Attempts = []Attempt{delivered(0, PromptKindFirst)}
		got, err := DecidePrompt(in, testPromptParams, now)
		if err != nil {
			t.Fatal(err)
		}
		want := PromptDecision{Action: PromptHold, Reason: PromptUnreadWaiting, RetryAt: after(testPromptParams.MinInterval)}
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})
}

func TestPromptParamsValidate(t *testing.T) {
	rows := []struct {
		rule string
		p    PromptParams
		ok   bool
	}{
		{"positive values are valid", PromptParams{MaxPerHour: 1, MinInterval: time.Second}, true},
		{"zero cap is rejected", PromptParams{MaxPerHour: 0, MinInterval: time.Second}, false},
		{"negative cap is rejected", PromptParams{MaxPerHour: -1, MinInterval: time.Second}, false},
		{"zero interval is rejected", PromptParams{MaxPerHour: 1}, false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			err := r.p.Validate()
			if (err == nil) != r.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, r.ok)
			}
			if err != nil && !errors.Is(err, ErrInvalidParams) {
				t.Fatalf("error %v does not wrap ErrInvalidParams", err)
			}
			if _, derr := DecidePrompt(PromptInput{Trigger: TriggerArrival, Pending: 1}, r.p, t0); (derr == nil) != r.ok {
				t.Fatalf("DecidePrompt err = %v, want ok=%v", derr, r.ok)
			}
		})
	}
}

func TestDecidePrompt(t *testing.T) {
	rows := []struct {
		rule string
		in   PromptInput
		now  time.Time
		want PromptDecision
	}{
		// Waking only when there is something to read.
		{"nothing pending: nothing to wake for",
			PromptInput{Trigger: TriggerArrival},
			t0, PromptDecision{Action: PromptHold, Reason: PromptNothingPending}},
		{"nothing pending at turn end: no re-prompt for an empty queue",
			PromptInput{Trigger: TriggerTurnEnd, Attempts: []Attempt{delivered(0, PromptKindFirst)}, LastRecvAt: after(time.Minute)},
			after(2 * time.Minute), PromptDecision{Action: PromptHold, Reason: PromptNothingPending}},

		// The first prompt, and coalescing.
		{"no unread prompt: the first message wakes the endpoint, count in the prompt",
			PromptInput{Trigger: TriggerArrival, Pending: 1},
			t0, PromptDecision{Action: PromptSend, Reason: PromptFirst, Kind: PromptKindFirst, Count: 1}},
		{"five messages waiting at once make one prompt that counts all five",
			PromptInput{Trigger: TriggerArrival, Pending: 5},
			t0, PromptDecision{Action: PromptSend, Reason: PromptFirst, Kind: PromptKindFirst, Count: 5}},
		{"busy session: wait for the turn to end instead of prompting",
			PromptInput{Trigger: TriggerArrival, Pending: 2, Busy: true},
			t0, PromptDecision{Action: PromptHold, Reason: PromptSessionBusy}},
		{"unread prompt and a new arrival inside the interval: no second prompt, wake at the end of the interval",
			PromptInput{Trigger: TriggerArrival, Pending: 2, LastArrivalAt: after(time.Minute),
				Attempts: []Attempt{delivered(0, PromptKindFirst)}},
			after(time.Minute), PromptDecision{Action: PromptHold, Reason: PromptUnreadWaiting, RetryAt: after(5 * time.Minute)}},
		{"a recv after the prompt reads it: the next message gets a fresh first prompt",
			PromptInput{Trigger: TriggerArrival, Pending: 1, LastArrivalAt: after(2 * time.Minute), LastRecvAt: after(time.Minute),
				Attempts: []Attempt{delivered(0, PromptKindFirst)}},
			after(2 * time.Minute), PromptDecision{Action: PromptSend, Reason: PromptFirst, Kind: PromptKindFirst, Count: 1}},
		{"a recv at the very instant of the prompt is not after it: still unread",
			PromptInput{Trigger: TriggerArrival, Pending: 1, LastRecvAt: after(0),
				Attempts: []Attempt{delivered(0, PromptKindFirst)}},
			after(time.Minute), PromptDecision{Action: PromptHold, Reason: PromptUnreadWaiting}},

		// Re-prompt at turn end, once.
		{"turn ended with the prompt unread: one re-prompt",
			PromptInput{Trigger: TriggerTurnEnd, Pending: 3, Attempts: []Attempt{delivered(0, PromptKindFirst)}},
			after(time.Minute), PromptDecision{Action: PromptSend, Reason: PromptReprompt, Kind: PromptKindReprompt, Count: 3}},
		{"the re-prompt was used and ignored: no second re-prompt",
			PromptInput{Trigger: TriggerTurnEnd, Pending: 3,
				Attempts: []Attempt{delivered(0, PromptKindFirst), delivered(time.Minute, PromptKindReprompt)}},
			after(2 * time.Minute), PromptDecision{Action: PromptHold, Reason: PromptRepromptSpent}},
		{"a failed re-prompt does not spend the allowance",
			PromptInput{Trigger: TriggerTurnEnd, Pending: 3,
				Attempts: []Attempt{delivered(0, PromptKindFirst),
					{At: after(time.Minute), Outcome: AttemptFailed, Kind: PromptKindReprompt}}},
			after(2 * time.Minute), PromptDecision{Action: PromptSend, Reason: PromptReprompt, Kind: PromptKindReprompt, Count: 3}},
		{"an arrival is not a turn end: no re-prompt on an arrival alone",
			PromptInput{Trigger: TriggerArrival, Pending: 3, Attempts: []Attempt{delivered(0, PromptKindFirst)}},
			after(time.Minute), PromptDecision{Action: PromptHold, Reason: PromptUnreadWaiting}},

		// Renewal by a new message after the minimum interval.
		{"new message after the minimum interval wakes an ignoring endpoint again",
			PromptInput{Trigger: TriggerArrival, Pending: 2, LastArrivalAt: after(20 * time.Minute),
				Attempts: []Attempt{delivered(0, PromptKindFirst), delivered(time.Minute, PromptKindReprompt)}},
			after(20 * time.Minute), PromptDecision{Action: PromptSend, Reason: PromptNewMessage, Kind: PromptKindFirst, Count: 2}},
		{"the minimum interval is inclusive",
			PromptInput{Trigger: TriggerArrival, Pending: 2, LastArrivalAt: after(6 * time.Minute),
				Attempts: []Attempt{delivered(0, PromptKindFirst), delivered(time.Minute, PromptKindReprompt)}},
			after(6 * time.Minute), PromptDecision{Action: PromptSend, Reason: PromptNewMessage, Kind: PromptKindFirst, Count: 2}},
		{"new message just inside the minimum interval does not renew",
			PromptInput{Trigger: TriggerArrival, Pending: 2, LastArrivalAt: after(6*time.Minute - time.Nanosecond),
				Attempts: []Attempt{delivered(0, PromptKindFirst), delivered(time.Minute, PromptKindReprompt)}},
			after(6*time.Minute - time.Nanosecond),
			PromptDecision{Action: PromptHold, Reason: PromptUnreadWaiting, RetryAt: after(6 * time.Minute)}},
		{"arrival inside the interval at turn end with the re-prompt spent: hold with RetryAt",
			PromptInput{Trigger: TriggerTurnEnd, Pending: 2, LastArrivalAt: after(2 * time.Minute),
				Attempts: []Attempt{delivered(0, PromptKindFirst), delivered(time.Minute, PromptKindReprompt)}},
			after(3 * time.Minute),
			PromptDecision{Action: PromptHold, Reason: PromptRepromptSpent, RetryAt: after(6 * time.Minute)}},
		{"no arrival since the last prompt: an unread-prompt hold names no retry",
			PromptInput{Trigger: TriggerArrival, Pending: 2, LastArrivalAt: after(-time.Minute),
				Attempts: []Attempt{delivered(0, PromptKindFirst)}},
			after(time.Minute), PromptDecision{Action: PromptHold, Reason: PromptUnreadWaiting}},
		{"no new message since the last prompt: the interval alone renews nothing",
			PromptInput{Trigger: TriggerTurnEnd, Pending: 1, LastArrivalAt: after(-time.Minute),
				Attempts: []Attempt{delivered(0, PromptKindFirst), delivered(time.Minute, PromptKindReprompt)}},
			after(time.Hour), PromptDecision{Action: PromptHold, Reason: PromptRepromptSpent}},
		{"a message that arrived inside the interval renews at the next turn end once the interval has passed",
			PromptInput{Trigger: TriggerTurnEnd, Pending: 2, LastArrivalAt: after(2 * time.Minute),
				Attempts: []Attempt{delivered(0, PromptKindFirst), delivered(time.Minute, PromptKindReprompt)}},
			after(10 * time.Minute), PromptDecision{Action: PromptSend, Reason: PromptNewMessage, Kind: PromptKindFirst, Count: 2}},
		{"after a renewed first prompt the endpoint again gets one re-prompt",
			PromptInput{Trigger: TriggerTurnEnd, Pending: 2,
				Attempts: []Attempt{delivered(0, PromptKindFirst), delivered(time.Minute, PromptKindReprompt),
					delivered(20*time.Minute, PromptKindFirst)}},
			after(21 * time.Minute), PromptDecision{Action: PromptSend, Reason: PromptReprompt, Kind: PromptKindReprompt, Count: 2}},
		{"busy session: a renewing message waits for the turn to end",
			PromptInput{Trigger: TriggerArrival, Pending: 2, Busy: true, LastArrivalAt: after(20 * time.Minute),
				Attempts: []Attempt{delivered(0, PromptKindFirst)}},
			after(20 * time.Minute), PromptDecision{Action: PromptHold, Reason: PromptSessionBusy}},

		// Failed and unknown attempts only affect whether to wake again.
		{"a failed prompt is not an unread prompt: wake again",
			PromptInput{Trigger: TriggerArrival, Pending: 1,
				Attempts: []Attempt{{At: after(0), Outcome: AttemptFailed, Kind: PromptKindFirst}}},
			after(time.Second), PromptDecision{Action: PromptSend, Reason: PromptFirst, Kind: PromptKindFirst, Count: 1}},
		{"an unknown prompt is not an unread prompt: wake again",
			PromptInput{Trigger: TriggerArrival, Pending: 1,
				Attempts: []Attempt{{At: after(0), Outcome: AttemptUnknown, Kind: PromptKindFirst}}},
			after(time.Second), PromptDecision{Action: PromptSend, Reason: PromptFirst, Kind: PromptKindFirst, Count: 1}},

		// Hourly cap.
		{"cap reached by delivered prompts: hold until the oldest leaves the hour",
			PromptInput{Trigger: TriggerArrival, Pending: 1, LastRecvAt: after(30 * time.Minute),
				Attempts: []Attempt{
					delivered(10*time.Minute, PromptKindFirst), delivered(11*time.Minute, PromptKindFirst),
					delivered(12*time.Minute, PromptKindFirst), delivered(13*time.Minute, PromptKindFirst),
					delivered(14*time.Minute, PromptKindFirst), delivered(15*time.Minute, PromptKindFirst)}},
			after(40 * time.Minute), PromptDecision{Action: PromptHold, Reason: PromptHourlyCap, RetryAt: after(70 * time.Minute)}},
		{"prompts older than an hour do not count toward the cap",
			PromptInput{Trigger: TriggerArrival, Pending: 1, LastRecvAt: after(30 * time.Minute),
				Attempts: []Attempt{
					delivered(1*time.Minute, PromptKindFirst), delivered(2*time.Minute, PromptKindFirst),
					delivered(3*time.Minute, PromptKindFirst), delivered(4*time.Minute, PromptKindFirst),
					delivered(5*time.Minute, PromptKindFirst), delivered(6*time.Minute, PromptKindFirst)}},
			after(66 * time.Minute), PromptDecision{Action: PromptSend, Reason: PromptFirst, Kind: PromptKindFirst, Count: 1}},
		{"failed attempts do not count toward the cap: a dead adapter does not delay the wake-up after it recovers",
			PromptInput{Trigger: TriggerArrival, Pending: 1, Attempts: []Attempt{
				{At: after(1 * time.Minute), Outcome: AttemptFailed, Kind: PromptKindFirst},
				{At: after(2 * time.Minute), Outcome: AttemptFailed, Kind: PromptKindFirst},
				{At: after(3 * time.Minute), Outcome: AttemptFailed, Kind: PromptKindFirst},
				{At: after(4 * time.Minute), Outcome: AttemptFailed, Kind: PromptKindFirst},
				{At: after(5 * time.Minute), Outcome: AttemptFailed, Kind: PromptKindFirst},
				{At: after(6 * time.Minute), Outcome: AttemptFailed, Kind: PromptKindFirst}}},
			after(10 * time.Minute), PromptDecision{Action: PromptSend, Reason: PromptFirst, Kind: PromptKindFirst, Count: 1}},
		{"unknown attempts count toward the cap: they may have woken the agent",
			PromptInput{Trigger: TriggerArrival, Pending: 1, Attempts: []Attempt{
				{At: after(1 * time.Minute), Outcome: AttemptUnknown, Kind: PromptKindFirst},
				{At: after(2 * time.Minute), Outcome: AttemptUnknown, Kind: PromptKindFirst},
				{At: after(3 * time.Minute), Outcome: AttemptUnknown, Kind: PromptKindFirst},
				{At: after(4 * time.Minute), Outcome: AttemptUnknown, Kind: PromptKindFirst},
				{At: after(5 * time.Minute), Outcome: AttemptUnknown, Kind: PromptKindFirst},
				{At: after(6 * time.Minute), Outcome: AttemptUnknown, Kind: PromptKindFirst}}},
			after(10 * time.Minute), PromptDecision{Action: PromptHold, Reason: PromptHourlyCap, RetryAt: after(61 * time.Minute)}},
		{"the cap holds back a re-prompt as well",
			PromptInput{Trigger: TriggerTurnEnd, Pending: 1, Attempts: []Attempt{
				delivered(1*time.Minute, PromptKindFirst), delivered(2*time.Minute, PromptKindFirst),
				delivered(3*time.Minute, PromptKindFirst), delivered(4*time.Minute, PromptKindFirst),
				delivered(5*time.Minute, PromptKindFirst), delivered(6*time.Minute, PromptKindFirst)}},
			after(10 * time.Minute), PromptDecision{Action: PromptHold, Reason: PromptHourlyCap, RetryAt: after(61 * time.Minute)}},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			got, err := DecidePrompt(r.in, testPromptParams, r.now)
			if err != nil {
				t.Fatal(err)
			}
			if got != r.want {
				t.Fatalf("got %+v, want %+v", got, r.want)
			}
		})
	}
}

// promptSim plays an endpoint through DecidePrompt the way a daemon would: it
// records attempts, reads and arrivals, and counts prompts that reached the
// session.
type promptSim struct {
	t       *testing.T
	now     time.Time
	pending int
	arrival time.Time
	recv    time.Time
	busy    bool
	history []Attempt
	woken   int
}

func (s *promptSim) decide(trigger PromptTrigger) PromptDecision {
	s.t.Helper()
	d, err := DecidePrompt(PromptInput{Trigger: trigger, Pending: s.pending, LastArrivalAt: s.arrival,
		LastRecvAt: s.recv, Busy: s.busy, Attempts: s.history}, PromptParams{MaxPerHour: 20, MinInterval: 5 * time.Minute}, s.now)
	if err != nil {
		s.t.Fatal(err)
	}
	return d
}

// send records the outcome of a prompt the decision asked for.
func (s *promptSim) send(d PromptDecision, outcome AttemptOutcome) {
	s.t.Helper()
	if d.Action != PromptSend {
		s.t.Fatalf("expected a prompt, got %+v", d)
	}
	s.history = append(s.history, Attempt{At: s.now, Outcome: outcome, Kind: d.Kind})
	if outcome == AttemptDelivered {
		s.woken++
		s.busy = true
	}
}

func (s *promptSim) arrive(n int) {
	s.pending += n
	s.arrival = s.now
}

func (s *promptSim) advance(d time.Duration) { s.now = s.now.Add(d) }

func (s *promptSim) endTurn() { s.busy = false }

func TestPromptScenarioIgnoredPromptWakesTwiceThenRenewsOnNewMessage(t *testing.T) {
	s := &promptSim{t: t, now: t0}
	s.arrive(1)
	s.send(s.decide(TriggerArrival), AttemptDelivered)

	s.advance(30 * time.Second)
	s.endTurn() // the agent ignored the prompt
	d := s.decide(TriggerTurnEnd)
	if d.Reason != PromptReprompt {
		t.Fatalf("first turn end: got %+v, want a re-prompt", d)
	}
	s.send(d, AttemptDelivered)

	s.advance(30 * time.Second)
	s.endTurn() // ignored again
	if d := s.decide(TriggerTurnEnd); d.Action != PromptHold || d.Reason != PromptRepromptSpent {
		t.Fatalf("second turn end: got %+v, want hold with the re-prompt spent", d)
	}
	if s.woken != 2 {
		t.Fatalf("woken = %d, want the prompt plus one re-prompt", s.woken)
	}

	s.advance(10 * time.Minute)
	s.arrive(1)
	d = s.decide(TriggerArrival)
	if d.Reason != PromptNewMessage {
		t.Fatalf("new message after the interval: got %+v, want a renewed prompt", d)
	}
	s.send(d, AttemptDelivered)
	if s.woken != 3 || d.Count != 2 {
		t.Fatalf("woken = %d, count = %d, want a third wake-up announcing both messages", s.woken, d.Count)
	}
}

// A message that arrives inside the minimum interval after an ignored prompt
// is woken for at RetryAt, and the caller needs no other event to get there.
func TestPromptScenarioArrivalInsideTheIntervalIsWokenAtRetryAt(t *testing.T) {
	s := &promptSim{t: t, now: t0}
	s.arrive(1)
	s.send(s.decide(TriggerArrival), AttemptDelivered)
	s.advance(30 * time.Second)
	s.endTurn()
	s.send(s.decide(TriggerTurnEnd), AttemptDelivered) // the one re-prompt
	s.advance(30 * time.Second)
	s.endTurn()
	if d := s.decide(TriggerTurnEnd); d.Reason != PromptRepromptSpent {
		t.Fatalf("got %+v, want the re-prompt spent", d)
	}

	// A message arrives a minute after the re-prompt, inside the interval.
	s.advance(time.Minute)
	s.arrive(1)
	d := s.decide(TriggerArrival)
	if d.Action != PromptHold || d.RetryAt.IsZero() {
		t.Fatalf("got %+v, want a hold that names when to look again", d)
	}
	if want := after(30 * time.Second).Add(5 * time.Minute); !d.RetryAt.Equal(want) {
		t.Fatalf("RetryAt = %v, want the end of the interval after the re-prompt, %v", d.RetryAt, want)
	}

	// Nothing else happens: the only later evaluation is the one at RetryAt.
	s.now = d.RetryAt
	d = s.decide(TriggerArrival)
	if d.Action != PromptSend || d.Reason != PromptNewMessage || d.Count != 2 {
		t.Fatalf("at RetryAt: got %+v, want a renewed prompt announcing both messages", d)
	}
}

func TestPromptScenarioFiveMessagesAtOnceStartOneTurn(t *testing.T) {
	s := &promptSim{t: t, now: t0}
	s.arrive(5)
	d := s.decide(TriggerArrival)
	if d.Count != 5 {
		t.Fatalf("count = %d, want 5", d.Count)
	}
	s.send(d, AttemptDelivered)
	// The other arrivals of the burst are evaluated while that turn runs.
	for i := 0; i < 4; i++ {
		s.advance(100 * time.Millisecond)
		if d := s.decide(TriggerArrival); d.Action != PromptHold {
			t.Fatalf("arrival %d during the turn: got %+v, want hold", i+2, d)
		}
	}
	if s.woken != 1 {
		t.Fatalf("woken = %d, want 1", s.woken)
	}
}

func TestPromptScenarioDeliveryKilledMidWayChangesNothingExternal(t *testing.T) {
	s := &promptSim{t: t, now: t0}
	s.arrive(1)
	s.send(s.decide(TriggerArrival), AttemptUnknown) // the native process was killed while sending

	// The message stays queued; the only effect is that the endpoint is woken again.
	s.advance(time.Second)
	if d := s.decide(TriggerArrival); d.Action != PromptSend || d.Reason != PromptFirst {
		t.Fatalf("got %+v, want a new first prompt", d)
	}
	v := AssessBlocked(BlockedInput{Attempts: s.history, OldestPendingAt: t0}, DefaultBlockedParams(), s.now)
	if v.Status != NotBlocked {
		t.Fatalf("an unknown prompt outcome must not be reported: %+v", v)
	}
}

func TestPromptEnumerationsAreNeverZero(t *testing.T) {
	for _, v := range []int{int(AttemptFailed), int(AttemptUnknown), int(AttemptDelivered),
		int(PromptKindFirst), int(PromptKindReprompt), int(TriggerArrival), int(TriggerTurnEnd),
		int(PromptHold), int(PromptSend)} {
		if v == 0 {
			t.Fatal("an enumeration value equals the zero value, so an unset field would pass for it")
		}
	}
}
