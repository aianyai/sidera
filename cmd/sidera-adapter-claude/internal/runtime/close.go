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

	// A close and an interrupt of one session are served one at a time.
	s.interruptMu.Lock()
	defer s.interruptMu.Unlock()

	s.mu.Lock()
	writers, err := a.writers(s.id, s.turn)
	s.mu.Unlock()
	if err != nil {
		a.log.Warn("the processes of a session cannot be looked at", "endpoint", s.endpoint, "error", err)
		return unreachable, nil
	}

	gone, err := a.interruptAndWait(ctx, s, writers, a.o.CloseWait)
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
	gone, err := a.interruptAndWait(ctx, nil, writers, a.o.CloseWait)
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
// waits up to wait for all of them to end. It reports whether they all did. s is
// the session the processes belong to, nil for a session the adapter does not
// hold; the turn process of s is interrupted by interruptProcess.
func (a *App) interruptAndWait(ctx context.Context, s *session, procs []turn.Process, wait time.Duration) (bool, error) {
	for _, p := range procs {
		alive, err := a.alive(p)
		if err != nil {
			return false, err
		}
		if !alive {
			continue
		}
		// A process that ended since it was looked at is what waitEnded is for.
		if err := a.interruptProcess(ctx, s, p); err != nil && !errors.Is(err, proc.ErrNotRunning) {
			return false, err
		}
	}
	return a.waitEnded(ctx, procs, wait)
}

// interruptProcess interrupts a process the way Ctrl-C does. A signal that fails
// is returned as it came, proc.ErrNotRunning for a process that ended since it
// was looked at.
//
// The turn process of s is held to two facts. The turn is marked interrupted, and
// the mark stored, before the signal, so that however soon the process ends, and
// even if this adapter does not live to see it, its end is reported as
// interrupted; a signal that fails takes the mark back, because the turn was
// not interrupted. And once a signal has been delivered to the process, s
// remembers it in memory, so that no second signal is sent to it and the mark
// is never taken for proof of one. The caller holds s.interruptMu, so that no
// other interrupt sees the mark of a signal that is still on its way.
func (a *App) interruptProcess(ctx context.Context, s *session, p turn.Process) error {
	if s == nil {
		return a.signal(ctx, p)
	}
	s.mu.Lock()
	t := s.turn
	if t == nil || t.Done() || !t.Process.Same(p) {
		s.mu.Unlock()
		return a.signal(ctx, p)
	}
	if s.signalled.Same(p) {
		s.mu.Unlock()
		return nil
	}
	t.Interrupted = true
	a.persistTurn(ctx, s)
	s.mu.Unlock()

	err := a.signal(ctx, p)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.signalled = p
		return nil
	}
	if t := s.turn; t != nil && !t.Done() && t.Process.Same(p) {
		t.Interrupted = false
		a.persistTurn(ctx, s)
	}
	return err
}

// persistTurn stores the session's checkpoint; a store that fails is logged, and
// the next change of the turn stores it again. The caller holds s.mu.
func (a *App) persistTurn(ctx context.Context, s *session) {
	if err := a.persistSession(ctx, s); err != nil {
		a.log.Warn("the checkpoint of a session was not stored", "endpoint", s.endpoint, "error", err)
	}
}

// signal interrupts a process the way Ctrl-C does, SIGINT or CTRL_C_EVENT on Windows.
func (a *App) signal(ctx context.Context, p turn.Process) error {
	return a.o.Signaller.Interrupt(ctx, proc.Identity{PID: p.PID, StartKey: p.StartKey})
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
