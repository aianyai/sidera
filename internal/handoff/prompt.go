package handoff

import (
	"errors"
	"fmt"
	"time"
)

// MaxReprompts is how many times an endpoint is prompted again, at the end of a
// turn, for the same unread prompt. The first prompt is not counted. One extra
// nudge covers an agent that missed the first one; more would turn a prompt the
// agent has decided to ignore into a loop that burns turns.
const MaxReprompts = 1

// hourlyWindow is the span over which PromptParams.MaxPerHour is counted.
const hourlyWindow = time.Hour

// Decided prompt values. Each has its reason with it, and DefaultPromptParams
// returns them together.
const (
	// DefaultMaxConcurrentTurns is the node cap on turns that prompts started.
	// With the queue time an endpoint's messages wait before they expire (30
	// minutes), a cap of 8 gives a turn to every endpoint of 16 that wake at
	// once when turns last 15 minutes, and of 48 when they last 5 minutes. A
	// smaller cap would let the node's own throttling expire messages; no cap
	// would let one burst start a turn in every session of the node.
	DefaultMaxConcurrentTurns = 8
	// DefaultMaxPerHour is the cap on prompts per endpoint and hour. A turn
	// that lasts a minute or more cannot reach 60 in an hour, so the cap stops
	// only a loop of wake-ups that do next to nothing. A lower cap would be hit
	// by quick question and answer exchanges and make a message wait up to the
	// queue time; a higher one would protect nothing.
	DefaultMaxPerHour = 60
	// DefaultMinInterval is how long a newly arrived message waits after the
	// last prompt before it wakes the endpoint again. An agent that answers
	// normally starts reading within about a minute, so two minutes adds no
	// wake-up for it, merges a run of messages into one wake-up, and leaves 28
	// of the 30 queue minutes after the last one. An endpoint that never reads
	// is woken about 30 times an hour at most, below DefaultMaxPerHour.
	DefaultMinInterval = 2 * time.Minute
	// DefaultRetryAfterFailure is how long after a failed prompt the next one
	// may be sent. A prompt fails mostly because the adapter or the native
	// service is not ready, for instance an adapter whose connection is being
	// put in service; a minute is long enough for that to pass and short
	// enough to cost a waiting message little of its 30 queue minutes. Sent
	// at once, the retry would fail again and a lasting outage would turn into
	// a loop of failures.
	DefaultRetryAfterFailure = time.Minute
)

// PromptParams holds the values of the prompt rules. DecidePrompt and
// DecideNodePrompts reject a value that is not positive: an unset cap is never
// read as no cap. DefaultPromptParams returns the decided values.
type PromptParams struct {
	// MaxPerHour caps the prompts that may have woken an endpoint within any
	// one hour: delivered attempts and attempts with an unknown outcome.
	MaxPerHour int
	// MinInterval is how long after the last delivered prompt a newly arrived
	// message must wait before it renews the endpoint's re-prompt allowance
	// and wakes the endpoint again.
	MinInterval time.Duration
	// MaxConcurrentTurns caps the turns that prompts have started and that are
	// still running on one node, across all endpoints. Only DecideNodePrompts
	// reads it; DecidePrompt, which judges one endpoint, does not.
	MaxConcurrentTurns int
	// RetryAfterFailure is how long after a failed attempt a prompt may be
	// sent again.
	RetryAfterFailure time.Duration
}

// DefaultPromptParams returns the decided values.
func DefaultPromptParams() PromptParams {
	return PromptParams{
		MaxPerHour:         DefaultMaxPerHour,
		MinInterval:        DefaultMinInterval,
		MaxConcurrentTurns: DefaultMaxConcurrentTurns,
		RetryAfterFailure:  DefaultRetryAfterFailure,
	}
}

// ErrInvalidParams is wrapped by errors about parameters that are not positive.
var ErrInvalidParams = errors.New("handoff: invalid parameters")

