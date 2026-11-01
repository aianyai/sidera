package adapter

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/codexcfg"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/create"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// createAttempt is what the adapter holds of one session.create attempt. The
// attempt is idempotent: a repeat waits for the run in progress, and once the
// attempt has a final answer every repeat gets it.
type createAttempt struct {
	mu     sync.Mutex // one run of the attempt at a time
	params adapterproto.SessionCreateParams
	final  *adapterproto.SessionCreateResult

	state        create.State
	checkpointed bool
	loadedOn     *appserver.Client // the connection the thread was started or resumed in
	named        bool
	mcpAssumed   bool
	turnsStarted int
	turnAt       time.Time
	began        time.Time
}

// attemptFor returns the record of an attempt, or the error of one whose
// parameters differ from the first request's. The checkpoint is not compared: it
// is what a retry carries and the first request did not.
func (a *App) attemptFor(p adapterproto.SessionCreateParams) (*createAttempt, error) {
	key := p
	key.Checkpoint = nil
	a.mu.Lock()
	defer a.mu.Unlock()
	if rec, ok := a.attempts[p.Attempt]; ok {
		if !reflect.DeepEqual(rec.params, key) {
			return nil, peer.NewError(adapterproto.CodeAttemptConflict, "the attempt was made with other parameters")
		}
		return rec, nil
	}
	rec := &createAttempt{params: key}
	a.attempts[p.Attempt] = rec
	return rec, nil
}

