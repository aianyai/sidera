package handoff

import "time"

// TrimAttempts drops the attempts of an endpoint that no rule reads any more, so
// that the record the caller keeps does not grow with every prompt. It keeps, in
// order:
//
//   - every attempt within the hour of PromptParams.MaxPerHour before now;
//   - the newest delivered first prompt and the delivered attempts after it, however
//     old: they decide whether the endpoint has an unread prompt and how many repeats
//     are spent;
//   - the earliest failure of the current failing run, the failed attempts since the
//     newest delivered one, however old: AssessBlocked measures the run from it.
//
// Failed and unknown attempts older than the hour that are none of these go. The
// input is not modified.
func TrimAttempts(attempts []Attempt, now time.Time) []Attempt {
	chain := -1 // index of the newest delivered first prompt; -1 keeps every delivered attempt
	lastDeliveredAt := -1
	for i, a := range attempts {
		if a.Outcome != AttemptDelivered {
			continue
		}
		lastDeliveredAt = i
		if a.Kind == PromptKindFirst {
			chain = i
		}
	}
	runStart := -1 // index of the earliest failure after the newest delivered attempt
	for i := len(attempts) - 1; i > lastDeliveredAt; i-- {
		if attempts[i].Outcome == AttemptFailed {
			runStart = i
		}
	}
	var out []Attempt
	for i, a := range attempts {
		recent := a.At.After(now.Add(-hourlyWindow))
		inChain := a.Outcome == AttemptDelivered && i >= chain
		if recent || inChain || i == runStart {
			out = append(out, a)
		}
	}
	return out
}
