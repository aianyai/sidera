package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/claudeargs"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/create"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/record"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/runtime/proc"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/turn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// recordedProcess is what a commit records of the attempt's turn process besides
// its PID and start second.
type recordedProcess struct {
	startKey                     string
	containerKind, containerName string
}

// attempt is a creation attempt in memory: what was last committed. The
// checkpoint of the attempt at the daemon is the same, written first, so this is
// a cache that also keeps two requests for one attempt from running side by side.
type attempt struct {
	mu sync.Mutex
	cp attemptCheckpoint
	st create.State
}

func (a *App) attemptFor(id adapterproto.AttemptID) *attempt {
	a.mu.Lock()
	defer a.mu.Unlock()
	at, ok := a.attempts[id]
	if !ok {
		at = &attempt{}
		a.attempts[id] = at
	}
	return at
}

var frontends = []adapterproto.Frontend{adapterproto.FrontendCLI, adapterproto.FrontendMCP}

func failed(reason adapterproto.CreateFailure, detail string) adapterproto.SessionCreateResult {
	return adapterproto.SessionCreateResult{Outcome: adapterproto.CreateFailed, Reason: reason, Detail: clip(detail)}
}

// clip keeps a detail within the protocol's bound.
func clip(s string) string {
	if len(s) > adapterproto.MaxDetailLen {
		return strings.ToValidUTF8(s[:adapterproto.MaxDetailLen], "")
	}
	return s
}

// CreateSession starts a session, or finds out what became of an earlier try.
// The flow is the create package's Decide, driven here: observe the process table
// and then the record, decide, do what was decided and commit the new state
// before the next step, until the first prompt is in the record or the attempt
// has failed for good.
func (a *App) CreateSession(ctx context.Context, p adapterproto.SessionCreateParams) (adapterproto.SessionCreateResult, error) {
	if err := a.requireStarted(); err != nil {
		return adapterproto.SessionCreateResult{}, err
	}
	at := a.attemptFor(p.Attempt)
	at.mu.Lock()
	defer at.mu.Unlock()

	if at.st.AttemptID == "" {
		res, done, err := a.loadAttempt(ctx, at, p)
		if done || err != nil {
			return res, err
		}
	} else if err := conflict(at.cp, p); err != nil {
		return adapterproto.SessionCreateResult{}, err
	}
	return a.driveCreate(ctx, at, p)
}

// conflict is the error for the same attempt asked again with other parameters.
func conflict(cp attemptCheckpoint, p adapterproto.SessionCreateParams) error {
	if cp.Preset == p.Preset && cp.SessionEndpoint == string(p.SessionEndpoint) &&
		cp.FirstPrompt == string(p.FirstPrompt.Attempt) && cp.FirstDigest == digest(p.FirstPrompt.Text) {
		return nil
	}
	return peer.NewError(adapterproto.CodeAttemptConflict, "the attempt was made with other parameters")
}