// CreateSession creates a session: a thread in the App Server, named after the
// attempt, with the first prompt as its first turn. It answers created only when
// the prompt is in the thread's record.
func (a *App) CreateSession(ctx context.Context, p adapterproto.SessionCreateParams) (adapterproto.SessionCreateResult, error) {
	rec, err := a.attemptFor(p)
	if err != nil {
		return adapterproto.SessionCreateResult{}, err
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.final != nil {
		return *rec.final, nil
	}
	if !rec.checkpointed && rec.state == (create.State{}) {
		st, err := create.ParseCheckpoint(p.Checkpoint)
		if err != nil {
			return adapterproto.SessionCreateResult{}, peer.NewError(peer.CodeInvalidParams, err.Error())
		}
		rec.state, rec.checkpointed = st, st.ThreadID != "" || st.Failed != ""
	}
	if rec.began.IsZero() {
		rec.began = time.Now()
	}
	res, err := a.runCreate(ctx, rec, p)
	if err != nil {
		return res, err
	}
	if res.Outcome != adapterproto.CreateIndeterminate {
		rec.final = &res
	}
	return res, nil
}

func failed(reason adapterproto.CreateFailure, detail string) adapterproto.SessionCreateResult {
	return adapterproto.SessionCreateResult{Outcome: adapterproto.CreateFailed, Reason: reason, Detail: detail}
}

func indeterminate(code adapterproto.GroundsCode, ref string) adapterproto.SessionCreateResult {
	return adapterproto.SessionCreateResult{
		Outcome: adapterproto.CreateIndeterminate, NativeRef: adapterproto.NativeRef(ref),
		Grounds: &adapterproto.Grounds{Code: code},
	}
}

// runCreate drives the decisions of the create package to an answer.
func (a *App) runCreate(ctx context.Context, rec *createAttempt, p adapterproto.SessionCreateParams) (adapterproto.SessionCreateResult, error) {
	preset, ok := a.presetOf(p.Preset)
	if !ok {
		a.mu.Lock()
		_, known := a.presets[p.Preset]
		a.mu.Unlock()
		if !known {
			return failed(adapterproto.CreateFailPresetUnknown, ""), nil
		}
		return failed(adapterproto.CreateFailPresetInvalid, "see the preset's problems in the probe"), nil
	}

	for {
		if err := ctx.Err(); err != nil {
			// The daemon gave up waiting (or the connection ended): nothing is
			// concluded, and the attempt is asked about again.
			return adapterproto.SessionCreateResult{}, err
		}
		if rec.state.Failed == "" {
			if res, done := a.precheck(ctx, rec); done {
				return res, nil
			}
		}
		client := a.liveClient()
		if client == nil && rec.state.Failed == "" {
			if rec.state.ThreadID != "" {
				return a.lostConnection(rec), nil
			}
			return failed(adapterproto.CreateFailNativeUnavailable, "the App Server connection ended"), nil
		}
		s := a.sessionOf(p.SessionEndpoint)
		if rec.state.ThreadID != "" && rec.loadedOn != client {
			if res, done, err := a.loadThread(ctx, rec, p, preset); done || err != nil {
				return res, err
			}
			continue
		}
		obs := a.observeCreate(ctx, client, s, rec, p)
		d := create.Decide(rec.state, obs, a.opts.Create)
		switch d.Action {
		case create.StartThread:
			if res, done, err := a.startThread(ctx, client, rec, p, preset); done || err != nil {
				return res, err
			}
		case create.PutCheckpoint:
			err := a.conn.CheckpointPut(ctx, adapterproto.CheckpointPutParams{
				Scope: adapterproto.ScopeAttempt, ID: string(p.Attempt), State: rec.state.Checkpoint(),
			})
			if err != nil {
				return adapterproto.SessionCreateResult{}, err
			}
			rec.checkpointed = true
		case create.NameThread:
			if err := client.ThreadSetName(ctx, rec.state.ThreadID, threadName(p.Attempt)); err != nil {
				return a.nativeFailure(ctx, rec, p, err)
			}
			rec.named = true
		case create.AwaitMCP:
			if !a.awaitWake(ctx, s, a.opts.MCPWait, &rec.began) {
				rec.mcpAssumed = true
				a.log.Warn("the channel's MCP server did not report; going on", "attempt", p.Attempt)
			}
		case create.StartTurn:
			rec.turnsStarted++
			rec.turnAt = time.Now()
			_, err := a.startTurn(ctx, client, s, preset, string(p.FirstPrompt.Attempt), p.FirstPrompt.Text)
			if err != nil {
				if ctx.Err() != nil {
					return adapterproto.SessionCreateResult{}, ctx.Err()
				}
				if errors.Is(err, appserver.ErrClosed) {
					return a.lostConnection(rec), nil
				}
				a.log.Warn("the first turn was refused", "attempt", p.Attempt, "error", err)
			}
		case create.AwaitPrompt:
			a.awaitWake(ctx, s, a.opts.PollEvery, nil)
			if err := ctx.Err(); err != nil {
				return adapterproto.SessionCreateResult{}, err
			}
		case create.Ready:
			return a.ready(ctx, client, rec, p, preset), nil
		case create.Indeterminate:
			return indeterminate(adapterproto.GroundsTurnRunning, rec.state.ThreadID), nil
		case create.Fail:
			return a.fail(ctx, rec, p, d.Failure), nil
		}
	}
}

func threadName(attempt adapterproto.AttemptID) string { return "sidera " + string(attempt) }

// precheck refuses what certainly cannot work before the thread exists: it
// answers failed with a reason the daemon maps, and creates nothing. Once the
// thread exists the checks are the loop's own, since a connection that ended then
// is not a failure but an outcome to ask about again.
func (a *App) precheck(ctx context.Context, rec *createAttempt) (adapterproto.SessionCreateResult, bool) {
	if rec.state.ThreadID != "" {
		return adapterproto.SessionCreateResult{}, false
	}
	a.mu.Lock()
	missing := append(missingPrograms(nil), a.missing...)
	a.mu.Unlock()
	if len(missing) > 0 {
		a.log.Warn("cannot create a session", "reason", missing.String())
		return failed(adapterproto.CreateFailDependencyMissing, cut(missing.String(), adapterproto.MaxDetailLen)), true
	}
	if _, err := a.ensureClient(ctx); err != nil {
		a.log.Warn("cannot create a session: the App Server cannot be reached", "error", err)
		a.startReconnect()
		return failed(adapterproto.CreateFailNativeUnavailable, "the App Server cannot be reached: "+shorten(err.Error())), true
	}
	a.mu.Lock()
	signedIn := a.signedIn
	a.mu.Unlock()
	if !signedIn {
		return failed(adapterproto.CreateFailRuntimeAuth, "Codex has no signed-in account"), true
	}
	return adapterproto.SessionCreateResult{}, false
}

// startThread starts the thread and registers it as the session, so that its
// events are reported from the first one.
func (a *App) startThread(ctx context.Context, c *appserver.Client, rec *createAttempt, p adapterproto.SessionCreateParams, preset codexcfg.Preset) (adapterproto.SessionCreateResult, bool, error) {
	s := newSession(p.SessionEndpoint, p.Preset, "", p.ToolAccess.Env)
	th, err := a.threadStart(ctx, c, s, preset)
	if err != nil {
		if errors.Is(err, errUnprotected) {
			return a.failUnprotected(ctx, rec, p, err), true, nil
		}
		if errors.Is(err, appserver.ErrClosed) {
			// No thread is known to exist, and a thread that was started on a
			// connection that ended is gone with it.
			return failed(adapterproto.CreateFailNativeUnavailable, "the App Server connection ended"), true, nil
		}
		res, nerr := a.nativeFailure(ctx, rec, p, err)
		return res, true, nerr
	}
	s.threadID, s.creating = th.ID, true
	rec.state.ThreadID = th.ID
	rec.loadedOn, rec.checkpointed, rec.named = c, false, false
	a.forget(p.SessionEndpoint)
	a.mu.Lock()
	a.sessions[p.SessionEndpoint], a.byThread[th.ID] = s, s
	a.mu.Unlock()
	return adapterproto.SessionCreateResult{}, false, nil
}

// loadThread loads a thread an earlier run of the attempt started, in the current
// connection. A thread the server no longer has was never written (it is written
// when named), so the attempt begins again.
func (a *App) loadThread(ctx context.Context, rec *createAttempt, p adapterproto.SessionCreateParams, preset codexcfg.Preset) (adapterproto.SessionCreateResult, bool, error) {
	c, err := a.ensureClient(ctx)
	if err != nil {
		a.startReconnect()
		return indeterminate(adapterproto.GroundsNativeUnreachable, rec.state.ThreadID), true, nil
	}
	// The session of an earlier run is kept: it holds the books of the turns
	// already reported.
	s := a.sessionOf(p.SessionEndpoint)
	if s == nil || s.threadID != rec.state.ThreadID {
		s = newSession(p.SessionEndpoint, p.Preset, rec.state.ThreadID, p.ToolAccess.Env)
		s.creating = true
	}
	th, err := a.threadResume(ctx, c, s, rec.state.ThreadID, preset)
	switch {
	case errors.Is(err, errUnprotected):
		return a.failUnprotected(ctx, rec, p, err), true, nil
	case err == nil:
		a.mu.Lock()
		s.status = th.Status.Type
		a.sessions[p.SessionEndpoint], a.byThread[s.threadID] = s, s
		a.mu.Unlock()
		rec.loadedOn, rec.named = c, true // a thread that can be resumed was named
		rec.turnAt = time.Time{}
		return adapterproto.SessionCreateResult{}, false, nil
	case appserver.IsThreadGone(err):
		a.forget(p.SessionEndpoint)
		rec.state.ThreadID = ""
		rec.checkpointed, rec.named, rec.turnsStarted = false, false, 0
		return adapterproto.SessionCreateResult{}, false, nil
	}
	return indeterminate(adapterproto.GroundsNativeUnreachable, rec.state.ThreadID), true, nil
}

// lostConnection ends a run whose connection to the server ended. The next run
// loads the thread again in the connection it finds.
func (a *App) lostConnection(rec *createAttempt) adapterproto.SessionCreateResult {
	rec.loadedOn = nil
	a.startReconnect()
	return indeterminate(adapterproto.GroundsNativeUnreachable, rec.state.ThreadID)
}

// forget drops a session from the tables.
func (a *App) forget(ref adapterproto.EndpointRef) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.sessions[ref]; s != nil {
		delete(a.byThread, s.threadID)
		delete(a.mcp, s.threadID)
		delete(a.sessions, ref)
	}
}

