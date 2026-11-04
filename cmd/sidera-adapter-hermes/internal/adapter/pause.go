package adapter

import (
	"context"
	"time"
)

// backoff is a pause that doubles from min to max with every failure in a row
// and starts over after a success.
type backoff struct {
	min, max, cur time.Duration
}

func newBackoff(min, max time.Duration) *backoff { return &backoff{min: min, max: max} }

// next returns how long to pause after one more failure.
func (b *backoff) next() time.Duration {
	if b.cur == 0 {
		b.cur = b.min
	} else {
		b.cur = min(2*b.cur, b.max)
	}
	return b.cur
}

func (b *backoff) reset() { b.cur = 0 }

// sleep pauses for d and reports false when ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// timing holds the intervals of the loops. The values below are the ones the
// adapter runs with; tests shorten them.
type timing struct {
	// receivePoll is how often every endpoint is read although no notification
	// said it holds a message: a notification is best effort and may be lost.
	receivePoll time.Duration
	// idleCheck is how often the endpoints are looked at for idle retirement.
	idleCheck time.Duration
	// retryMin and retryMax bound the pause of a loop whose Hermes is unusable or
	// whose handler asked to retry. The pause grows with every failure in a row
	// and stays well under the lease of a message, which is 15 minutes at most.
	retryMin, retryMax time.Duration
	// registerMin and registerMax bound the pause between attempts to register an
	// endpoint while the daemon is not running yet.
	registerMin, registerMax time.Duration
}

var defaultTiming = timing{
	receivePoll: 30 * time.Second,
	idleCheck:   10 * time.Minute,
	retryMin:    time.Second,
	retryMax:    2 * time.Minute,
	registerMin: 200 * time.Millisecond,
	registerMax: 5 * time.Second,
}