// Validate reports whether every parameter is usable.
func (p PromptParams) Validate() error {
	if p.MaxPerHour <= 0 {
		return fmt.Errorf("%w: MaxPerHour must be positive, got %d", ErrInvalidParams, p.MaxPerHour)
	}
	if p.MinInterval <= 0 {
		return fmt.Errorf("%w: MinInterval must be positive, got %v", ErrInvalidParams, p.MinInterval)
	}
	if p.MaxConcurrentTurns <= 0 {
		return fmt.Errorf("%w: MaxConcurrentTurns must be positive, got %d", ErrInvalidParams, p.MaxConcurrentTurns)
	}
	if p.RetryAfterFailure <= 0 {
		return fmt.Errorf("%w: RetryAfterFailure must be positive, got %v", ErrInvalidParams, p.RetryAfterFailure)
	}
	return nil
}

// PromptText is the text of the prompt sent into a native session. It carries
// no message body: the body and the sender reach the model only through recv,
// so a sender cannot put words into the session by way of the prompt. The
// wording is provisional. count is the number of waiting messages and must be
// at least 1; DecidePrompt never asks for a prompt otherwise. A session created
// without an initial message has no message to count: its first prompt is
// NewSessionPromptText.
func PromptText(count int) string {
	if count == 1 {
		return "You have 1 new message. Read it with recv."
	}
	return fmt.Sprintf("You have %d new messages. Read them with recv.", count)
}

// NewSessionPromptText is the prompt that starts a session created without an
// initial message. It tells the agent how messages will reach it and carries no
// message body, like PromptText. The wording is provisional.
//
// It is not a prompt Attempt: the caller does not record it in
// PromptInput.Attempts, and it does not count toward PromptParams.MaxPerHour.
// It announces no unread message, so a recorded attempt would be read by
// DecidePrompt as a delivered prompt the endpoint has not answered, and would
// hold back the first real message for MinInterval. There is one per created
// session, which authorization and the rate of session creation already bound.
const NewSessionPromptText = "This is a new session. " +
	"When you are told that new messages have arrived, read them with recv."

// AttemptOutcome is what is known about one attempt to send a prompt into a
// session.
type AttemptOutcome int

const (
	// AttemptFailed means the prompt certainly did not reach the session.
	AttemptFailed AttemptOutcome = iota + 1
	// AttemptUnknown means it is not known whether the prompt reached the
	// session, for example because the adapter died while sending it.
	AttemptUnknown
	// AttemptDelivered means the prompt reached the session.
	AttemptDelivered
)

// PromptKind says which role a prompt played for the unread prompt it belongs
// to.
type PromptKind int

const (
	// PromptKindFirst starts a new chain of prompts: the endpoint had no unread
	// prompt, or its re-prompt allowance had been renewed.
	PromptKindFirst PromptKind = iota + 1
	// PromptKindReprompt repeats a prompt the endpoint left unread.
	PromptKindReprompt
)

// Attempt is one recorded attempt to send a prompt into the session of an
// endpoint. Callers record every attempt, and the Kind that DecidePrompt chose
// for it.
type Attempt struct {
	At      time.Time
	Outcome AttemptOutcome
	Kind    PromptKind
}

// PromptTrigger is the event that makes the caller ask whether to prompt.
type PromptTrigger int

const (
	// TriggerArrival: a message arrived for the endpoint.
	TriggerArrival PromptTrigger = iota + 1
	// TriggerTurnEnd: a turn of the endpoint's session ended.
	TriggerTurnEnd
)

// PromptInput is everything DecidePrompt reads about one endpoint.
type PromptInput struct {
	Trigger PromptTrigger
	// Pending is the number of messages queued for the endpoint that still
	// have something not handed out: a message handed out in parts counts until
	// its last part has been. A message whose recv-stage authorization is
	// currently Suspend is not a waiting message and is not counted: suspension
	// means no lease, no prompt and no feedback.
	Pending int
	// LastArrivalAt is when the newest waiting message arrived; a suspended
	// message is not waiting (see Pending).
	LastArrivalAt time.Time
	// LastRecvAt is when the endpoint last called recv; zero if never.
	LastRecvAt time.Time
	// Busy reports that the session must not be prompted now: a turn is
	// running, or the caller holds it. The caller asks again when that ends.
	Busy bool
	// Attempts is the endpoint's prompt attempts in ascending time order.
	// Attempts older than the hourly window and older than the last first
	// prompt that was read may be dropped.
	Attempts []Attempt
}

