// Package clock is the time source of the daemon's runtime packages, and the
// judgment of whether the wall clock has been set back.
//
// Every protocol or state deadline reads time through a [Clock] that is handed
// to it, so a test drives the same code with a fake from package clocktest. In
// the daemon's runtime packages (internal/node/...), this package is the only
// code allowed to read the process clock directly.
package clock

import "time"

// Clock is the injected time source for every protocol and state deadline. Now
// returns UTC wall time with no monotonic reading, so two readings compare by
// wall clock: a reading that carries a monotonic reading would be compared by
// it, and a wall clock set back would never show.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer is a single-shot timer with the semantics of [time.Timer] since Go 1.23:
// after Stop or Reset returns, a value from the earlier setting is never
// received, and a due value that was not yet received still counts as pending
// (Stop and Reset report true for it).
type Timer interface {
	// C delivers the time at which the timer fired, once per setting.
	C() <-chan time.Time
	// Stop prevents the timer from firing and reports whether it was still
	// pending.
	Stop() bool
	// Reset re-arms the timer to fire after d and reports whether it was still
	// pending.
	Reset(d time.Duration) bool
}

// System is the real clock. Code that needs real time (I/O timeouts, ping,
// restart backoff, retry backoff, shutdown limits) names it explicitly; code
// that decides a protocol or state deadline takes a Clock from its caller.
func System() Clock { return system{} }

type system struct{}

// Now returns the wall time in UTC. UTC strips the monotonic reading that
// time.Now attaches.
func (system) Now() time.Time { return time.Now().UTC() }

func (system) NewTimer(d time.Duration) Timer { return systemTimer{time.NewTimer(d)} }

type systemTimer struct{ t *time.Timer }

func (s systemTimer) C() <-chan time.Time        { return s.t.C }
func (s systemTimer) Stop() bool                 { return s.t.Stop() }
func (s systemTimer) Reset(d time.Duration) bool { return s.t.Reset(d) }