// loadAttempt takes the attempt up from the checkpoint the daemon sent, or
// begins one: it checks what a creation needs, allocates the session uuid and
// commits the intent before anything native is touched. done is true when the
// result is the answer, a failure found before anything was started.
func (a *App) loadAttempt(ctx context.Context, at *attempt, p adapterproto.SessionCreateParams) (res adapterproto.SessionCreateResult, done bool, err error) {
	if p.Checkpoint != nil {
		cp, err := decodeAttemptCheckpoint(p.Checkpoint)
		if err != nil {
			return res, true, err
		}
		if cp.Attempt != string(p.Attempt) {
			return res, true, peer.NewError(adapterproto.CodeAttemptConflict, "the checkpoint is of another attempt")
		}
		if err := conflict(cp, p); err != nil {
			return res, true, err
		}
		at.cp, at.st = cp, cp.state()
		return res, false, nil
	}

	claude, sidera, _ := a.executables()
	switch {
	case claude == "":
		return failed(adapterproto.CreateFailDependencyMissing, "claude was not found"), true, nil
	case sidera == "":
		return failed(adapterproto.CreateFailDependencyMissing, "sidera was not found"), true, nil
	}
	status, err := a.claudeStatus(ctx, claude)
	switch {
	case err != nil:
		return failed(adapterproto.CreateFailNativeUnavailable, err.Error()), true, nil
	case !status.LoggedIn:
		return failed(adapterproto.CreateFailRuntimeAuth, "claude is not logged in"), true, nil
	}
	preset, perr := a.resolvePreset(p.Preset, status.ProjectsDirectory)
	if perr != nil {
		return failed(perr.failure, perr.Error()), true, nil
	}
	session, err := newUUID(a.o.Rand)
	if err != nil {
		return res, true, err
	}
	st, err := create.NewIntent(create.AttemptID(p.Attempt), create.SessionID(session), create.PromptID(p.FirstPrompt.Attempt), preset.RecordDir)
	if err != nil {
		return res, true, err
	}
	cp := attemptCheckpoint{
		V: checkpointVersion, Attempt: string(p.Attempt), Session: session,
		FirstPrompt: string(p.FirstPrompt.Attempt), FirstDigest: digest(p.FirstPrompt.Text),
		RecordDir: preset.RecordDir, Cwd: preset.Cwd, Preset: p.Preset, SessionEndpoint: string(p.SessionEndpoint),
	}.withState(st)
	if err := a.putCheckpoint(ctx, adapterproto.ScopeAttempt, cp.Attempt, cp); err != nil {
		return res, true, err
	}
	at.cp, at.st = cp, st
	return res, false, nil
}

// commit stores a new state of the attempt, then keeps it in memory: the state
// that was stored is the state that is acted on.
//
// rec is the process the new state records, when it changes: its start key and
// the container it was started in, empty for a process found and not started.
func (a *App) commit(ctx context.Context, at *attempt, st create.State, rec *recordedProcess) error {
	cp := at.cp.withState(st)
	if rec != nil {
		cp.StartKey, cp.ContainerKind, cp.ContainerName = rec.startKey, rec.containerKind, rec.containerName
	}
	if err := a.putCheckpoint(ctx, adapterproto.ScopeAttempt, cp.Attempt, cp); err != nil {
		return err
	}
	at.cp, at.st = cp, st
	return nil
}

func (a *App) driveCreate(ctx context.Context, at *attempt, p adapterproto.SessionCreateParams) (adapterproto.SessionCreateResult, error) {
	for {
		if err := ctx.Err(); err != nil {
			return adapterproto.SessionCreateResult{}, err
		}
		obs, keys, err := a.observeCreate(at.st, at.cp.StartKey)
		if err != nil {
			return adapterproto.SessionCreateResult{}, err
		}
		d, err := create.Decide(at.st, obs, a.o.Create, a.o.Now())
		if err != nil {
			return adapterproto.SessionCreateResult{}, err
		}
		switch d.Action {
		case create.ActionStartNew, create.ActionStartResume:
			started, res, err := a.startFirstTurn(at, p, d.Action == create.ActionStartResume)
			if err != nil || res != nil {
				if res != nil {
					return *res, nil
				}
				return adapterproto.SessionCreateResult{}, err
			}
			st, err := at.st.Started(create.Process{PID: started.PID, StartUnix: started.StartUnix})
			if err != nil {
				return adapterproto.SessionCreateResult{}, err
			}
			rec := &recordedProcess{startKey: started.StartKey, containerKind: string(started.Container.Kind), containerName: started.Container.Name}
			if err := a.commit(ctx, at, st, rec); err != nil {
				return adapterproto.SessionCreateResult{}, err
			}
		case create.ActionRecordProcess:
			st, err := at.st.Started(d.Process)
			if err != nil {
				return adapterproto.SessionCreateResult{}, err
			}
			if err := a.commit(ctx, at, st, &recordedProcess{startKey: keys[d.Process.PID]}); err != nil {
				return adapterproto.SessionCreateResult{}, err
			}
		case create.ActionMarkUUIDSeen:
			st, err := at.st.UUIDSeen()
			if err != nil {
				return adapterproto.SessionCreateResult{}, err
			}
			if err := a.commit(ctx, at, st, nil); err != nil {
				return adapterproto.SessionCreateResult{}, err
			}
		case create.ActionWait:
			if err := sleep(ctx, a.o.PollInterval); err != nil {
				return adapterproto.SessionCreateResult{}, err
			}
		case create.ActionMarkReady:
			st, err := at.st.Ready()
			if err != nil {
				return adapterproto.SessionCreateResult{}, err
			}
			if err := a.commit(ctx, at, st, nil); err != nil {
				return adapterproto.SessionCreateResult{}, err
			}
			return a.finishCreate(ctx, at, p)
		case create.ActionFail, create.ActionControlUnknown:
			st, err := at.st.Failed(d.Reason)
			if err != nil {
				return adapterproto.SessionCreateResult{}, err
			}
			if err := a.commit(ctx, at, st, nil); err != nil {
				return adapterproto.SessionCreateResult{}, err
			}
			return failedFor(d.Reason), nil
		case create.ActionDone:
			if d.Reason == create.ReasonAlreadyFailed {
				return failedFor(d.FailReason), nil
			}
			return a.finishCreate(ctx, at, p)
		default:
			return adapterproto.SessionCreateResult{}, fmt.Errorf("create: unknown action %v", d.Action)
		}
	}
}

