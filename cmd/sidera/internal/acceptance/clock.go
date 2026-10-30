package acceptance

import (
	"context"
	"errors"
	"time"

	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/sdk/go/peer"
)

func (f *Fixture) SetTime(ctx context.Context, to time.Time) error {
	// The fixture owns time across generations. A lost response must not make
	// the next daemon start before a clock change the previous one may have seen.
	f.clockMu.Lock()
	f.now = to
	f.clockMu.Unlock()
	if _, err := f.Manage(ctx, "test.clock.set", manageapi.Request{Params: map[string]any{"to_ms": to.UnixMilli()}}); err != nil {
		return err
	}
	return f.syncConnections(ctx)
}

// A fake starts from the caller-owned config's current clock. Before a handle is
// returned or quiet is accepted, every connection is synchronized again.
func (f *Fixture) syncConnections(ctx context.Context) error {
	for _, c := range f.control.Conns() {
		err := c.SetClock(ctx, f.Now().UnixMilli())
		if err == nil && f.step != "" {
			err = c.SetStep(ctx, f.step)
		}
		if err != nil {
			if !errors.Is(err, peer.ErrClosed) {
				return err
			}
			// Conns is a snapshot, not a liveness lease. A member whose
			// transport has ended no longer owes a clock or step response.
			// ErrClosed can arrive before Done, so confirm within this call.
			select {
			case <-c.Done():
			case <-ctx.Done():
				return err
			}
			if ctx.Err() != nil {
				return err
			}
		}
	}
	return nil
}
