package adapter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// gate asks whether the Hermes command line can be used right now, and tells the
// daemon when the answer changes. Both loops ask before they touch anything: a
// message is taken from Sidera only when the card it becomes can be written, since
// a message that is taken and not acknowledged is handed out again after its
// lease and each hand-out brings it nearer to being declared of unknown outcome.
type gate struct {
	board  *kanban.Client
	report func(context.Context, adapterproto.AvailabilityParams) error
	log    *slog.Logger

	mu   sync.Mutex
	down bool
}

// Check runs a cheap command that needs the board: it fails when the program
// cannot be run, the database is locked or the board is gone. A failure caused by
// the end of ctx is not Hermes' and is returned without being reported.
func (g *gate) Check(ctx context.Context) error {
	ok, err := g.board.BoardExists(ctx)
	if err == nil && !ok {
		err = errors.New("the board does not exist")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	g.set(ctx, err)
	return err
}

// set reports a change of state, and only a change: the daemon replaces the
// previous snapshot with each one it gets.
func (g *gate) set(ctx context.Context, err error) {
	g.mu.Lock()
	changed := (err != nil) != g.down
	g.down = err != nil
	g.mu.Unlock()
	if !changed {
		return
	}
	snapshot := adapterproto.AvailabilityParams{Conditions: []adapterproto.Availability{}}
	if err != nil {
		g.log.Warn("hermes kanban is not usable; receiving and polling are paused", "error", err)
		snapshot.Conditions = append(snapshot.Conditions, adapterproto.Availability{
			Condition: adapterproto.ConditionDependencyMissing,
			Detail:    detail(fmt.Sprintf("hermes kanban is not usable, so no card is read or written and messages wait in Sidera: %v", err)),
		})
	} else {
		g.log.Info("hermes kanban is usable again")
	}
	if rerr := g.report(ctx, snapshot); rerr != nil && ctx.Err() == nil {
		g.log.Warn("reporting availability", "error", rerr)
	}
}

// detail cuts text to what an availability or dependency detail may hold.
func detail(s string) string {
	if len(s) <= adapterproto.MaxDetailLen {
		return s
	}
	return strings.ToValidUTF8(s[:adapterproto.MaxDetailLen-3], "") + "..."
}
