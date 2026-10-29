package acceptance

import (
	"context"
	"time"

	"github.com/aianyai/sidera/internal/node/manageapi"
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
		if err := c.SetClock(ctx, f.Now().UnixMilli()); err != nil {
			return err
		}
		if f.step != "" {
			if err := c.SetStep(ctx, f.step); err != nil {
				return err
			}
		}
	}
	return nil
}
