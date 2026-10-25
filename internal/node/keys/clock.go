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
	c, _, err := readClockIfPresent(r)
	return c, err
}

// readClockIfPresent is ReadClock that also says whether the file was there.
func readClockIfPresent(r fsx.Reader) (ClockState, bool, error) {
	data, err := readOwnerOnly(r, ClockPath)
	if errors.Is(err, ErrMissing) {
		return ClockState{}, false, nil
	}
	if err != nil {
		return ClockState{}, false, err
	}
	c, err := decodeClock(data)
	if err != nil {
		return ClockState{}, false, corrupt(ClockPath, err)
	}
	return c, true, nil
}

// SaveClock writes the clock record: the periodic persistence of the high water
// mark, and the start of a rollback pause. It never goes back. A mark below the
// persisted one, a pause flag that is lifted or moved, and a state with no mark
// are refused with ErrClockRegression and write nothing, and so is a mark or a
// pause start that no record can hold, except that a pause start before
// 1970-01-01T00:00:01Z is stored as that second; only the owner's clear,
// PrepareClockCleared with PutClockCleared, resets the mark or lifts the pause.
// Times are cut to whole seconds before they are compared and written, so the
// persisted mark is never above the reading it came from.
func (k *Keys) SaveClock(next ClockState) error {
	next.HighWater = next.HighWater.UTC().Truncate(time.Second)
	next.RollbackPausedSince = next.RollbackPausedSince.UTC().Truncate(time.Second)
	// A pause that started before the first second a record holds (a dead
	// battery sets the clock to the epoch) is the case the pause exists for, so
	// it is stored with its start at that second rather than refused.
	if next.Paused() && next.RollbackPausedSince.Before(earliestRecordTime) {
		next.RollbackPausedSince = earliestRecordTime
	}
	if err := checkRecordTime(next.HighWater); err != nil {
		return fileError(ClockPath, ErrClockRegression, "the record needs a high water mark from 1970-01-01T00:00:01Z on, to the end of year 9999")
	}
	if !next.RollbackPausedSince.IsZero() {
		if err := checkRecordTime(next.RollbackPausedSince); err != nil {
			return fileError(ClockPath, ErrClockRegression, "the rollback pause needs a start from 1970-01-01T00:00:01Z on, to the end of year 9999")
		}
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

// PreparedClockCleared is the clear of a rollback pause, checked and encoded for
// a transaction of the engine. PrepareClockCleared runs before that transaction
// begins, so a time the record cannot hold is an error that touches nothing, and
// PutClockCleared can only fail the way a write of the transaction does.
type PreparedClockCleared struct{ data []byte }

// PrepareClockCleared checks and encodes the owner's clear of a rollback pause:
// the high water mark is set to now, which may be below the mark it replaces
// because the clock really was set back, and the pause flag is removed. It does
// no I/O.
func PrepareClockCleared(now time.Time) (PreparedClockCleared, error) {
	now = now.UTC().Truncate(time.Second)
	if err := checkRecordTime(now); err != nil {
		return PreparedClockCleared{}, fileError(ClockPath, ErrClockRegression, "the record needs a high water mark from 1970-01-01T00:00:01Z on, to the end of year 9999")
	}
	return PreparedClockCleared{encodeClock(ClockState{HighWater: now})}, nil
}

// PutClockCleared writes a prepared clear in a transaction of the engine that
// adds its own result: the mark and the removal of the flag are one replacement,
// so there is no state in between with the flag gone and the mark unchanged. The
// caller opens and commits the transaction and decides whether there was a pause
// to clear. It panics for a PreparedClockCleared that PrepareClockCleared did
// not return.
func PutClockCleared(tx *txn.Tx, p PreparedClockCleared) error {
	if p.data == nil {
		panic("keys: PutClockCleared of a value PrepareClockCleared did not return")
	}
	return tx.Replace(RoleClock, ClockPath, p.data, fsx.ModePrivate)
}

// initSigningTime is the time at which init signs a certificate again: the later
// of now and the persisted mark. Renew refuses to sign while the clock is in
// doubt (rolledBack, or a pause in force); init cannot refuse, because only a
// running daemon clears a pause and the daemon cannot start before init has
// finished, so refusing would leave the owner no way out but deleting
// clock.rec, which loses the pause. Signing no earlier than the mark keeps the
// certificate from starting before what the node already issued, and the next
// renewal after the pause is cleared replaces it.
func initSigningTime(now time.Time, c ClockState) time.Time {
	if c.HighWater.After(now) {
		return c.HighWater
	}
	return now
}

// rolledBack reports whether now is behind the persisted mark by more than the
// clock tolerance, by the rule of the clock package.
func rolledBack(now, highWater time.Time) bool {
	verdict, _ := clock.CheckWall(now, highWater)
	return verdict == clock.WallRolledBack
}