// failUnprotected ends the attempt: a thread that cannot be protected from
// reading the data directory is never used, and trying again would meet the same
// refusal. The detail says why, since the owner has to act on it.
func (a *App) failUnprotected(ctx context.Context, rec *createAttempt, p adapterproto.SessionCreateParams, err error) adapterproto.SessionCreateResult {
	res := a.fail(ctx, rec, p, adapterproto.CreateFailNativeError)
	res.Detail = cut(err.Error(), adapterproto.MaxDetailLen)
	return res
}

// nativeFailure answers a failure of the server that the adapter cannot tell more
// about: a lost connection is an outcome to ask about again, a refusal is the
// attempt's final failure.
func (a *App) nativeFailure(ctx context.Context, rec *createAttempt, p adapterproto.SessionCreateParams, err error) (adapterproto.SessionCreateResult, error) {
	switch {
	case ctx.Err() != nil:
		return adapterproto.SessionCreateResult{}, ctx.Err()
	case errors.Is(err, appserver.ErrClosed):
		return a.lostConnection(rec), nil
	}
	a.log.Warn("the App Server refused a step of a creation", "attempt", p.Attempt, "error", err)
	return a.fail(ctx, rec, p, adapterproto.CreateFailNativeError), nil
}

// observeCreate reads what the decision needs: the thread's name and channel, and
// whether the prompt is in the record.
func (a *App) observeCreate(ctx context.Context, c *appserver.Client, s *session, rec *createAttempt, p adapterproto.SessionCreateParams) create.Observed {
	obs := create.Observed{
		Checkpointed: rec.checkpointed, Named: rec.named, TurnsStarted: rec.turnsStarted,
	}
	since := rec.began
	if !rec.turnAt.IsZero() {
		since = rec.turnAt
	}
	obs.Waited = time.Since(since)
	if s == nil || c == nil || rec.state.ThreadID == "" {
		return obs
	}
	a.mu.Lock()
	state := a.mcp[rec.state.ThreadID]
	prompt := s.persisted.has(string(p.FirstPrompt.Attempt))
	obs.TurnRunning = s.busy()
	a.mu.Unlock()
	switch {
	case state == appserver.MCPFailed:
		obs.MCP = create.MCPFailed
	case state == appserver.MCPReady || rec.mcpAssumed:
		obs.MCP = create.MCPReady
	}
	if rec.named && !prompt {
		// The event may have been missed; the record is the proof.
		turns, _, err := readTurns(ctx, c, rec.state.ThreadID, 1, func(t appserver.Turn) bool { return hasPrompt(t, p.FirstPrompt.Attempt) })
		if err == nil {
			for _, t := range turns {
				if hasPrompt(t, p.FirstPrompt.Attempt) {
					prompt = true
				}
			}
			if !prompt && len(turns) > 0 {
				obs.TurnRunning = obs.TurnRunning || turns[0].Running()
			}
		}
	}
	obs.PromptRecorded = prompt
	return obs
}

