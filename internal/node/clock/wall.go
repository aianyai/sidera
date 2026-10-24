package clock

import (
	"time"

	"github.com/aianyai/sidera/internal/timing"
)

// WallVerdict is the outcome of comparing the wall clock with the high water
// mark. The zero value is unset.
type WallVerdict uint8

const (
	// WallOK means the reading may be used.
	WallOK WallVerdict = iota + 1
	// WallRolledBack means the wall clock reads earlier than the high water
	// mark by more than the clock tolerance.
	WallRolledBack
)

// WallReason says why [CheckWall] reached its verdict. The zero value is unset.
type WallReason uint8

const (
	// ReasonNoHighWater: nothing is recorded yet, as on a first start.
	ReasonNoHighWater WallReason = iota + 1
	// ReasonNotBehind: now is at or after the high water mark.
	ReasonNotBehind
	// ReasonWithinTolerance: now is behind the mark by at most
	// timing.ClockTolerance.
	ReasonWithinTolerance
	// ReasonBehindHighWater: now is behind the mark by more than
	// timing.ClockTolerance.
	ReasonBehindHighWater
)

// CheckWall compares the wall clock with the persisted high water mark. A zero
// highWater means none is recorded. The tolerance is timing.ClockTolerance:
// peers already accept timestamps up to timing.ClockTolerance from their own
// clock, so a set-back within it (including NTP stepping a drifted clock) yields
// signed times and deadline judgments indistinguishable from skew peers already
// tolerate, and pausing for it would only cost availability. A larger set-back
// would let the node sign or judge times earlier than what it already issued,
// beyond any peer's tolerance. Using the same constant makes the threshold
// exactly the peers' tolerance, and it is not a parameter.
func CheckWall(now, highWater time.Time) (WallVerdict, WallReason) {
	switch {
	case highWater.IsZero():
		return WallOK, ReasonNoHighWater
	case !now.Before(highWater):
		return WallOK, ReasonNotBehind
	case highWater.Sub(now) <= timing.ClockTolerance:
		return WallOK, ReasonWithinTolerance
	default:
		return WallRolledBack, ReasonBehindHighWater
	}
}

// HighWater is the in-memory high water mark, owned by the single writer and
// not safe for concurrent use. It is raised only by readings that pass
// [CheckWall] and are not behind it, so a clock that was set back never lowers
// it.
type HighWater struct {
	t time.Time
}

// NewHighWater starts the mark at the persisted value; the zero time means
// nothing was persisted.
func NewHighWater(persisted time.Time) HighWater { return HighWater{t: persisted} }

// Observe judges the reading now against the mark and, when the reading is at
// or ahead of the mark, raises the mark to it. A reading behind the mark never
// changes it, whether within the tolerance or rolled back; the mark does not
// remember a rejected reading, so each call is judged against the same mark.
func (h *HighWater) Observe(now time.Time) (WallVerdict, WallReason) {
	verdict, reason := CheckWall(now, h.t)
	if reason == ReasonNoHighWater || reason == ReasonNotBehind {
		h.t = now
	}
	return verdict, reason
}

// Value is the current mark: what the periodic task persists.
func (h HighWater) Value() time.Time { return h.t }

// Reset accepts now as the new mark even when it is behind the old one. It is
// the effect of the owner clearing a rollback.
func (h *HighWater) Reset(now time.Time) { h.t = now }
