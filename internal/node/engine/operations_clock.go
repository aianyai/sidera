package engine

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	nodekeys "github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
)

// ClockClear is what the owner's clearing of the pause of a wall clock set back
// did, for the result to say.
type ClockClear struct {
	// Cleared: a pause was in force and is lifted. False is a request that found
	// none, which writes no mark and still stores its result.
	Cleared bool
	// HighWater is the mark the node follows now: the clock as it read at the
	// clear when a pause was lifted, and the mark that was when none was.
	HighWater time.Time
	// Now is the clock as it read when the request ran.
	Now time.Time
}

// ClearClockRollbackOp is the operation that lifts the pause of a wall clock that
// was set back. The owner has looked at the clock, so the mark the node follows
// from now on is the clock as it reads, which may be below the mark it replaces,
// and the pause flag goes with it: both are one replacement of the clock record,
// in the transaction that stores the result, so there is no state on disk with
// the flag gone and the mark unchanged. Memory follows the disk, after the commit.
// encode makes the result; it runs in the loop and must not wait.
//
// A request that finds no pause has nothing to write and says so. It stores its
// result all the same. A crash between the clock record and the result leaves the
// pause lifted without the result, and the request sent again says that no pause
// was in force: it has no effect, so the result differs from the first, which the
// operation allows itself because it changes nothing the second time.
func ClearClockRollbackOp(encode func(ClockClear) ([]byte, error)) Operation {
	return &clearClockOp{encode: encode}
}

type clearClockOp struct {
	encode func(ClockClear) ([]byte, error)
}

func (o *clearClockOp) Admin() admin.Operation {
	return admin.Operation{Kind: admin.OpClearClockRollback}
}
func (o *clearClockOp) stops() bool { return false }
func (o *clearClockOp) run(e *Engine, x *opRun) ([]byte, error) {
	out := ClockClear{Cleared: e.paused, HighWater: e.hw.Value(), Now: x.now}
	var prepared nodekeys.PreparedClockCleared
	if out.Cleared {
		var err error
		if prepared, err = nodekeys.PrepareClockCleared(x.now); err != nil {
			return nil, err
		}
		out.HighWater = x.now
	}
	result, err := o.encode(out)
	if err != nil {
		return nil, fmt.Errorf("engine: the result of %s: %w", x.name, err)
	}
	pr, err := store.PrepareAdminResult(x.record(result))
	if err != nil {
		return nil, err
	}
	tx := e.d.Writer.Begin(&declClockClear)
	defer tx.Close()
	if out.Cleared {
		if err := nodekeys.PutClockCleared(tx, prepared); err != nil {
			return nil, err
		}
	}
	if err := store.PutAdminResult(tx, store.RoleResult, pr); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if out.Cleared {
		e.clearRollback(x.now)
	}
	return result, nil
}
