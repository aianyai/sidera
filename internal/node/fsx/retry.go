package fsx

import (
	"time"

	"github.com/aianyai/sidera/internal/node/clock"
)

// Bounded retry with exponential backoff for primitives that fail transiently
// while another process (an antivirus scanner, an indexer, a reader that has
// the file open) holds a handle. Only the Windows primitives use it, but the
// logic is platform independent and tested everywhere.
//
// The backoff is modelled on the Go toolchain's own retry loop
// (cmd/internal/robustio), which sleeps for exponentially growing intervals.
// This one doubles deterministically from one millisecond, with no jitter, the
// last sleep shortened, and gives up once two seconds have been slept in total:
// a budget of accumulated sleep rather than a count of attempts.

const (
	retryFirstSleep = time.Millisecond
	retryBudget     = 2 * time.Second
)

// retry runs f until it succeeds, reports that retrying cannot help, or the
// sleep budget is spent. It returns the last error. sleep is the time seam.
func retry(sleep func(time.Duration), f func() (mayRetry bool, err error)) error {
	var slept time.Duration
	next := retryFirstSleep
	for {
		mayRetry, err := f()
		if err == nil || !mayRetry {
			return err
		}
		if slept >= retryBudget {
			return err
		}
		d := min(next, retryBudget-slept)
		sleep(d)
		slept += d
		next *= 2
	}
}

// sleepSystem sleeps for real, on a timer of the system clock. It is the only
// place in this package that waits, so that another clock can replace it in one
// place.
func sleepSystem(d time.Duration) {
	t := clock.System().NewTimer(d)
	<-t.C()
}
