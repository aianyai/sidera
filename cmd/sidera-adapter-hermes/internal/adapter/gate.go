package adapter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// gate asks whether the Hermes command line can be used right now, and tells the
// daemon when the answer changes. Both loops ask before they touch anything: a
// message is taken from Sidera only when the card it becomes can be written, since
// a message that is taken and not acknowledged is handed out again after its
// lease and each hand-out brings it nearer to being declared of unknown outcome.
//
// "Can be used" means the whole probe of the client: a Hermes release the adapter
// was built against, no profile the dispatcher would confuse with the lane, the
// board in place. The probe costs three Hermes processes, so it runs when the gate
// has never passed it, when it last failed, and every timing.verifyEvery: a Hermes
// that is upgraded under a running adapter, or a profile created later, is found
// within that time. Between probes the check is the one cheap command that needs
// the board.
type gate struct {
	board       *kanban.Client
	report      func(context.Context, adapterproto.AvailabilityParams) error
	log         *slog.Logger
	now         func() time.Time
	verifyEvery time.Duration

	// mu is held from the decision that the state changed until the daemon has been
	// told, so that the reports reach the daemon in the order of the changes.
	mu         sync.Mutex
	down       bool
	verifiedAt time.Time
}

// Check asks whether Hermes can be used, and reports a change. A failure caused by
// the end of ctx is not Hermes' and is returned without being reported.
func (g *gate) Check(ctx context.Context) error {
	full := g.verificationDue()
	var err error
	if full {
		err = g.board.Probe(ctx)
	} else {
		var ok bool
		ok, err = g.board.BoardExists(ctx)
		if err == nil && !ok {
			err = errors.New("the board does not exist")
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	g.set(ctx, err, full)
	return err
}

// verificationDue reports whether the next check has to be the whole probe.
func (g *gate) verificationDue() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.down || g.verifiedAt.IsZero() || g.now().Sub(g.verifiedAt) >= g.verifyEvery
}

// set records the outcome of a check and reports a change of state, and only a
// change: the daemon replaces the previous snapshot with each one it gets.
func (g *gate) set(ctx context.Context, err error, full bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case err != nil:
		g.verifiedAt = time.Time{}
	case full:
		g.verifiedAt = g.now()
	}
	changed := (err != nil) != g.down
	g.down = err != nil
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
