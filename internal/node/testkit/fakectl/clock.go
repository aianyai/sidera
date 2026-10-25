package fakectl

import (
	"sync/atomic"
	"time"
)

// Clock is the clock of a fake process. It has no source of its own: it reads the
// time the fixture last set over the control channel, so the adapters and
// runtimes of a test report times from the clock the daemon runs on.
type Clock struct {
	ms atomic.Int64
}

// NewClock returns a clock reading startMS, in Unix milliseconds.
func NewClock(startMS int64) *Clock {
	c := &Clock{}
	c.ms.Store(startMS)
	return c
}

// Now returns the current test time in UTC.
func (c *Clock) Now() time.Time { return time.UnixMilli(c.ms.Load()).UTC() }

// SetMS sets the time. It may go backwards.
func (c *Clock) SetMS(ms int64) { c.ms.Store(ms) }
