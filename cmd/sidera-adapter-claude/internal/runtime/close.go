package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/runtime/proc"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/turn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

var (
	closed      = adapterproto.CloseResult{Outcome: adapterproto.CloseClosed}
	unreachable = adapterproto.CloseResult{Outcome: adapterproto.CloseUnconfirmed, Reason: adapterproto.CloseNativeUnreachable}
	stillBusy   = adapterproto.CloseResult{Outcome: adapterproto.CloseUnconfirmed, Reason: adapterproto.CloseTurnStillRunning}
)

// CloseSession releases a session: no turn process of it runs any more, and the
// adapter forgets it. The session file stays as history, and the daemon removes
// the checkpoint. A running turn is interrupted the way Ctrl-C does (SIGINT, CTRL_C_EVENT on
// Windows), which the runtime ends cleanly (SIGTERM would leave half a turn), and waited for; one that does
// not end is reported as such and never killed.
//
// A session the adapter holds is closed by its endpoint. After a restart the
// adapter no longer holds a closed endpoint and the daemon sends the session's
// uuid: a process of that session is looked for in the process table. A uuid
// that belongs to another endpoint the adapter holds is left alone.
func (a *App) CloseSession(ctx context.Context, p adapterproto.CloseParams) (adapterproto.CloseResult, error) {
	if err := a.requireStarted(); err != nil {
		return adapterproto.CloseResult{}, err
	}
	if a.attachedSession(p.Endpoint) != nil {
		return adapterproto.CloseResult{}, wrongEndpointKind()
	}
	s := a.session(p.Endpoint)
	if s == nil {
		if p.NativeRef == "" {
			return adapterproto.CloseResult{}, unknownEndpoint()
		}
		return a.closeByReference(ctx, string(p.NativeRef))
	}

	s.mu.Lock()
	writers, err := a.writers(s.id, s.turn)
	if err != nil {
		s.mu.Unlock()
		a.log.Warn("the processes of a session cannot be looked at", "endpoint", s.endpoint, "error", err)
		return unreachable, nil
	}
	if s.turn != nil && !s.turn.Done() {
		for _, w := range writers {
			if w.Same(s.turn.Process) {
				s.turn.Interrupted = true
				if err := a.persistSession(ctx, s); err != nil {
					a.log.Warn("the checkpoint of a session was not stored", "endpoint", s.endpoint, "error", err)
				}
			}
		}
	}
	s.mu.Unlock()

	gone, err := a.interruptAndWait(ctx, writers, a.o.CloseWait)
	if err != nil {
		a.log.Warn("a session cannot be interrupted", "endpoint", s.endpoint, "error", err)
		return unreachable, nil
	}
	if !gone {
		return stillBusy, nil
	}
	// The end of the turn is reported before the answer, with the final that the
	// interrupt gave it.
	s.mu.Lock()
	a.tick(ctx, s)
	s.mu.Unlock()
	a.forget(s)
	return closed, nil
}

func (a *App) closeByReference(ctx context.Context, ref string) (adapterproto.CloseResult, error) {
	a.mu.Lock()
	for _, other := range a.sessions {
		if other.id == ref {
			a.mu.Unlock()
			return closed, nil
		}
	}
	a.mu.Unlock()
	writers, err := a.writers(ref, nil)
	if err != nil {
		a.log.Warn("the processes of a session cannot be looked at", "error", err)
		return unreachable, nil
	}
	gone, err := a.interruptAndWait(ctx, writers, a.o.CloseWait)
	switch {
	case err != nil:
		a.log.Warn("a session cannot be interrupted", "error", err)
		return unreachable, nil
	case !gone:
		return stillBusy, nil
	}
	return closed, nil
}

// writers lists the processes that write the session: its recorded turn process
// if it lives and every turn process of it in the process table.
func (a *App) writers(session string, t *turn.State) ([]turn.Process, error) {
	list, err := a.o.Table.List()
	if err != nil {
		return nil, err
	}
	var out []turn.Process
	seen := map[int]bool{}
	for _, info := range a.turnProcesses(list, session) {
		out, seen[info.PID] = append(out, processOf(info)), true
	}
	if t != nil && !t.Done() && !seen[t.Process.PID] {
		alive, err := a.alive(t.Process)
		if err != nil {
			return nil, err
		}
		if alive {
			out = append(out, t.Process)
		}
	}
	return out, nil
}

// interruptAndWait interrupts each process that is still the process it was, and
// waits up to wait for all of them to end. It reports whether they all did.
func (a *App) interruptAndWait(ctx context.Context, procs []turn.Process, wait time.Duration) (bool, error) {
	for _, p := range procs {
		alive, err := a.alive(p)
		if err != nil {
			return false, err
		}
		if alive {
			if err := a.signal(ctx, p); err != nil {
				return false, err
			}
		}
	}
	return a.waitEnded(ctx, procs, wait)
}

// signal interrupts a turn process the way Ctrl-C does. A process that ended
// since it was looked at is what waitEnded is for.
func (a *App) signal(ctx context.Context, p turn.Process) error {
	err := a.o.Signaller.Interrupt(ctx, proc.Identity{PID: p.PID, StartKey: p.StartKey})
	if err != nil && !errors.Is(err, proc.ErrNotRunning) {
		return err
	}
	return nil
}

// waitEnded waits up to wait for the processes to end, and reports whether they all did.
func (a *App) waitEnded(ctx context.Context, procs []turn.Process, wait time.Duration) (bool, error) {
	deadline := time.Now().Add(wait)
	for {
		running := false
		for _, p := range procs {
			alive, err := a.alive(p)
			if err != nil {
				return false, err
			}
			running = running || alive
		}
		if !running {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		if err := sleep(ctx, a.o.PollInterval); err != nil {
			return false, err
		}
	}
}