// awaitWake waits for an event of the session, for the time given or for the
// context to end, and says whether an event came. With since, a wait that ran out
// is measured from that moment instead (the caller bounds a whole phase).
func (a *App) awaitWake(ctx context.Context, s *session, d time.Duration, since *time.Time) bool {
	if s == nil {
		return false
	}
	if since != nil {
		left := d - time.Since(*since)
		if left < 0 {
			left = 0
		}
		d = left
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-s.wake:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// ready answers created. The turn of the first prompt may already be over when the
// answer is given, which the daemon cannot learn from a notification that came
// before it, so the answer says so.
func (a *App) ready(ctx context.Context, c *appserver.Client, rec *createAttempt, p adapterproto.SessionCreateParams, preset codexcfg.Preset) adapterproto.SessionCreateResult {
	ended := false
	if turns, _, err := readTurns(ctx, c, rec.state.ThreadID, 1, func(t appserver.Turn) bool { return hasPrompt(t, p.FirstPrompt.Attempt) }); err == nil {
		for _, t := range turns {
			if hasPrompt(t, p.FirstPrompt.Attempt) {
				ended = !t.Running()
			}
		}
	}
	a.mu.Lock()
	if s := a.sessions[p.SessionEndpoint]; s != nil {
		s.creating = false
	}
	a.mu.Unlock()
	return adapterproto.SessionCreateResult{
		Outcome:        adapterproto.CreateCreated,
		NativeRef:      adapterproto.NativeRef(rec.state.ThreadID),
		Frontends:      preset.Frontends(),
		FirstTurnEnded: adapterproto.TrueOnly(ended),
	}
}

// fail ends the attempt with a final failure: the reason is stored first, so that
// a repeat of the attempt, after a crash or a lost answer, gets the same one, and
// the thread is archived so that nothing is left running.
func (a *App) fail(ctx context.Context, rec *createAttempt, p adapterproto.SessionCreateParams, reason adapterproto.CreateFailure) adapterproto.SessionCreateResult {
	if rec.state.Failed == "" {
		rec.state.Failed = reason
		if rec.state.ThreadID != "" {
			if err := a.conn.CheckpointPut(ctx, adapterproto.CheckpointPutParams{
				Scope: adapterproto.ScopeAttempt, ID: string(p.Attempt), State: rec.state.Checkpoint(),
			}); err != nil {
				a.log.Warn("cannot store the failure of an attempt", "attempt", p.Attempt, "error", err)
			}
		}
	}
	if rec.state.ThreadID != "" {
		if c := a.liveClient(); c != nil {
			actx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := c.ThreadArchive(actx, rec.state.ThreadID); err != nil {
				a.log.Warn("cannot archive the thread of a failed creation", "error", err)
			}
			cancel()
		}
	}
	a.forget(p.SessionEndpoint)
	detail := ""
	if reason == adapterproto.CreateFailDependencyMissing {
		detail = "the channel's MCP server did not start in the thread"
	}
	return failed(reason, detail)
}