// failedFor is the answer for an attempt that failed with the reason.
func failedFor(reason create.Reason) adapterproto.SessionCreateResult {
	failure, _ := reason.CreateFailure() // only a reason that fails an attempt is ever stored
	return failed(failure, reason.String())
}

// startFirstTurn starts the turn process of the attempt. A launch that cannot be
// made started nothing, so it is answered as a failure without ending the
// attempt: a repeated request tries again. A process that started but cannot be
// identified is an error, never a failure.
func (a *App) startFirstTurn(at *attempt, p adapterproto.SessionCreateParams, resume bool) (proc.Started, *adapterproto.SessionCreateResult, error) {
	preset, err := a.loadPreset(at.cp.Preset)
	if err != nil {
		return proc.Started{}, nil, err
	}
	started, err := a.launch(turnLaunch{
		preset: resolvedPreset{Preset: preset, Cwd: at.cp.Cwd, RecordDir: at.cp.RecordDir}, session: at.cp.Session, resume: resume,
		attempt: at.cp.FirstPrompt, text: p.FirstPrompt.Text, toolEnv: p.ToolAccess.Env,
	})
	var missing errDependencyMissing
	switch {
	case errors.Is(err, proc.ErrStartedUnidentified):
		// The process runs with the first prompt, so the attempt is neither failed
		// nor reported: it ends as an error, and the repeated request finds the
		// process by the uuid on its command line.
		return proc.Started{}, nil, fmt.Errorf("the first turn was started and cannot be identified: %w", err)
	case errors.As(err, &missing):
		res := failed(adapterproto.CreateFailDependencyMissing, err.Error())
		return proc.Started{}, &res, nil
	case err != nil:
		res := failed(adapterproto.CreateFailNativeUnavailable, err.Error())
		return proc.Started{}, &res, nil
	}
	return started, nil, nil
}

// loadPreset reads the values of a preset that an attempt in progress needs
// again; one that was removed or became invalid since is an error and not a
// failed attempt, because a process of the attempt may be running.
func (a *App) loadPreset(name string) (claudeargs.Preset, error) {
	a.mu.Lock()
	values, ok := a.presets[name]
	a.mu.Unlock()
	if !ok {
		return claudeargs.Preset{}, fmt.Errorf("preset %q is no longer configured", name)
	}
	preset, problems := claudeargs.ParsePreset(values)
	if len(problems) > 0 {
		return claudeargs.Preset{}, fmt.Errorf("preset %q is no longer valid: %s", name, strings.Join(problems, "; "))
	}
	return preset, nil
}

