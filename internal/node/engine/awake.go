package engine

import "time"

// awakeStepCap is the most one step between two readings of the clock adds to the
// awake time, derived from the periodic tasks of the engine: every one of them keeps
// an entry in the schedule, so the loop wakes at least once in the period of the
// clock state's task, and two readings further apart than a little over that (half
// as much again) mean the machine slept; the time it slept is not time the node was
// awake to hear from anybody. Without a task of the clock state it is the longest
// period of the other periodic tasks, and with no periodic task at all there is no
// cap (zero).
func (e *Engine) awakeStepCap() time.Duration {
	if t := e.periodic[e.clockTask]; t != nil {
		return t.every * 3 / 2
	}
	var longest time.Duration
	for _, t := range e.periodic {
		longest = max(longest, t.every)
	}
	return longest
}

// awakeTime is the time the node has been awake since it started, as far as the
// readings of the wall clock show. The clock gives UTC wall time without a monotonic
// reading, so a machine that sleeps looks like a jump forward; counting each step
// between readings at most stepCap keeps a sleep from counting as time passed. With
// no cap (zero) every step counts whole. A reading behind the last one (the clock
// was set back) adds nothing, so awake time is a lower bound of the wall time since
// the start, unless the wall clock was set back.
//
// A rule that is about the absence of somebody, and not about a deadline fixed on the
// wall clock, measures it as the difference of two values of total. It is read and
// advanced by the loop alone.
type awakeTime struct {
	total   time.Duration
	last    time.Time
	stepCap time.Duration
}

// begin sets the reading the first step is counted from and the cap of a step.
func (a *awakeTime) begin(now time.Time, stepCap time.Duration) {
	a.last, a.stepCap = now, stepCap
}

// advance counts the step from the last reading to now.
func (a *awakeTime) advance(now time.Time) {
	if d := now.Sub(a.last); d > 0 {
		if a.stepCap > 0 {
			d = min(d, a.stepCap)
		}
		a.total += d
	}
	a.last = now
}
