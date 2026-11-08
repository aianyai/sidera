package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/reconcile"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/record"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/runtime/proc"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/turn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

func refused(reason adapterproto.PromptRefusal) (adapterproto.PromptDeliverResult, error) {
	return adapterproto.PromptDeliverResult{Stage: adapterproto.PromptRefused, Reason: reason}, nil
}

func unknownEndpoint() error {
	return peer.NewError(adapterproto.CodeUnknownEndpoint, "the adapter holds no such session")
}

// DeliverPrompt puts a prompt into a session by starting a turn process for it.
// Delivery is idempotent per attempt; a session has one writer, so a prompt is
// refused as busy while a turn process lives; and the attempt's checkpoint is
// stored before the process is started, so that "no checkpoint" proves nothing was.
func (a *App) DeliverPrompt(ctx context.Context, p adapterproto.PromptDeliverParams) (adapterproto.PromptDeliverResult, error) {
	if err := a.requireStarted(); err != nil {
		return adapterproto.PromptDeliverResult{}, err
	}
	if as := a.attachedSession(p.Endpoint); as != nil {
		return a.deliverAttached(ctx, as, p)
	}
	s := a.session(p.Endpoint)
	if s == nil {
		return adapterproto.PromptDeliverResult{}, unknownEndpoint()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if exists, err := a.fileExists(s.file()); err != nil {
		return adapterproto.PromptDeliverResult{}, err
	} else if !exists {
		return refused(adapterproto.RefusalSessionLost)
	}
	sum := digest(p.Text)
	if s.turn != nil && s.turn.Attempt == string(p.Attempt) && s.turn.Digest != sum {
		return adapterproto.PromptDeliverResult{}, peer.NewError(adapterproto.CodeAttemptConflict, "the attempt was made with another text")
	}
	// The process table is read before the session file: a process seen as gone
	// has written everything it will, so that no process and no prompt in the file
	// read afterwards prove that the prompt never started.
	list, err := a.o.Table.List()
	if err != nil {
		return adapterproto.PromptDeliverResult{}, err
	}
	recordedAlive := false
	if s.turn != nil && !s.turn.Done() {
		if recordedAlive, err = a.alive(s.turn.Process); err != nil {
			return adapterproto.PromptDeliverResult{}, err
		}
	}
	// A prompt in the record was delivered, whoever delivered it.
	ix := record.NewIndex(s.id, string(p.Attempt))
	if _, err := a.scan(s.file(), ix); err != nil {
		return adapterproto.PromptDeliverResult{}, fmt.Errorf("read the session file: %w", err)
	}
	if ix.UserSeen(string(p.Attempt)) {
		return adapterproto.PromptDeliverResult{Stage: adapterproto.PromptPersisted}, nil
	}

	// The session's writers: the recorded turn process if it lives, and every
	// turn process of the session the process table shows, each once.
	var recorded *turn.Process
	if s.turn != nil && !s.turn.Done() {
		if recordedAlive {
			recorded = &s.turn.Process
		} else if !a.tick(ctx, s) {
			// Its end is reported before another turn replaces it.
			return adapterproto.PromptDeliverResult{}, errors.New("the last turn of the session cannot be settled")
		}
	}
	writers := map[int]turn.Process{}
	for _, info := range a.turnProcesses(list, s.id) {
		writers[info.PID] = processOf(info)
	}
	if recorded != nil {
		if w, ok := writers[recorded.PID]; ok && !w.Same(*recorded) {
			return adapterproto.PromptDeliverResult{}, fmt.Errorf("process %d of the session is not the recorded one", recorded.PID)
		}
		writers[recorded.PID] = *recorded
	}
	switch len(writers) {
	case 0:
	case 1:
		for _, w := range writers {
			switch {
			case s.turn != nil && !s.turn.Done() && s.turn.Process.Same(w) && s.turn.Attempt == string(p.Attempt):
				// The same attempt, delivered and in flight.
				return adapterproto.PromptDeliverResult{Stage: adapterproto.PromptAccepted}, nil
			case s.turn == nil || s.turn.Done() || !s.turn.Process.Same(w):
				// A process that was started and never recorded is the session's writer
				// all the same, and its turn is followed like any other.
				s.turn = adoptUnknown(w)
				if err := a.persistSession(ctx, s); err != nil {
					a.log.Warn("the checkpoint of a session was not stored", "endpoint", s.endpoint, "error", err)
				}
				a.watch(s)
			}
		}
		return refused(adapterproto.RefusalBusy)
	default:
		return refused(adapterproto.RefusalControlUnknown)
	}

	if err := a.putCheckpoint(ctx, adapterproto.ScopeAttempt, string(p.Attempt), dispatchCheckpoint{V: checkpointVersion, Stage: "dispatching", Session: s.id}); err != nil {
		return adapterproto.PromptDeliverResult{}, err
	}
	preset, err := a.loadPreset(s.preset, s.choice)
	if err != nil {
		a.log.Warn("a prompt cannot be delivered", "endpoint", s.endpoint, "error", err)
		return refused(adapterproto.RefusalNativeUnavailable)
	}
	started, err := a.launch(turnLaunch{
		preset: resolvedPreset{Preset: preset, Cwd: s.cwd, RecordDir: s.recordDir}, session: s.id, resume: true,
		attempt: string(p.Attempt), text: p.Text, toolEnv: s.toolEnv,
	})
	if errors.Is(err, proc.ErrStartedUnidentified) {
		// The process runs with the prompt, so the outcome is unknown and not a
		// refusal; the daemon reconciles, and the next look at the session finds
		// the process by its command line.
		return adapterproto.PromptDeliverResult{}, fmt.Errorf("a turn process was started and cannot be identified: %w", err)
	}
	if err != nil {
		a.log.Warn("a turn process could not be started", "endpoint", s.endpoint, "error", err)
		return refused(adapterproto.RefusalNativeUnavailable)
	}
	s.turn = &turn.State{Attempt: string(p.Attempt), Process: startedProcess(started), Digest: sum, PersistNotice: true}
	s.idx = nil
	a.watch(s)
	if err := a.persistSession(ctx, s); err != nil {
		return adapterproto.PromptDeliverResult{}, err
	}
	return adapterproto.PromptDeliverResult{Stage: adapterproto.PromptAccepted}, nil
}

// Reconcile says whether a prompt attempt reached the session record; see
// package reconcile for the rules and for what the facts are.
func (a *App) Reconcile(_ context.Context, p adapterproto.ReconcileParams) (adapterproto.ReconcileResult, error) {
	if err := a.requireStarted(); err != nil {
		return adapterproto.ReconcileResult{}, err
	}
	if as := a.attachedSession(p.Endpoint); as != nil {
		return a.reconcileAttached(as, p)
	}
	s := a.session(p.Endpoint)
	if s == nil {
		return adapterproto.ReconcileResult{}, unknownEndpoint()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	attempt := string(p.Attempt)
	in := reconcile.Input{Dispatched: p.Checkpoint != nil || (s.turn != nil && s.turn.Attempt == attempt)}

	// The process table is read before the file: a process seen as gone has
	// written everything it will, so the file read afterwards is complete.
	list, err := a.o.Table.List()
	if err != nil {
		return adapterproto.ReconcileResult{}, err
	}
	// The recorded turn process counts as running even when its command line is
	// not the one the listing search looks for, a claude found at another path.
	recordedAlive := false
	if s.turn != nil && s.turn.Process.PID != 0 {
		if recordedAlive, err = a.alive(s.turn.Process); err != nil {
			return adapterproto.ReconcileResult{}, err
		}
	}
	in.SessionBusy = recordedAlive || len(a.turnProcesses(list, s.id)) > 0
	if s.turn != nil && s.turn.Attempt == attempt {
		in.TurnAlive = recordedAlive
		in.Interrupted = s.turn.Interrupted
	}

	ix := record.NewIndex(s.id, attempt)
	exists, err := a.scan(s.file(), ix)
	in.RecordReadable = exists && err == nil
	if err != nil {
		a.log.Warn("the session file cannot be read", "endpoint", s.endpoint, "error", err)
	}
	in.RecordRecognized = ix.Recognized()
	in.InRecord = in.RecordReadable && ix.UserSeen(attempt)
	if !in.TurnAlive {
		in.Outcome = a.lastResult(s.id, attempt)
	}
	v := reconcile.Decide(in)
	res := adapterproto.ReconcileResult{Verdict: v.Verdict, Grounds: adapterproto.Grounds{Code: v.Grounds}}
	if v.Verdict == adapterproto.VerdictFound {
		res.Turn = &adapterproto.TurnReport{Ref: adapterproto.TurnRef(attempt), State: v.Turn}
	}
	return res, nil
}