// observeCreate gathers what Decide needs: the process table first, then the
// session file. keys are the start keys of the processes seen, by PID. startKey is
// the start key recorded with the attempt's process.
func (a *App) observeCreate(st create.State, startKey string) (create.Observations, map[int]string, error) {
	list, err := a.o.Table.List()
	if err != nil {
		return create.Observations{}, nil, err
	}
	var obs create.Observations
	if !st.Process.IsZero() {
		// The recorded process runs when the table says its identity does; then it
		// has the start the state records, whatever second a new reading of its
		// start gives (on Linux the seconds derive from the boot time).
		alive, err := a.alive(turn.Process{PID: st.Process.PID, StartKey: startKey})
		if err != nil {
			return create.Observations{}, nil, err
		}
		obs.Recorded = create.Lookup{PID: st.Process.PID, Alive: alive}
		if alive {
			obs.Recorded.StartUnix = st.Process.StartUnix
		}
	}
	keys := map[int]string{}
	for _, info := range a.turnProcesses(list, string(st.SessionID)) {
		unix := info.StartUnix
		if info.PID == st.Process.PID && info.StartKey == startKey {
			unix = st.Process.StartUnix
		}
		obs.Running = append(obs.Running, create.Process{PID: info.PID, StartUnix: unix})
		keys[info.PID] = info.StartKey
	}
	ix := record.NewIndex(string(st.SessionID), string(st.FirstPromptID))
	exists, err := a.scan(record.File(st.RecordDir, string(st.SessionID)), ix)
	if err != nil {
		return create.Observations{}, nil, fmt.Errorf("read the session file: %w", err)
	}
	obs.Record = create.SessionRecord{
		Dir: st.RecordDir, Exists: exists,
		HasSessionID: ix.SessionSeen(), HasFirstPrompt: ix.UserSeen(string(st.FirstPromptID)),
	}
	return obs, keys, nil
}

// finishCreate answers a ready attempt: it opens the session in the table,
// reports the first turn and answers created. The session's checkpoint is stored
// before anything is reported, so a crash after the first turn.started leaves a
// state to pick the turn up from. When the first turn's process is already gone
// the answer says so (first_turn_ended), which the daemon takes as the end of that
// turn however late the answer arrives.
func (a *App) finishCreate(ctx context.Context, at *attempt, p adapterproto.SessionCreateParams) (adapterproto.SessionCreateResult, error) {
	cp := at.cp
	a.mu.Lock()
	s := a.sessions[p.SessionEndpoint]
	fresh := s == nil
	if fresh {
		s = &session{
			endpoint: p.SessionEndpoint, id: cp.Session, preset: cp.Preset, cwd: cp.Cwd, recordDir: cp.RecordDir,
			toolEnv: p.ToolAccess.Env,
			turn: &turn.State{
				Attempt: cp.FirstPrompt, Digest: cp.FirstDigest,
				Process: cp.process(),
			},
		}
		a.sessions[s.endpoint] = s
	}
	a.mu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if fresh {
		if err := a.persistSession(ctx, s); err != nil {
			a.forgetLocked(s)
			return adapterproto.SessionCreateResult{}, err
		}
	}
	a.tick(ctx, s)
	a.watch(s)
	// The first turn is over if its state says so, or if a later turn replaced it.
	over := s.turn.Attempt != cp.FirstPrompt || s.turn.Done()
	return adapterproto.SessionCreateResult{
		Outcome: adapterproto.CreateCreated, NativeRef: adapterproto.NativeRef(cp.Session),
		Process:        identity(cp.process()),
		Frontends:      frontends,
		FirstTurnEnded: adapterproto.TrueOnly(over),
	}, nil
}

// forgetLocked drops a session whose lock the caller holds from the table.
func (a *App) forgetLocked(s *session) {
	a.mu.Lock()
	if a.sessions[s.endpoint] == s {
		delete(a.sessions, s.endpoint)
	}
	a.mu.Unlock()
}

// sleep waits for d or the end of ctx.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
