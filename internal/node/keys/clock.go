package keys

import (
	"errors"
	"time"

	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
)

// ClockState is what keys/clock.rec holds: the highest wall-clock reading the
// node has persisted, and, only while a rollback pause is in force, since when.
// Both are whole seconds in UTC. The file lives in keys/ and not in state/
// because deleting state/ must not reset the mark: a node whose mark is zero
// would sign with a clock that was set back, and a deleted pause flag would let
// the owner's confirmation be skipped.
type ClockState struct {
	// HighWater is the persisted mark. The zero time means none is recorded: the
	// node starts for the first time.
	HighWater time.Time
	// RollbackPausedSince is when the pause started; the zero time means the node
	// is not paused. The rollback alert is derived from it and stored nowhere
	// else.
	RollbackPausedSince time.Time
}

// Paused reports whether a rollback pause is in force.
func (c ClockState) Paused() bool { return !c.RollbackPausedSince.IsZero() }

// ReadClock reads keys/clock.rec. A file that is not there is the zero state:
// the first start, the semantics of clock.ReasonNoHighWater, which is what the
// owner gets who deletes the file while the node is stopped. A file that is
// damaged or that anyone but the owner can reach is refused, naming it.
func ReadClock(r fsx.Reader) (ClockState, error) {
	data, err := readOwnerOnly(r, ClockPath)
	if errors.Is(err, ErrMissing) {
		return ClockState{}, nil
	}
	if err != nil {
		return ClockState{}, err
	}
	c, err := decodeClock(data)
	if err != nil {
		return ClockState{}, corrupt(ClockPath, err)
	}
	return c, nil
}

// SaveClock writes the clock record: the periodic persistence of the high water
// mark, and the start of a rollback pause. It never goes back. A mark below the
// persisted one, a pause flag that is lifted or moved, and a state with no mark
// are refused with ErrClockRegression and write nothing; only the owner's clear,
// PutClockCleared, resets the mark or lifts the pause. Times are cut to whole
// seconds before they are compared and written, so the persisted mark is never
// above the reading it came from.
func (k *Keys) SaveClock(next ClockState) error {
	next.HighWater = next.HighWater.UTC().Truncate(time.Second)
	next.RollbackPausedSince = next.RollbackPausedSince.UTC().Truncate(time.Second)
	if next.HighWater.Unix() < 1 {
		return fileError(ClockPath, ErrClockRegression, "the record needs a high water mark")
	}
	cur, err := ReadClock(k.s.w.Reader())
	if err != nil {
		return err
	}
	switch {
	case next.HighWater.Before(cur.HighWater):
		return fileError(ClockPath, ErrClockRegression, "the high water mark would go from %s to %s", formatTime(cur.HighWater), formatTime(next.HighWater))
	case cur.Paused() && !next.RollbackPausedSince.Equal(cur.RollbackPausedSince):
		return fileError(ClockPath, ErrClockRegression, "the rollback pause in force would be lifted or moved")
	}
	tx := k.s.w.Begin(clockHighWater)
	defer tx.Close()
	if err := tx.Replace(RoleClock, ClockPath, encodeClock(next), fsx.ModePrivate); err != nil {
		return err
	}
	return tx.Commit()
}

// PutClockCleared is the write of the owner's clear of a rollback pause, a
// transaction of the engine that adds its own result: the high water mark is set
// to now, which may be below the mark it replaces because the clock really was
// set back, and the pause flag is removed, in one replacement. There is no state
// in between with the flag gone and the mark unchanged. The caller opens and
// commits the transaction and decides whether there was a pause to clear.
func PutClockCleared(tx *txn.Tx, now time.Time) error {
	now = now.UTC().Truncate(time.Second)
	if now.Unix() < 1 {
		return fileError(ClockPath, ErrClockRegression, "the record needs a high water mark")
	}
	return tx.Replace(RoleClock, ClockPath, encodeClock(ClockState{HighWater: now}), fsx.ModePrivate)
}

// rolledBack reports whether now is behind the persisted mark by more than the
// clock tolerance, by the rule of the clock package.
func rolledBack(now, highWater time.Time) bool {
	verdict, _ := clock.CheckWall(now, highWater)
	return verdict == clock.WallRolledBack
}
