package adapter

import (
	"context"
	"errors"
	"strings"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/codexcfg"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/declare"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/reconcile"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// startTurn starts a turn with a prompt. The prompt's attempt is the client id the
// server keeps on its record, which is how it is found again; the text is the
// daemon's, unchanged. The turn is tied to the attempt before its start event can
// be reported, so that the event names it.
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
		s.turn(turn.ID).attempt = attempt
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
	signedIn, lost, busy, status := a.signedIn, s.lost, s.busy(), s.status
	a.mu.Unlock()
	switch {
	case lost:
		return refused(adapterproto.RefusalSessionLost), nil
	case c == nil:
		return refused(adapterproto.RefusalNativeUnavailable), nil
	case !signedIn:
		return refused(adapterproto.RefusalRuntimeAuth), nil
	case busy:
		return refused(adapterproto.RefusalBusy), nil
	}
	if status == appserver.ThreadNotLoaded {
		if state, _ := a.attach(ctx, c, s, true); state != adapterproto.AttachAttached {
			if state == adapterproto.AttachLost {
				return refused(adapterproto.RefusalSessionLost), nil
			}
			return refused(adapterproto.RefusalNativeUnavailable), nil
		}
	}

	turn, err := a.startTurn(ctx, c, s, preset, string(p.Attempt), p.Text)
	if err != nil {
		return a.refusalOrUnknown(s, err)
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
// (prompt.reconcile), and so is an internal error.
func (a *App) refusalOrUnknown(s *session, err error) (adapterproto.PromptDeliverResult, error) {
	var rpc *appserver.RPCError
	if errors.As(err, &rpc) {
		m := strings.ToLower(rpc.Message)
		switch {
		case appserver.IsThreadGone(err):
			a.mu.Lock()
			s.lost = true
			a.mu.Unlock()
			return refused(adapterproto.RefusalSessionLost), nil
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
	a.mu.Lock()
	in.ThreadActive = s.status == appserver.ThreadActive
	if rec := s.prompts[p.Attempt]; rec != nil {
		if t := s.turns[rec.turnID]; t != nil && t.started && !t.ended {
			in.TurnRunning = true
		}
	}
	version := a.codexVer
	a.mu.Unlock()
	in.VersionUntested = !declare.VersionTested(version)

	turns, complete, err := readTurns(ctx, c, s.threadID, recordMaxPages, func(t appserver.Turn) bool { return hasPrompt(t, p.Attempt) })
	if err != nil {
		if errors.Is(err, appserver.ErrClosed) {
			in.Unreachable = true
			return reconcile.Judge(in), nil
		}
		a.log.Warn("cannot read the record of a session", "endpoint", p.Endpoint, "error", err)
		complete = false
	}
	in.Complete = complete
	for _, t := range turns {
		in.Turns = append(in.Turns, reconcile.Turn{Ref: t.ID, State: turnState(t.Status), Attempts: t.UserMessageIDs()})
	}
	return reconcile.Judge(in), nil
}
