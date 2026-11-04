package adapter

import (
	"context"
	"errors"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// interruptCallTimeout bounds the request that asks the server to stop a turn, and
// endCommandsTimeout the requests that end the commands the turn left running. The
// server answers them at once; a server that does not is not worth waiting for.
const (
	interruptCallTimeout = 10 * time.Second
	endCommandsTimeout   = 10 * time.Second
)

func refusedInterrupt(reason adapterproto.InterruptRefusal) adapterproto.InterruptResult {
	return adapterproto.InterruptResult{Stage: adapterproto.InterruptRefused, Reason: reason}
}

// refusalOf says why a request to the server failed, as an interrupt reports it.
func refusalOf(err error) adapterproto.InterruptRefusal {
	if errors.Is(err, appserver.ErrClosed) {
		return adapterproto.InterruptNativeUnreachable
	}
	return adapterproto.InterruptNativeError
}

// Interrupt asks the server to stop the session's running turn and waits for the end
// of the turn to be reported, which it does only after the commands the turn left
// running are ended: the server stops the turn and leaves those commands alive. The
// answer is stopped once the turn is over and its commands are ended, requested when
// the turn is still running after Options.InterruptWait (the end is reported when it
// comes), not_running when there is no turn, and refused when the server could not
// be asked, or the commands could not be ended.
//
// An attached session is the wrong kind of endpoint: the turn is the person's.
func (a *App) Interrupt(ctx context.Context, p adapterproto.InterruptParams) (adapterproto.InterruptResult, error) {
	s := a.sessionOf(p.Endpoint)
	if s == nil {
		return adapterproto.InterruptResult{}, unknownEndpoint(p.Endpoint)
	}
	if s.attached {
		return adapterproto.InterruptResult{}, wrongKind("interrupting")
	}
	s.interruptMu.Lock()
	defer s.interruptMu.Unlock()

	a.mu.Lock()
	creating := s.creating
	a.mu.Unlock()
	if creating {
		return adapterproto.InterruptResult{}, errStillCreating
	}
	c := a.liveClient()
	if c == nil {
		return refusedInterrupt(adapterproto.InterruptNativeUnreachable), nil
	}
	// A thread the server unloaded is loaded again first, which also brings the
	// books on its turns up to date: the turn the adapter thought running may be over.
	a.mu.Lock()
	status := s.status
	a.mu.Unlock()
	if status == appserver.ThreadNotLoaded {
		switch state, _ := a.loadUnloaded(ctx, c, s, true); state {
		case adapterproto.AttachAttached:
		case adapterproto.AttachLost:
			return adapterproto.InterruptResult{Stage: adapterproto.InterruptNotRunning}, nil
		default:
			return refusedInterrupt(adapterproto.InterruptNativeUnreachable), nil
		}
	}

	a.mu.Lock()
	turnID := s.activeTurn
	if turnID == "" {
		a.mu.Unlock()
		return adapterproto.InterruptResult{Stage: adapterproto.InterruptNotRunning}, nil
	}
	rec := s.turn(turnID)
	asked := rec.over != nil // an earlier call asked and gave up waiting
	if !asked {
		rec.over = make(chan struct{})
	}
	over := rec.over
	a.mu.Unlock()

	if !asked {
		callCtx, cancel := context.WithTimeout(ctx, interruptCallTimeout)
		err := c.TurnInterrupt(callCtx, s.threadID, turnID)
		cancel()
		if err != nil {
			a.mu.Lock()
			rec.over = nil
			a.mu.Unlock()
			if appserver.IsNoActiveTurn(err) {
				// The turn ended while the call was made; its end is on its way.
				return adapterproto.InterruptResult{Stage: adapterproto.InterruptNotRunning}, nil
			}
			a.log.Warn("cannot interrupt a turn", "endpoint", s.endpoint, "error", err)
			return refusedInterrupt(refusalOf(err)), nil
		}
	}

	wait := time.NewTimer(a.opts.InterruptWait)
	defer wait.Stop()
	select {
	case <-over:
	case <-wait.C:
		return adapterproto.InterruptResult{Stage: adapterproto.InterruptRequested}, nil
	case <-ctx.Done():
		return adapterproto.InterruptResult{}, ctx.Err()
	}
	a.mu.Lock()
	err := rec.endErr
	a.mu.Unlock()
	if err != nil {
		return refusedInterrupt(refusalOf(err)), nil
	}
	return adapterproto.InterruptResult{Stage: adapterproto.InterruptStopped}, nil
}

// finishTurn settles the end of a turn: it ends the commands the turn left running
// when an interrupt of the turn was sent and the turn did not complete, then makes
// the session idle if the turn was its running one, then reports the end. So the
// report, and the answer to the interrupt, say the turn's work has stopped, and the
// session takes no new turn while the commands of the old one are being ended. A
// turn that failed, or that the server no longer lists, is ended like an interrupted
// one: it is not known that it stopped what it started.
func (a *App) finishTurn(s *session, turnID, status string) {
	a.mu.Lock()
	rec := s.turn(turnID)
	over, ran, partial := rec.over, rec.commands, rec.partial
	rec.commands = nil
	a.mu.Unlock()

	var endErr error
	if over != nil && status != appserver.TurnCompleted {
		if endErr = a.endCommands(s, ran, partial); endErr != nil {
			a.log.Warn("cannot end the commands of an interrupted turn", "endpoint", s.endpoint, "error", endErr)
		}
	}
	a.mu.Lock()
	if s.activeTurn == turnID {
		s.activeTurn, s.status = "", appserver.ThreadIdle
	}
	a.mu.Unlock()
	s.signal()
	a.reportEnded(s, turnID, status)
	a.turnEnded()
	if over != nil {
		a.mu.Lock()
		rec.endErr = endErr
		a.mu.Unlock()
		close(over)
	}
}

// endCommands ends the processes of an interrupted turn's commands that are still
// running. ran holds the items of the commands the turn started; when it may be
// incomplete (partial) every process of the thread is ended, since none can be told
// from the thread's other commands.
func (a *App) endCommands(s *session, ran map[string]struct{}, partial bool) error {
	if len(ran) == 0 && !partial {
		return nil
	}
	c := a.liveClient()
	if c == nil {
		return appserver.ErrClosed
	}
	ctx, cancel := context.WithTimeout(a.bgCtx, endCommandsTimeout)
	defer cancel()
	if partial {
		return c.ThreadTerminalsClean(ctx, s.threadID)
	}
	running, err := c.ThreadTerminals(ctx, s.threadID)
	if err != nil {
		return err
	}
	var first error
	for _, t := range running {
		if _, ok := ran[t.ItemID]; !ok {
			continue
		}
		if _, err := c.ThreadTerminalTerminate(ctx, s.threadID, t.ProcessID); err != nil && first == nil {
			first = err
		}
	}
	return first
}
