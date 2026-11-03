package runtime

import (
	"context"
	"errors"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/runtime/proc"
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
// reported when it comes; not_running when no turn has started or its process is
// gone, including a process that ended before the signal reached it; and refused
// when the process cannot be looked at or signalled. The process is never killed.
//
// A turn begins when its prompt is in the session record, as for turn.started. A
// process that has not got that far has no turn to stop, and signalling it would
// end it without a trace of the prompt, which the daemon would then find absent
// and deliver again.
//
// A repeated request for a turn whose process was signalled does not signal it
// again; it waits for the end as the first did. The interrupts and the close of a
// session are served one at a time. An attached session is the wrong kind of
// endpoint: the turn is the person's. A session that is still being created is
// not held yet and is unknown, as it is to a close.
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
	// The turn is brought up to date first: it may have started or ended since
	// the monitor looked.
	a.tick(ctx, s)
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
	if !alive || !t.StartedReported {
		// A process that is gone has its end on its way; one whose prompt is not in
		// the record yet has no turn.
		s.mu.Unlock()
		return adapterproto.InterruptResult{Stage: adapterproto.InterruptNotRunning}, nil
	}
	s.mu.Unlock()

	if err := a.interruptProcess(ctx, s, target); err != nil {
		if errors.Is(err, proc.ErrNotRunning) {
			return adapterproto.InterruptResult{Stage: adapterproto.InterruptNotRunning}, nil
		}
		a.log.Warn("a turn cannot be interrupted", "endpoint", s.endpoint, "error", err)
		return refusedInterrupt(adapterproto.InterruptNativeError), nil
	}

	gone, err := a.waitEnded(ctx, []turn.Process{target}, a.o.InterruptWait)
	if err != nil {
		if ctx.Err() != nil {
			return adapterproto.InterruptResult{}, ctx.Err()
		}
		// The signal was delivered; the turn will end, or not, whether it can be
		// looked at or not, and its end is reported when it comes.
		a.log.Warn("the process of a turn cannot be looked at", "endpoint", s.endpoint, "error", err)
		return adapterproto.InterruptResult{Stage: adapterproto.InterruptRequested}, nil
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
