package adapter

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/codexcfg"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/reconcile"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// startTurn starts a turn with a prompt. The prompt's attempt is the client id the
// server keeps on its record, which is how it is found again; the text is the
// daemon's, unchanged. The turn is tied to the attempt before its start event can
// be reported, so that the event names it, and the thread counts as busy from the
// answer on.
func (a *App) startTurn(ctx context.Context, c *appserver.Client, s *session, preset codexcfg.Preset, attempt, text string) (appserver.Turn, error) {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	ps := &pendingStart{resolved: make(chan struct{})}
	a.mu.Lock()
	s.pending = ps
	a.mu.Unlock()
	turn, err := c.TurnStart(ctx, appserver.TurnStartParams{
		ThreadID: s.threadID, ClientUserMessageID: attempt, Input: []appserver.UserInput{appserver.TextInput(text)},
		CWD: preset.CWD, ApprovalPolicy: preset.ApprovalPolicy, SandboxPolicy: preset.SandboxPolicy(),
		Model: preset.Model, Effort: preset.Effort,
	})
	a.mu.Lock()
	s.pending = nil
	if err == nil {
		rec := s.turn(turn.ID)
		rec.attempt = attempt
		// The answer says the turn runs, and its events may come much later (the
		// server holds them while the thread's MCP servers start). Until they do,
		// nothing else may take the thread for idle, or the same prompt would be
		// started again. A turn that already ended is over, whatever the answer says.
		if turn.Running() && !rec.ended {
			s.activeTurn = turn.ID
		}
	}
	a.mu.Unlock()
	close(ps.resolved)
	return turn, err
}