// PromptAction is what to do about a prompt.
type PromptAction int

const (
	// PromptHold: send nothing now.
	PromptHold PromptAction = iota + 1
	// PromptSend: send a prompt now.
	PromptSend
)

// PromptReason says why DecidePrompt chose its action.
type PromptReason int

const (
	// PromptNothingPending: no message waits, so there is nothing to wake for.
	PromptNothingPending PromptReason = iota + 1
	// PromptSessionBusy: the session must not be prompted now. The caller asks
	// again when the turn or hold ends.
	PromptSessionBusy
	// PromptFirst: the endpoint has no unread prompt.
	PromptFirst
	// PromptReprompt: the endpoint left the previous prompt unread and its
	// turn ended; this is the one repeat.
	PromptReprompt
	// PromptNewMessage: a message arrived after the last prompt and the
	// minimum interval has passed, so the re-prompt allowance is renewed.
	PromptNewMessage
	// PromptUnreadWaiting: a prompt is still unread and nothing allows another
	// yet; the next turn end or a later arrival is the next chance.
	PromptUnreadWaiting
	// PromptRepromptSpent: the allowance of repeats is used up.
	PromptRepromptSpent
	// PromptHourlyCap: the endpoint has been woken as often as the hourly cap
	// allows.
	PromptHourlyCap
	// PromptNodeFull: the endpoint may be prompted, but the node already runs
	// as many prompt-started turns as it allows. Only DecideNodePrompts returns
	// it. It is not an attempt and no failure: the caller records nothing. When
	// the endpoint is evaluated again is described at DecideNodePrompts.
	PromptNodeFull
	// PromptRetryAfterFailure: a prompt would be sent, but the latest attempt
	// failed less than RetryAfterFailure ago. RetryAt is when it may be sent.
	PromptRetryAfterFailure
)

// PromptDecision is the verdict of DecidePrompt.
type PromptDecision struct {
	Action PromptAction
	Reason PromptReason
	// Kind is how the caller records a sent prompt. Set only when Action is
	// PromptSend.
	Kind PromptKind
	// Count is the number of waiting messages to put in the prompt text. Set
	// only when Action is PromptSend.
	Count int
	// RetryAt is when to evaluate again, for the one case in which no event of
	// the endpoint's own will prompt that evaluation. Set for PromptHourlyCap,
	// where it is when the cap frees a slot, for PromptRetryAfterFailure, where
	// it is when the retry of a failed prompt is due, and for PromptUnreadWaiting and
	// PromptRepromptSpent when a message newer than the last delivered prompt
	// is waiting for the minimum interval to pass, where it is when that
	// happens. The caller evaluates again at RetryAt with the same Trigger as
	// the evaluation that returned it. Zero otherwise.
	RetryAt time.Time
}

