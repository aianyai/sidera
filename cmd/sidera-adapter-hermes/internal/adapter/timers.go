package adapter

import (
	"context"
	"log/slog"
)

// timerLoop runs the timer handler: recovery first, then a tick every timing.tick.
// Like the receiver and the poller it does not touch the board while Hermes cannot
// be used, and pauses with a growing pause.
type timerLoop struct {
	handler TimerHandler
	gate    *gate
	log     *slog.Logger
	tm      timing
}

func (l *timerLoop) run(ctx context.Context) {
	pauses := newBackoff(l.tm.retryMin, l.tm.retryMax)
	recovered := false
	for {
		wait := l.tm.tick
		switch {
		case l.gate.Check(ctx) != nil:
			wait = pauses.next()
		case !recovered:
			if err := l.handler.Recover(ctx); err != nil {
				if ctx.Err() == nil {
					l.log.Warn("reconciling the cards with the board", "error", err)
				}
				wait = pauses.next()
			} else {
				recovered = true
				pauses.reset()
			}
		default:
			l.handler.Tick(ctx)
			pauses.reset()
		}
		if !sleep(ctx, wait) {
			return
		}
	}
}
