package handoff

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
)

func TestDefaultPromptParamsValidate(t *testing.T) {
	if err := DefaultPromptParams().Validate(); err != nil {
		t.Fatalf("the defaults fail their own Validate: %v", err)
	}
}

func TestDefaultPromptParamsValues(t *testing.T) {
	p := DefaultPromptParams()
	rows := []struct {
		rule string
		got  any
		want any
	}{
		{"node cap is 8 turns so that a 30-minute queue releases 16 waiting endpoints at 15-minute turns", p.MaxConcurrentTurns, 8},
		{"endpoint cap is 60 prompts an hour so that turns of a minute or more never reach it", p.MaxPerHour, 60},
		{"re-prompt interval is 2 minutes so that a late arrival wakes the endpoint well before the backlog expires", p.MinInterval, 2 * time.Minute},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if r.got != r.want {
				t.Fatalf("got %v, want %v", r.got, r.want)
			}
		})
	}
}

// waitersReleasedInTime counts, with a plain loop, the endpoints that wait at
// the same moment and are all given a turn before queueTime has run out. The
// k-th waiter, earliest first, starts after (k-1)/slots full turns, and a turn
// has to have started a minute before the backlog expires for recv to lease
// the message in time.
func waitersReleasedInTime(slots int, turn, queueTime time.Duration) int {
	n := 0
	for k := 1; ; k++ {
		start := time.Duration((k-1)/slots) * turn
		if start > queueTime-time.Minute {
			return n
		}
		n = k
	}
}

func TestDefaultPromptParamsReasons(t *testing.T) {
	p := DefaultPromptParams()
	queue := delivery.DefaultEndpointMaxQueue
	rows := []struct {
		rule string
		ok   bool
	}{
		{"fifteen-minute turns: the node cap releases 16 simultaneous waiters in the queue time",
			waitersReleasedInTime(p.MaxConcurrentTurns, 15*time.Minute, queue) == 16},
		{"ten-minute turns: the node cap releases 24 simultaneous waiters in the queue time",
			waitersReleasedInTime(p.MaxConcurrentTurns, 10*time.Minute, queue) == 24},
		{"five-minute turns: the node cap releases 48 simultaneous waiters in the queue time",
			waitersReleasedInTime(p.MaxConcurrentTurns, 5*time.Minute, queue) == 48},
		{"a turn of one minute or more at most fills the hourly cap, never exceeds it",
			int(time.Hour/time.Minute) <= p.MaxPerHour},
		{"an endpoint that never reads is woken at most once per interval, below the hourly cap",
			int(time.Hour/p.MinInterval) < p.MaxPerHour},
		{"the re-prompt interval leaves most of the queue time after the last wake-up",
			queue-p.MinInterval >= 28*time.Minute},
		{"the re-prompt interval equals the time after which a target counts as unreachable",
			p.MinInterval == delivery.UnreachableHintAfter},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if !r.ok {
				t.Fatalf("does not hold for %+v", p)
			}
		})
	}
}
