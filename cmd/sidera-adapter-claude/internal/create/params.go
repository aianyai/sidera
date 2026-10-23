package create

import (
	"errors"
	"fmt"
	"time"
)

// ErrInvalidParams is wrapped by errors about parameters that are not positive.
var ErrInvalidParams = errors.New("create: invalid parameters")

// Params holds the values that are not decided yet. DefaultParams returns the
// provisional starting points.
type Params struct {
	// StartTimeout is how long a turn process may run, counted from its own
	// start time, before the first prompt is in the session record. Past it
	// the attempt fails: the prompt reaches the record within seconds of the
	// start, so a longer silence means the process is stuck. The boundary
	// itself is not yet a failure.
	StartTimeout time.Duration
	// MaxStarts is how many distinct turn processes one attempt may use. Each
	// retry after a process that ended without the first prompt in the record
	// is one more; past the limit the attempt fails instead of looping on a
	// runtime that cannot start.
	MaxStarts int
}

// DefaultParams returns the provisional starting points: they are neither
// measured nor approved.
func DefaultParams() Params {
	return Params{StartTimeout: 2 * time.Minute, MaxStarts: 3}
}

// Validate refuses parameters that are not positive.
func (p Params) Validate() error {
	if p.StartTimeout <= 0 {
		return fmt.Errorf("%w: StartTimeout must be positive, got %v", ErrInvalidParams, p.StartTimeout)
	}
	if p.MaxStarts <= 0 {
		return fmt.Errorf("%w: MaxStarts must be positive, got %d", ErrInvalidParams, p.MaxStarts)
	}
	return nil
}
