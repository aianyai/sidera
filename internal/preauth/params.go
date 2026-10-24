package preauth

import (
	"errors"
	"fmt"
	"time"
)

// Params are the numbers of the accounting model. None has a decided value: the
// network layer chooses them from measurement before it ships, and this package
// only checks that they are consistent. Every field must be set; the zero value
// of a field is refused. The exception is ExchangeDeadline, which is a protocol
// rule and not a field.
type Params struct {
	// UnauthenticatedCap is the most inbound connections that may be in the
	// unauthenticated state at once, counting those of peers not known as members.
	// A connection beyond it is refused as soon as the secure handshake has told
	// who is calling. It bounds how many connections a flood can hold open; it
	// does not bound the handshakes already paid for (see the topic document).
	UnauthenticatedCap int
	// NonMemberConcurrency is the most inbound connections that may be in the
	// non-member state at once. A connection whose exchange completes as a
	// non-member while the limit is reached is closed.
	NonMemberConcurrency int
	// NonMemberLifetime is how long an inbound connection may live without
	// becoming a member or an authorized person, counted from the moment the
	// connection exists. It covers the exchange and the online part of pairing,
	// so it is at least ExchangeDeadline + PairingOnlineTimeout.
	NonMemberLifetime time.Duration
	// PairingOnlineTimeout is the time pairing gives its online part. Pairing
	// owns the value; it is given here only so that the limit above can be
	// checked against it, and is never started by this package.
	PairingOnlineTimeout time.Duration
	// SweepInterval is the age at which a connection that was let through the
	// secure handshake but was never seen connected is forgotten, and so how often
	// the network layer must call Sweep. A connection can be rejected after the
	// handshake (resource limits, multiplexer negotiation) without any other
	// callback, and its entry would otherwise count against the cap for good. It
	// must be longer than the longest time between the handshake and the
	// connection being established, or live connections lose their entry.
	SweepInterval time.Duration
}

// ParamsProblem says what is wrong with a parameter.
type ParamsProblem uint8

const (
	// ProblemUnset: the field is zero or negative.
	ProblemUnset ParamsProblem = iota + 1
	// ProblemLifetimeTooShort: NonMemberLifetime is below ExchangeDeadline plus
	// PairingOnlineTimeout, so a connection could be closed in the middle of the
	// online part of pairing.
	ProblemLifetimeTooShort
)

func (p ParamsProblem) String() string {
	switch p {
	case ProblemUnset:
		return "unset"
	case ProblemLifetimeTooShort:
		return "lifetime-too-short"
	}
	return "invalid"
}

// ParamsError is the error of Params.Validate. Match it with errors.As, or with
// errors.Is against ErrParams.
type ParamsError struct {
	// Field is the name of the offending field.
	Field   string
	Problem ParamsProblem
}

func (e *ParamsError) Error() string {
	return fmt.Sprintf("preauth: parameter %s: %s", e.Field, e.Problem)
}

func (e *ParamsError) Is(target error) bool { return target == ErrParams }

// ErrParams is matched by every *ParamsError.
var ErrParams = errors.New("invalid preauth parameters")

// Validate checks that every field is set and that NonMemberLifetime leaves
// room for the exchange and the online part of pairing. The first problem found
// is returned, fields in declaration order.
func (p Params) Validate() error {
	for _, f := range []struct {
		name string
		ok   bool
	}{
		{"UnauthenticatedCap", p.UnauthenticatedCap > 0},
		{"NonMemberConcurrency", p.NonMemberConcurrency > 0},
		{"NonMemberLifetime", p.NonMemberLifetime > 0},
		{"PairingOnlineTimeout", p.PairingOnlineTimeout > 0},
		{"SweepInterval", p.SweepInterval > 0},
	} {
		if !f.ok {
			return &ParamsError{Field: f.name, Problem: ProblemUnset}
		}
	}
	if p.NonMemberLifetime < ExchangeDeadline+p.PairingOnlineTimeout {
		return &ParamsError{Field: "NonMemberLifetime", Problem: ProblemLifetimeTooShort}
	}
	return nil
}
