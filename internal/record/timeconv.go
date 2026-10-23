package record

import (
	"fmt"
	"time"
)

// MaxUnixSeconds is the largest time on the wire, 9999-12-31T23:59:59Z. It
// keeps every wire time far inside what time.Time represents, so a conversion
// can never wrap.
const MaxUnixSeconds int64 = 253402300799

// Times, counters and levels are the only values with a zero that differs from
// "nothing": every record type converts them with these functions, so the rule
// is stated once.
//
//   - A required time is a positive int64 of Unix seconds. Zero and negative
//     values are refused, so an uninitialised field can never be signed.
//   - An optional time is left out of the map to say "not provided". An explicit
//     zero is refused: it would be a second spelling of "not provided".
//   - A time.Time zero value converts to "not provided" only for optional
//     fields; the consumer's own documentation decides what that means (for
//     example, a credential without a NotAfter never expires).

// TimeToWire converts a required time to wire seconds, truncating sub-second
// precision. A zero or out-of-range time is refused.
func TimeToWire(t time.Time) (int64, error) {
	// The zero time.Time is year 1, a negative Unix time, so it is refused here
	// like every other time before the epoch.
	sec := t.Unix()
	if err := checkWireSeconds(sec); err != nil {
		return 0, err
	}
	return sec, nil
}

// TimeFromWire converts wire seconds to a UTC time. Zero, negative and
// out-of-range values are refused.
func TimeFromWire(sec int64) (time.Time, error) {
	if err := checkWireSeconds(sec); err != nil {
		return time.Time{}, err
	}
	return time.Unix(sec, 0).UTC(), nil
}

// OptionalTimeToWire converts an optional time: the zero time becomes nil
// ("not provided", the key is left out), any other time as TimeToWire.
func OptionalTimeToWire(t time.Time) (*int64, error) {
	if t.IsZero() {
		return nil, nil
	}
	sec, err := TimeToWire(t)
	if err != nil {
		return nil, err
	}
	return &sec, nil
}

// OptionalTimeFromWire converts an optional time: nil becomes the zero time,
// anything else as TimeFromWire. An explicit zero is therefore refused: it
// would be a second spelling of "not provided".
func OptionalTimeFromWire(p *int64) (time.Time, error) {
	if p == nil {
		return time.Time{}, nil
	}
	return TimeFromWire(*p)
}

func checkWireSeconds(sec int64) error {
	if sec <= 0 || sec > MaxUnixSeconds {
		return fmt.Errorf("%w: %d is outside 1..%d", ErrTime, sec, MaxUnixSeconds)
	}
	return nil
}

// RequireCounter refuses a counter or serial below 1. Counters and serials
// start at 1 so that zero stays "none yet".
func RequireCounter(v uint64) error {
	if v < 1 {
		return fmt.Errorf("%w: got %d", ErrCounter, v)
	}
	return nil
}

// wholeSeconds returns t in UTC cut to whole seconds, which is the precision of
// every wire time: a value built with it reads back equal from the wire.
func wholeSeconds(t time.Time) time.Time { return time.Unix(t.Unix(), 0).UTC() }