// DecidePrompt decides whether to send a prompt into the endpoint's session.
//
// A prompt is a wake-up signal without a body, so the rules only keep it from
// waking an agent that has already been told, or too often:
//
//   - An endpoint has at most one unread prompt. A prompt is unread until the
//     endpoint calls recv after it was delivered.
//   - Messages that arrive while a prompt is unread, or while the session is
//     busy, do not add prompts: the next prompt counts them all, so five
//     messages arriving together start one turn.
//   - When a turn ends with the prompt still unread, the endpoint is prompted
//     again, MaxReprompts times at most.
//   - A message arriving after the last delivered prompt renews that allowance
//     and wakes the endpoint, once params.MinInterval has passed since that
//     prompt. Without this an endpoint that ignored one prompt would never be
//     woken again.
//   - At most params.MaxPerHour prompts that may have reached the session are
//     sent per hour.
//
// Three holds name when to evaluate again (PromptDecision.RetryAt): a cap that
// frees a slot, a message that waits for the minimum interval, and the retry of
// a failed prompt. No event of the endpoint marks any of these moments.
//
// A failed attempt is invisible to all of these except that it leaves the
// endpoint unprompted and that the next prompt waits for params.RetryAfterFailure
// after it: it is not an unread prompt, spends no allowance and does not count
// toward the cap, because it woke nobody. A prompt that would be sent is held
// only when the newest attempt of all is the failed one; any other hold keeps
// its own reason. An attempt with an
// unknown outcome counts toward the cap, since it may have woken the agent, and
// is otherwise treated like a failed one.
func DecidePrompt(in PromptInput, p PromptParams, now time.Time) (PromptDecision, error) {
	if err := p.Validate(); err != nil {
		return PromptDecision{}, err
	}
	if in.Pending <= 0 {
		return hold(PromptNothingPending), nil
	}
	if in.Busy {
		return hold(PromptSessionBusy), nil
	}

	last, delivered := lastDelivered(in.Attempts)
	unread := delivered && !in.LastRecvAt.After(last.At)

	var reason PromptReason
	var kind PromptKind
	switch {
	case !unread:
		reason, kind = PromptFirst, PromptKindFirst
	case in.LastArrivalAt.After(last.At) && now.Sub(last.At) >= p.MinInterval:
		reason, kind = PromptNewMessage, PromptKindFirst
	case in.Trigger != TriggerTurnEnd:
		return holdForInterval(PromptUnreadWaiting, in, last, p), nil
	case repromptsSinceFirst(in.Attempts) >= MaxReprompts:
		return holdForInterval(PromptRepromptSpent, in, last, p), nil
	default:
		reason, kind = PromptReprompt, PromptKindReprompt
	}

	if oldest, capped := hourlyCap(in.Attempts, p.MaxPerHour, now); capped {
		d := hold(PromptHourlyCap)
		d.RetryAt = oldest.Add(hourlyWindow)
		return d, nil
	}
	if n := len(in.Attempts); n > 0 && in.Attempts[n-1].Outcome == AttemptFailed {
		if retryAt := in.Attempts[n-1].At.Add(p.RetryAfterFailure); now.Before(retryAt) {
			d := hold(PromptRetryAfterFailure)
			d.RetryAt = retryAt
			return d, nil
		}
	}
	return PromptDecision{Action: PromptSend, Reason: reason, Kind: kind, Count: in.Pending}, nil
}

func hold(r PromptReason) PromptDecision {
	return PromptDecision{Action: PromptHold, Reason: r}
}

// holdForInterval is a hold while a prompt is unread. A message newer than the
// last delivered prompt is not woken for yet, because the minimum interval has
// not passed; nothing else is due to bring the caller back when it does, so the
// decision names that instant.
func holdForInterval(r PromptReason, in PromptInput, last Attempt, p PromptParams) PromptDecision {
	d := hold(r)
	if in.LastArrivalAt.After(last.At) {
		d.RetryAt = last.At.Add(p.MinInterval)
	}
	return d
}

// lastDelivered returns the newest delivered attempt.
func lastDelivered(attempts []Attempt) (Attempt, bool) {
	for i := len(attempts) - 1; i >= 0; i-- {
		if attempts[i].Outcome == AttemptDelivered {
			return attempts[i], true
		}
	}
	return Attempt{}, false
}

// repromptsSinceFirst counts the delivered re-prompts that follow the newest
// delivered first prompt.
func repromptsSinceFirst(attempts []Attempt) int {
	n := 0
	for i := len(attempts) - 1; i >= 0; i-- {
		a := attempts[i]
		if a.Outcome != AttemptDelivered {
			continue
		}
		if a.Kind == PromptKindFirst {
			break
		}
		n++
	}
	return n
}

// hourlyCap reports whether limit attempts that may have woken the endpoint fall
// within the hour before now, and the oldest of them.
func hourlyCap(attempts []Attempt, limit int, now time.Time) (oldest time.Time, capped bool) {
	count := 0
	for _, a := range attempts {
		if a.Outcome == AttemptFailed || !a.At.After(now.Add(-hourlyWindow)) {
			continue
		}
		if count == 0 {
			oldest = a.At
		}
		count++
	}
	return oldest, count >= limit
}
