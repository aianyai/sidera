package pairing

import (
	"errors"
	"fmt"
	"time"
)

// ErrParams reports a Params with an unset or inconsistent value.
var ErrParams = errors.New("invalid pairing parameters")

// Params are the tunable numbers of the protocol. The starting values are the
// ones DefaultParams returns; they are measured and adjusted with real
// networks, so they are a struct and not constants. The zero value of any
// field is invalid: a parameter that was never set must not silently mean
// "no limit".
type Params struct {
	// OnlineTimeout bounds the online segment, from the stream's opening to the
	// target's last frame. A connection that has not finished a certificate
	// exchange and this segment in time is the connection layer's to drop: its
	// lifetime for such connections must be at least this plus the certificate
	// exchange.
	OnlineTimeout time.Duration
	// FetchTimeout bounds one result exchange.
	FetchTimeout time.Duration
	// FetchBackoffMin and FetchBackoffMax bound the delay between result
	// exchanges, which grows exponentially from the first to the second with
	// jitter.
	FetchBackoffMin, FetchBackoffMax time.Duration
	// PendingTTL is how long a target holds a request for its owner, from the
	// moment it accepts it.
	PendingTTL time.Duration
	// ResultRetention is how long a target keeps the outcome of a request after
	// it ended, for an initiator that comes back late, and how long past its
	// expiry an initiator keeps asking.
	ResultRetention time.Duration
	// ExpectationTTL is how long an expectation lasts after it was entered.
	ExpectationTTL time.Duration

	// PendingCap is the most unexpected requests a target holds at once. It is
	// never made room for by evicting a request: eviction would let a stranger
	// push out the one an owner is comparing.
	PendingCap int
	// ExpectationCap is the most live expectations a node holds.
	ExpectationCap int

	// RevealBudget is how many times in RevealWindow a target reveals its nonce
	// to an initiator it does not expect. Every reveal gives an attacker who
	// aborts and starts again one more guess at the code, so this number, and
	// not the number of requests, bounds the chance that a man in the middle
	// gets the same code on both screens: at most RevealBudget in 10^6 per
	// window.
	RevealBudget int
	RevealWindow time.Duration

	// FailureLimit failures within FailureWindow pause pairing with nodes that
	// are not expected. A failure is only an owner reporting that the codes
	// differ.
	FailureLimit  int
	FailureWindow time.Duration
}

// DefaultParams returns the starting values.
func DefaultParams() Params {
	return Params{
		OnlineTimeout:   30 * time.Second,
		FetchTimeout:    10 * time.Second,
		FetchBackoffMin: time.Second,
		FetchBackoffMax: time.Minute,
		PendingTTL:      time.Hour,
		ResultRetention: 24 * time.Hour,
		ExpectationTTL:  time.Hour,
		PendingCap:      16,
		ExpectationCap:  32,
		RevealBudget:    5,
		RevealWindow:    time.Hour,
		FailureLimit:    3,
		FailureWindow:   time.Hour,
	}
}

// Validate reports why p cannot be used, or nil. Durations are whole seconds,
// because every time the package keeps is.
func (p Params) Validate() error {
	durations := []struct {
		name string
		d    time.Duration
	}{
		{"OnlineTimeout", p.OnlineTimeout}, {"FetchTimeout", p.FetchTimeout},
		{"FetchBackoffMin", p.FetchBackoffMin}, {"FetchBackoffMax", p.FetchBackoffMax},
		{"PendingTTL", p.PendingTTL}, {"ResultRetention", p.ResultRetention}, {"ExpectationTTL", p.ExpectationTTL},
		{"RevealWindow", p.RevealWindow}, {"FailureWindow", p.FailureWindow},
	}
	for _, d := range durations {
		if d.d <= 0 || d.d%time.Second != 0 {
			return fmt.Errorf("%w: %s must be a positive whole number of seconds", ErrParams, d.name)
		}
	}
	counts := []struct {
		name string
		n    int
	}{
		{"PendingCap", p.PendingCap}, {"ExpectationCap", p.ExpectationCap},
		{"RevealBudget", p.RevealBudget}, {"FailureLimit", p.FailureLimit},
	}
	for _, c := range counts {
		if c.n <= 0 {
			return fmt.Errorf("%w: %s must be positive", ErrParams, c.name)
		}
	}
	if p.FetchBackoffMax < p.FetchBackoffMin {
		return fmt.Errorf("%w: FetchBackoffMax is below FetchBackoffMin", ErrParams)
	}
	return nil
}