// DeliverPrompt puts a prompt into a session as a new turn. A session that is
// running a turn refuses it: the adapter is the thread's only writer and does not
// queue behind or steer a running turn. The answer is accepted, and the prompt is
// reported persisted when it reaches the record, or persisted at once when it did
// already.
func (a *App) DeliverPrompt(ctx context.Context, p adapterproto.PromptDeliverParams) (adapterproto.PromptDeliverResult, error) {
	s := a.sessionOf(p.Endpoint)
	if s == nil {
		return adapterproto.PromptDeliverResult{}, unknownEndpoint(p.Endpoint)
	}
	if res, done, err := a.repeatedDelivery(s, p); done {
		return res, err
	}
	if s.attached {
		return a.deliverAttached(ctx, s, p)
	}
	preset, ok := a.presetOf(s.preset)
	if !ok {
		return refused(adapterproto.RefusalNativeUnavailable), nil
	}
	c := a.liveClient()
	if c == nil {
		if cc, err := a.ensureClient(ctx); err == nil {
			c = cc
		}
	}
	a.mu.Lock()
	signedIn, lost, status := a.signedIn, s.lost, s.status
	a.mu.Unlock()
	switch {
	case lost:
		return refused(adapterproto.RefusalSessionLost), nil
	case c == nil:
		return refused(adapterproto.RefusalNativeUnavailable), nil
	case !signedIn:
		return refused(adapterproto.RefusalRuntimeAuth), nil
	}
	// A thread the server unloaded is loaded again, with its configuration, before
	// anything is asked of it: a turn on a thread loaded any other way would run
	// without the session's credentials. Loading also brings the books on its turns
	// up to date, so the busy check comes after it.
	if status == appserver.ThreadNotLoaded {
		if res, ok := a.reload(ctx, c, s, a.loadUnloaded); !ok {
			return res, nil
		}
	}
	a.mu.Lock()
	busy := s.busy()
	a.mu.Unlock()
	if busy {
		return refused(adapterproto.RefusalBusy), nil
	}

	turn, err := a.startTurn(ctx, c, s, preset, string(p.Attempt), p.Text)
	if err != nil && appserver.IsThreadGone(err) {
		// "Not found" from turn/start does not show that the thread is gone: the
		// server may have unloaded it. thread/resume, which reloads a thread that
		// exists, says so for one that does not.
		res, ok := a.reload(ctx, c, s, a.attach)
		if !ok {
			return res, nil
		}
		turn, err = a.startTurn(ctx, c, s, preset, string(p.Attempt), p.Text)
	}
	if err != nil {
		return a.refusalOrUnknown(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	res := adapterproto.PromptDeliverResult{Stage: adapterproto.PromptAccepted}
	rec := &promptRec{text: p.Text, result: res, turnID: turn.ID}
	if s.persisted.has(string(p.Attempt)) {
		// The record was written before the answer came.
		res.Stage = adapterproto.PromptPersisted
		rec.result, rec.persistedSent = res, true
	}
	s.prompts[p.Attempt] = rec
	return res, nil
}

// loader loads a session's thread: attach, or loadUnloaded.
type loader func(ctx context.Context, c *appserver.Client, s *session, report bool) (adapterproto.AttachState, bool)

// reload loads the session's thread in the connection. When it cannot, ok is false
// and the result is the refusal to answer the delivery with.
func (a *App) reload(ctx context.Context, c *appserver.Client, s *session, load loader) (res adapterproto.PromptDeliverResult, ok bool) {
	switch state, _ := load(ctx, c, s, true); state {
	case adapterproto.AttachAttached:
		return adapterproto.PromptDeliverResult{}, true
	case adapterproto.AttachLost:
		return refused(adapterproto.RefusalSessionLost), false
	}
	return refused(adapterproto.RefusalNativeUnavailable), false
}

func refused(reason adapterproto.PromptRefusal) adapterproto.PromptDeliverResult {
	return adapterproto.PromptDeliverResult{Stage: adapterproto.PromptRefused, Reason: reason}
}

// repeatedDelivery answers an attempt the adapter has answered before with the same
// answer, brought up to date: an accepted prompt that has reached the record is
// persisted. The same attempt with another text is a conflict.
func (a *App) repeatedDelivery(s *session, p adapterproto.PromptDeliverParams) (adapterproto.PromptDeliverResult, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec := s.prompts[p.Attempt]
	if rec == nil {
		return adapterproto.PromptDeliverResult{}, false, nil
	}
	if rec.text != p.Text {
		return adapterproto.PromptDeliverResult{}, true, peer.NewError(adapterproto.CodeAttemptConflict, "the attempt was delivered with another text")
	}
	if rec.result.Stage == adapterproto.PromptAccepted && s.persisted.has(string(p.Attempt)) {
		rec.result.Stage = adapterproto.PromptPersisted
	}
	return rec.result, true, nil
}

// refusalOrUnknown maps a failed turn start. A refusal the server worded clearly
// is the adapter's to report as one, because certainly nothing was delivered; any
// other failure leaves the outcome unknown, which the daemon settles by asking
// (prompt.reconcile), and so is an internal error. That includes "not found" after
// the thread was loaded again, which contradicts the load.
func (a *App) refusalOrUnknown(err error) (adapterproto.PromptDeliverResult, error) {
	var rpc *appserver.RPCError
	if errors.As(err, &rpc) {
		m := strings.ToLower(rpc.Message)
		switch {
		case strings.Contains(m, "unauthorized") || strings.Contains(m, "not logged in") || strings.Contains(m, "sign in"):
			return refused(adapterproto.RefusalRuntimeAuth), nil
		case strings.Contains(m, "active turn") || strings.Contains(m, "already running") || strings.Contains(m, "busy"):
			return refused(adapterproto.RefusalBusy), nil
		}
	}
	return adapterproto.PromptDeliverResult{}, err
}

// Reconcile says whether a prompt attempt reached the session, from the thread's
// record; see the reconcile package for the rules.
func (a *App) Reconcile(ctx context.Context, p adapterproto.ReconcileParams) (adapterproto.ReconcileResult, error) {
	s := a.sessionOf(p.Endpoint)
	if s == nil {
		return adapterproto.ReconcileResult{}, unknownEndpoint(p.Endpoint)
	}
	in := reconcile.Input{Attempt: string(p.Attempt)}
	c := a.liveClient()
	if c == nil {
		in.Unreachable = true
		return reconcile.Judge(in), nil
	}
	queueUnread := false
	if s.attached {
		// A prompt of an attached session waits in the thread's queue until its
		// owner takes it, and is in no record before.
		queued, err := a.attachedQueueHolds(ctx, c, s, string(p.Attempt))
		if errors.Is(err, appserver.ErrClosed) {
			in.Unreachable = true
			return reconcile.Judge(in), nil
		}
		in.Queued, queueUnread = queued, err != nil
	}
	a.mu.Lock()
	in.ThreadActive = s.status == appserver.ThreadActive
	// The adapter's books count a turn from the answer to its start, before the
	// server's events for it come.
	in.TurnRunning = s.activeTurn != ""
	a.mu.Unlock()

	turns, complete, err := readTurns(ctx, c, s.threadID, recordMaxPages, func(t appserver.Turn) bool { return hasPrompt(t, p.Attempt) })
	if err != nil {
		if errors.Is(err, appserver.ErrClosed) {
			in.Unreachable = true
			return reconcile.Judge(in), nil
		}
		a.log.Warn("cannot read the record of a session", "endpoint", p.Endpoint, "error", err)
		complete = false
	}
	in.Complete = complete && !queueUnread
	now := time.Now()
	a.mu.Lock()
	for _, t := range turns {
		state := turnState(t.Status)
		if s.attached && t.Unfinished() && !a.stale(t, s.seenAt(t.ID, now), now) {
			// A turn another process runs shows "interrupted" with no completion
			// time until it ends; it is still running, and may be about to record
			// the prompt. A created session's server, which does run its turns, never
			// leaves one so: there it means the server died.
			state = adapterproto.TurnStateRunning
		}
		in.Turns = append(in.Turns, reconcile.Turn{Ref: t.ID, State: state, Attempts: t.UserMessageIDs()})
	}
	a.mu.Unlock()
	return reconcile.Judge(in), nil
}
