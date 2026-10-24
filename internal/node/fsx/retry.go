package fsx

import (
	"context"
	"time"
)

// Bounded retry with exponential backoff for primitives that fail transiently
// while another process (an antivirus scanner, an indexer, a reader that has
// the file open) holds a handle. Only the Windows primitives use it, but the
// logic is platform independent and tested everywhere.
//
// The backoff follows the Go toolchain's own retry loop: sleeps of 1, 2, 4, ...
// milliseconds, the last one shortened, until two seconds have been slept in
// total.

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

// sleepSystem sleeps for real. It is the only place in this package that waits,
// so that a system clock can replace it in one place. It waits on a context
// deadline rather than reading or sleeping on the time package's clock.
func sleepSystem(d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	<-ctx.Done()
	cancel()
}
