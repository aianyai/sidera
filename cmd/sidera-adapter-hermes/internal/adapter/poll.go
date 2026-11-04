package adapter

import (
	"context"
	"log/slog"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/card"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
)

// poller looks at the board for cards that are ready and meant for the lane, and
// gives each to the card handler. It claims nothing itself. Like the receiver it
// does not touch the board while Hermes cannot be used, and pauses with a growing
// pause.
type poller struct {
	board    *kanban.Client
	gate     *gate
	handler  CardHandler
	log      *slog.Logger
	interval time.Duration
	tm       timing
}

func (p *poller) run(ctx context.Context) {
	pauses := newBackoff(p.tm.retryMin, p.tm.retryMax)
	for {
		wait := p.interval
		if !p.poll(ctx) {
			wait = pauses.next()
		} else {
			pauses.reset()
		}
		if !sleep(ctx, wait) {
			return
		}
	}
}

// poll makes one look and reports whether the board could be read.
func (p *poller) poll(ctx context.Context) bool {
	if err := p.gate.Check(ctx); err != nil {
		return false
	}
	cards, err := p.board.List(ctx, kanban.StatusReady)
	if err != nil {
		if ctx.Err() == nil {
			p.log.Warn("listing the ready cards", "error", err)
		}
		return false
	}
	for _, c := range cards {
		if ctx.Err() != nil {
			return true
		}
		if card.MatchesAssignee(c.Assignee) {
			p.handler.HandleCard(ctx, c)
		}
	}
	return true
}
