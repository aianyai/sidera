package runtime

import (
	"context"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/turn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func refusedInterrupt(reason adapterproto.InterruptRefusal) adapterproto.InterruptResult {
	return adapterproto.InterruptResult{Stage: adapterproto.InterruptRefused, Reason: reason}
}

// Interrupt stops the running turn of a session the way Ctrl-C does (SIGINT,
// CTRL_C_EVENT on Windows): the turn process ends its work and exits, which
// SIGTERM would not do cleanly. The answer is stopped once the turn process has
// ended within InterruptWait, with the end of the turn reported before it;
// requested when it is still running after that, in which case the end is
// reported when it comes; not_running when no turn process runs; and refused
// when the process cannot be looked at or signalled. The process is never killed.
//
// A repeated request for a turn already interrupted does not signal it again; it
// waits for the end as the first did. The interrupts of a session are served one
// at a time. An attached session is the wrong kind of endpoint: the turn is the
// person's. A session that is still being created is not held yet and is unknown,
// as it is to a close.
func (a *App) Interrupt(ctx context.Context, p adapterproto.InterruptParams) (adapterproto.InterruptResult, error) {
	if err := a.requireStarted(); err != nil {
		return adapterproto.InterruptResult{}, err
	}
	if a.attachedSession(p.Endpoint) != nil {
		return adapterproto.InterruptResult{}, wrongEndpointKind()
	}
	s := a.session(p.Endpoint)
	if s == nil {
		return adapterproto.InterruptResult{}, unknownEndpoint()
	}
	s.interruptMu.Lock()
	defer s.interruptMu.Unlock()

	s.mu.Lock()
	t := s.turn
	if t == nil || t.Done() {
		s.mu.Unlock()
		return adapterproto.InterruptResult{Stage: adapterproto.InterruptNotRunning}, nil
	}
	target := t.Process
	alive, err := a.alive(target)
	if err != nil {
		s.mu.Unlock()
		a.log.Warn("the process of a turn cannot be looked at", "endpoint", s.endpoint, "error", err)
		return refusedInterrupt(adapterproto.InterruptNativeUnreachable), nil
	}
	if !alive {
		// The turn ended since the monitor looked; its end is on its way.
		s.mu.Unlock()
		return adapterproto.InterruptResult{Stage: adapterproto.InterruptNotRunning}, nil
	}
	// The turn is marked before the signal, so that however soon the process ends
	// its turn is reported as interrupted.
	asked := t.Interrupted
	if !asked {
		t.Interrupted = true
		if err := a.persistSession(ctx, s); err != nil {
			a.log.Warn("the checkpoint of a session was not stored", "endpoint", s.endpoint, "error", err)
		}
	}
	s.mu.Unlock()

	if !asked {
		if err := a.signal(ctx, target); err != nil {
			a.log.Warn("a turn cannot be interrupted", "endpoint", s.endpoint, "error", err)
			// The turn was not interrupted, and must not be reported as if it was.
			s.mu.Lock()
			if s.turn != nil && s.turn.Process.Same(target) {
				s.turn.Interrupted = false
				if err := a.persistSession(ctx, s); err != nil {
					a.log.Warn("the checkpoint of a session was not stored", "endpoint", s.endpoint, "error", err)
				}
			}
			s.mu.Unlock()
			return refusedInterrupt(adapterproto.InterruptNativeError), nil
		}
	}

	gone, err := a.waitEnded(ctx, []turn.Process{target}, a.o.InterruptWait)
	if err != nil {
		if ctx.Err() != nil {
			return adapterproto.InterruptResult{}, ctx.Err()
		}
		a.log.Warn("the process of a turn cannot be looked at", "endpoint", s.endpoint, "error", err)
		return refusedInterrupt(adapterproto.InterruptNativeUnreachable), nil
	}
	if !gone {
		return adapterproto.InterruptResult{Stage: adapterproto.InterruptRequested}, nil
	}
	// The end of the turn is reported before the answer, with the final that the
	// interrupt gave it.
	s.mu.Lock()
	a.tick(ctx, s)
	s.mu.Unlock()
	return adapterproto.InterruptResult{Stage: adapterproto.InterruptStopped}, nil
}
