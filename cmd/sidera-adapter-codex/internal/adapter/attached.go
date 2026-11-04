package adapter

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// An attached session is a thread a person's Codex runs, and which asked to be
// attached. Another process has it loaded, so this adapter's server cannot resume
// it ("already has an active writer") and gets no event of it. What it can do, and
// what the adapter relies on, is what the server does for a thread it does not
// hold: read its record (thread/read, thread/turns/list) and put prompts into its
// queue (thread/queue/add). The queue lives in the Codex home, which this server
// shares with the person's Codex, and the process that has the thread loaded takes
// a queued prompt when the thread is idle, or when the turn it runs ends.

// attachedWindow is how many of the newest turns one reading looks at. A thread
// gains a turn in many seconds, and the reading comes every second, so a few are
// ample; a turn that falls out of the window unseen is not reported.
const attachedWindow = 5

// queuePage is how many queued prompts a reconcile looks at; a queue holding more
// is taken to hold the prompt it looks for.
const queuePage = 100

// queueWithdrawTimeout bounds the taking back of queued prompts when an endpoint
// detaches.
const queueWithdrawTimeout = 10 * time.Second

// AttachSession takes over a thread the person opened: it checks that the thread
// exists and can be given prompts, counts the turns it already has as history and
// starts reading its turns. The wake is not used: Codex needs none. The call is
// idempotent, and the heartbeat that repeats it only checks the thread again.
func (a *App) AttachSession(ctx context.Context, p adapterproto.SessionAttachParams) (adapterproto.SessionAttachResult, error) {
	// adapter.start replaces the adapter's sessions: one attached before it would be
	// lost with its reading left running, so the request is answered with an error,
	// which leaves its outcome unknown to the daemon, and the daemon asks again.
	a.mu.Lock()
	started := a.started
	a.mu.Unlock()
	if !started {
		return adapterproto.SessionAttachResult{}, errNotStarted
	}
	c := a.liveClient()
	if c == nil {
		if cc, err := a.ensureClient(ctx); err == nil {
			c = cc
		}
	}
	if c == nil {
		return attachFailed(adapterproto.AttachFailNativeUnreachable, "the App Server cannot be reached"), nil
	}
	threadID := string(p.NativeRef)
	if res, ok := a.checkAttachable(ctx, c, threadID); !ok {
		return res, nil
	}

	a.attachMu.Lock()
	defer a.attachMu.Unlock()
	s := a.sessionOf(p.Endpoint)
	if s != nil {
		a.mu.Lock()
		wasLost := s.lost
		a.mu.Unlock()
		if wasLost {
			// The thread is back: the watch begins again, and so does the endpoint.
			a.setRefusal(s, "")
		}
	}
	if s == nil {
		s = newAttachedSession(p.Endpoint, threadID)
		if err := a.pollAttached(ctx, c, s, true); err != nil {
			return attachFailureOf(err), nil
		}
		a.mu.Lock()
		a.sessions[p.Endpoint], a.byThread[threadID] = s, s
		a.mu.Unlock()
	}
	a.startWatch(s)
	return adapterproto.SessionAttachResult{Outcome: adapterproto.SessionAttachAttached}, nil
}

// checkAttachable reads the thread and says whether prompts can be given to it.
// When it cannot, the result is the answer to give.
func (a *App) checkAttachable(ctx context.Context, c *appserver.Client, threadID string) (adapterproto.SessionAttachResult, bool) {
	th, err := c.ThreadRead(ctx, threadID)
	switch {
	case err == nil:
	case appserver.IsThreadGone(err):
		return adapterproto.SessionAttachResult{Outcome: adapterproto.SessionAttachNotFound}, false
	default:
		return attachFailureOf(err), false
	}
	switch {
	case th.Ephemeral:
		return attachFailed(adapterproto.AttachFailWakeUnavailable, "the thread is ephemeral, and Codex takes no queued prompt for it"), false
	case th.IsSubAgent():
		return attachFailed(adapterproto.AttachFailWakeUnavailable, "the thread belongs to a sub-agent, and Codex takes no queued prompt for it"), false
	}
	return adapterproto.SessionAttachResult{}, true
}

func attachFailed(reason adapterproto.SessionAttachFailure, detail string) adapterproto.SessionAttachResult {
	return adapterproto.SessionAttachResult{Outcome: adapterproto.SessionAttachFailed, Reason: reason, Detail: detail}
}

// attachFailureOf maps a failed call to the server: one that did not reach it is
// unreachable, an answer that is not what was expected is the server's error.
func attachFailureOf(err error) adapterproto.SessionAttachResult {
	var rpc *appserver.RPCError
	if errors.As(err, &rpc) {
		return attachFailed(adapterproto.AttachFailNativeError, cut(rpc.Message, adapterproto.MaxDetailLen))
	}
	return attachFailed(adapterproto.AttachFailNativeUnreachable, cut(err.Error(), adapterproto.MaxDetailLen))
}

// SessionDetached stops watching a session the endpoint of which was closed, and
// takes back the prompts it had put into the person's queue that nobody took: a
// wake-up must not outlive the endpoint it was for. The person's thread is left as
// it is otherwise.
func (a *App) SessionDetached(_ context.Context, p adapterproto.SessionDetachedParams) {
	if s := a.sessionOf(p.Endpoint); s != nil && s.attached {
		if c := a.liveClient(); c != nil {
			ctx, cancel := context.WithTimeout(a.bgCtx, queueWithdrawTimeout)
			a.withdrawQueued(ctx, c, s)
			cancel()
		}
	}
	if a.forgetAttached(p.Endpoint) {
		// Its condition, if it had one, goes with it.
		a.publishAvailability(false)
	}
}

// forgetAttached stops the watch of an attached session and drops it, and says
// whether there was one.
func (a *App) forgetAttached(endpoint adapterproto.EndpointRef) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[endpoint]
	if s == nil || !s.attached {
		return false
	}
	if s.watchCancel != nil {
		s.watchCancel()
		s.watchCancel = nil
	}
	delete(a.sessions, endpoint)
	if a.byThread[s.threadID] == s {
		delete(a.byThread, s.threadID)
	}
	return true
}

// resumeAttached brings an attached session of adapter.start back: the thread is
// checked, its turns read as history (the daemon learns of a running turn from the
// answer to adapter.start) and the watching begins. A session whose server cannot
// be reached is watched all the same, and the watching goes on trying.
func (a *App) resumeAttached(ctx context.Context, c *appserver.Client, s *session) (adapterproto.AttachState, bool) {
	if c != nil {
		if res, ok := a.checkAttachable(ctx, c, s.threadID); !ok {
			if res.Outcome == adapterproto.SessionAttachNotFound || res.Reason == adapterproto.AttachFailWakeUnavailable {
				a.mu.Lock()
				s.lost = true
				a.mu.Unlock()
				return adapterproto.AttachLost, false
			}
		} else if err := a.pollAttached(ctx, c, s, false); err != nil && appserver.IsThreadGone(err) {
			a.mu.Lock()
			s.lost = true
			a.mu.Unlock()
			return adapterproto.AttachLost, false
		}
	}
	a.startWatch(s)
	a.mu.Lock()
	defer a.mu.Unlock()
	if !s.baselined {
		return adapterproto.AttachUnreachable, false
	}
	return adapterproto.AttachAttached, s.activeTurn != ""
}

// startWatch begins reading the turns of an attached session, unless that is going
// on.
func (a *App) startWatch(s *session) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s.watchCancel != nil || a.shutdownNow {
		return
	}
	ctx, cancel := context.WithCancel(a.bgCtx)
	s.watchCancel, s.lost = cancel, false
	go a.watchAttached(ctx, s)
}

// watchAttached reads the turns of an attached session for as long as the session
// is attached: it is the only way the adapter hears of them. Each reading makes the
// server rebuild the thread's history, so the pace follows need: every
// AttachPollEvery while a prompt of this adapter is awaited (queued, or taken and
// its turn not yet seen) or a turn is open, which is when a change is news to look
// for soon, and every AttachIdlePollEvery otherwise. A prompt that is queued starts
// the quick pace at once (see attachedPoke). The slow pace cannot cause a prompt in
// the middle of a person's turn, which a late sight of that turn might otherwise let
// the daemon send: Codex's queue never puts an item into a running turn, the owner
// takes it only when the turn is over.
//
// While the server cannot be reached, or fails, the wait grows to ReconnectMax,
// and it is the next reading that tells what happened meanwhile. A thread the server
// says is gone ends the watching; the next attachment of the endpoint checks it
// again.
func (a *App) watchAttached(ctx context.Context, s *session) {
	delay := a.pollPace(s)
	failing := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		case <-s.poke:
		}
		c := a.liveClient()
		if c == nil {
			delay = min(delay*2, a.opts.ReconnectMax)
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, dialTimeout)
		err := a.pollAttached(rctx, c, s, true)
		cancel()
		switch {
		case err == nil:
			delay, failing = a.pollPace(s), false
		case ctx.Err() != nil:
			return
		case appserver.IsThreadGone(err):
			a.log.Warn("the thread of an attached session is gone", "endpoint", s.endpoint)
			// What the person's session cannot take any more is the endpoint's alone to
			// say: a turn that was open is over with the thread, and the condition
			// tells the daemon why prompts are not delivered.
			a.mu.Lock()
			s.lost, s.watchCancel = true, nil
			end := a.abandonActiveLocked(s)
			a.mu.Unlock()
			if end != nil {
				end()
			}
			a.setRefusal(s, cut("the thread is gone: "+err.Error(), adapterproto.MaxDetailLen))
			return
		default:
			if !failing {
				a.log.Warn("cannot read the turns of an attached session", "endpoint", s.endpoint, "error", err)
			}
			delay, failing = min(delay*2, a.opts.ReconnectMax), true
		}
	}
}

// pollPace is the wait until the next reading of an attached session.
func (a *App) pollPace(s *session) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s.awaitsPrompt() || s.activeTurn != "" {
		return a.opts.AttachPollEvery
	}
	return a.opts.AttachIdlePollEvery
}

// pollAttached reads the newest turns of an attached thread once and settles the
// adapter's books with them, reporting what changed.
//
// A turn is over when the record gives it a completion time. A turn another
// process runs shows no end: its status is "interrupted" and its completion time
// null (see Turn.Unfinished). Such a turn is still taken for over when a newer turn
// exists, which could not begin before it ended, and when it began (or, if the record
// shows no start, when it was first seen) more than TurnStaleAfter ago, so that a
// Codex that died in the middle of a turn does not leave the session busy for ever;
// it is then reported as failed.
//
// The first reading counts the turns the thread already has as history. Only a turn
// that is running then is adopted as the person's, and reported when report is set
// (the daemon is told by adapter.start otherwise). Later, a turn that was not there
// before is reported started and, when it is over, ended; the prompt it carries
// names its attempt when it is one this adapter queued.
func (a *App) pollAttached(ctx context.Context, c *appserver.Client, s *session, report bool) error {
	res, err := c.ThreadTurnsList(ctx, appserver.ThreadTurnsListParams{ThreadID: s.threadID, Limit: attachedWindow, ItemsView: "summary"})
	if err != nil {
		return err
	}
	turns := res.Data // newest first
	now := time.Now()
	var notify []func()

	a.mu.Lock()
	awaiting := s.awaitsPrompt()
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		ids := t.UserMessageIDs()
		rec := s.turns[t.ID]
		if rec == nil {
			rec = s.turn(t.ID)
			rec.seenAt = now
		}
		over := !t.Unfinished() || i > 0 || a.stale(t, rec.seenAt, now)
		if !rec.started {
			if !s.baselined {
				rec.started, rec.ended = true, over
				if !over {
					s.activeTurn = t.ID
					if report {
						notify = append(notify, func() { a.reportStarted(s, t.ID, "") })
					}
				}
			} else {
				// Codex writes a turn before its prompt: when the prompt of ours is
				// expected, a turn that shows none yet is looked at once more.
				if !over && len(ids) == 0 && awaiting && rec.looks == 0 {
					rec.looks++
					continue
				}
				rec.started, rec.attempt = true, s.attemptIn(ids)
				if !over {
					s.activeTurn = t.ID
				}
				attempt := rec.attempt
				notify = append(notify, func() { a.reportStarted(s, t.ID, attempt) })
			}
		}
		for _, id := range ids {
			if s.notePersisted(id) {
				notify = append(notify, func() { a.reportPersisted(s, adapterproto.AttemptID(id)) })
			}
		}
		if over && !rec.ended {
			rec.ended = true
			if s.activeTurn == t.ID {
				s.activeTurn = ""
			}
			status := appserver.TurnFailed
			if !t.Unfinished() {
				status = t.Status
			}
			notify = append(notify, func() { a.reportEnded(s, t.ID, status) })
		}
	}
	// A turn the record no longer lists cannot be waited for.
	if id := s.activeTurn; id != "" && !slices.ContainsFunc(turns, func(t appserver.Turn) bool { return t.ID == id }) {
		notify = append(notify, a.abandonActiveLocked(s))
	}
	s.baselined = true
	a.mu.Unlock()

	for _, f := range notify {
		f()
	}
	return nil
}

// abandonActiveLocked ends the books on the turn reported started and not ended, as
// failed, and returns the notification that says so; nil when there is none. The
// caller holds App.mu and sends the notification after releasing it.
func (a *App) abandonActiveLocked(s *session) func() {
	id := s.activeTurn
	if id == "" {
		return nil
	}
	s.turn(id).ended, s.activeTurn = true, ""
	return func() { a.reportEnded(s, id, appserver.TurnFailed) }
}

// stale reports whether a turn is old enough to be taken for abandoned if it shows
// no end. A turn counts from its start; one that shows no start (a thread forked in
// the middle of a turn can hold such an old, unfinished turn) counts from when the
// adapter first saw it, which is seen.
func (a *App) stale(t appserver.Turn, seen, now time.Time) bool {
	if !t.Unfinished() {
		return false
	}
	since := seen
	if t.StartedAt != nil {
		since = time.Unix(*t.StartedAt, 0)
	}
	return now.Sub(since) > a.opts.TurnStaleAfter
}

// seenAt returns when the adapter first read a turn of an attached session, or now
// for one it has not read yet. The caller holds App.mu.
func (s *session) seenAt(turnID string, now time.Time) time.Time {
	if rec := s.turns[turnID]; rec != nil && !rec.seenAt.IsZero() {
		return rec.seenAt
	}
	return now
}

// awaitsPrompt reports whether a prompt this adapter queued has not been seen in
// the record. The caller holds App.mu.
func (s *session) awaitsPrompt() bool {
	for attempt, rec := range s.prompts {
		if rec.result.Stage == adapterproto.PromptAccepted && !s.persisted.has(string(attempt)) {
			return true
		}
	}
	return false
}

// attemptIn returns the attempt of the first prompt among the client ids that this
// adapter queued, or "". The reading of an attached session asks for the summary
// view of its turns, which keeps only the first user message of a turn, so a turn
// is named by the first prompt it holds: that is the prompt a queue item starts a
// turn with. Reconcile reads the full view and is not limited by this. The caller
// holds App.mu.
func (s *session) attemptIn(clientIDs []string) string {
	for _, id := range clientIDs {
		if s.prompts[adapterproto.AttemptID(id)] != nil {
			return id
		}
	}
	return ""
}

// deliverAttached queues a prompt for an attached session. The prompt's attempt is
// the client id of the queued item, which the record of the turn that takes it
// keeps, so that the turn is recognized as the one the prompt started. Queueing
// answers at once; the prompt is reported persisted and its turn started when the
// reading of the thread's turns shows them. While the person is in a turn, or
// nobody has the thread loaded, the prompt stays queued, and nothing is reported
// until it is taken.
func (a *App) deliverAttached(ctx context.Context, s *session, p adapterproto.PromptDeliverParams) (adapterproto.PromptDeliverResult, error) {
	leave, ok := a.enterServerOp()
	if !ok {
		return refused(adapterproto.RefusalNativeUnavailable), nil
	}
	defer leave()
	c := a.liveClient()
	if c == nil {
		if cc, err := a.ensureClient(ctx); err == nil {
			c = cc
		}
	}
	if c == nil {
		return refused(adapterproto.RefusalNativeUnavailable), nil
	}
	// At most one wake-up of Sidera waits in the person's queue: a prompt that nobody
	// took is taken back when a newer one is queued.
	a.withdrawQueued(ctx, c, s)
	// The prompt is booked before it is queued: the owner can take it and the
	// reading see its turn before the answer to the queueing arrives.
	rec := &promptRec{text: p.Text, result: adapterproto.PromptDeliverResult{Stage: adapterproto.PromptAccepted}}
	a.mu.Lock()
	s.prompts[p.Attempt] = rec
	a.mu.Unlock()
	queued, err := c.ThreadQueueAdd(ctx, s.threadID, string(p.Attempt), []appserver.UserInput{appserver.TextInput(p.Text)})
	s.attachedPoke() // the quick pace begins, whatever the answer
	// The server refuses a thread that cannot take a prompt (archived, ephemeral, a
	// sub-agent's, unknown) as an invalid request, before it queues anything, and says
	// "no rollout found" for an unknown one in an internal error. Any other failure
	// leaves the outcome unknown, which the daemon settles by asking (prompt.reconcile).
	threadRefuses := err != nil && (appserver.IsInvalidRequest(err) || appserver.IsThreadGone(err))
	if err == nil || threadRefuses {
		// The answer is the adapter's only sign that the thread takes prompts or
		// does not, so it also ends or begins the condition of the endpoint.
		a.notePromptRefusal(s, err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		delete(s.prompts, p.Attempt)
		if threadRefuses {
			a.log.Warn("a prompt cannot be queued for an attached session", "endpoint", s.endpoint, "error", err)
			return refused(adapterproto.RefusalNativeUnavailable), nil
		}
		return adapterproto.PromptDeliverResult{}, err
	}
	if s.persisted.has(string(p.Attempt)) {
		rec.result.Stage, rec.persistedSent = adapterproto.PromptPersisted, true
	} else {
		s.queued[p.Attempt] = queued.ID
	}
	return rec.result, nil
}

// withdrawQueued takes back the prompts this adapter queued for an attached session
// that are still in its queue, and only those: an item it did not add is the
// person's, or another tool's, and is never touched. A prompt the owner took in the
// meantime is no longer queued and is left to its turn. A failure is logged and
// leaves the prompt queued, which only costs the person a stale wake-up.
func (a *App) withdrawQueued(ctx context.Context, c *appserver.Client, s *session) {
	a.mu.Lock()
	mine := make(map[string]adapterproto.AttemptID, len(s.queued))
	for attempt, id := range s.queued {
		mine[id] = attempt
	}
	a.mu.Unlock()
	if len(mine) == 0 {
		return
	}
	queued, more, err := c.ThreadQueueList(ctx, s.threadID, queuePage)
	if err != nil {
		a.log.Warn("cannot read the queue of an attached session to take back a prompt", "endpoint", s.endpoint, "error", err)
		return
	}
	inQueue := map[string]bool{}
	for _, q := range queued {
		attempt, ok := mine[q.ID]
		if !ok || q.ClientUserMessageID != string(attempt) {
			continue
		}
		inQueue[q.ID] = true
		if _, err := c.ThreadQueueDelete(ctx, s.threadID, q.ID); err != nil {
			a.log.Warn("cannot take back a prompt from the queue of an attached session", "endpoint", s.endpoint, "error", err)
			continue
		}
		a.mu.Lock()
		delete(s.queued, attempt)
		// The prompt never reached the thread, and a repeat of its attempt is a new one.
		if rec := s.prompts[attempt]; rec != nil && rec.result.Stage == adapterproto.PromptAccepted && !s.persisted.has(string(attempt)) {
			delete(s.prompts, attempt)
		}
		a.mu.Unlock()
	}
	if more {
		return
	}
	// The whole queue was read: a prompt of ours that is not in it was taken.
	a.mu.Lock()
	for id, attempt := range mine {
		if !inQueue[id] {
			delete(s.queued, attempt)
		}
	}
	a.mu.Unlock()
}

// attachedQueueHolds reports whether the thread's queue may still hold the prompt
// of an attempt. A queue too long to look through is taken to.
func (a *App) attachedQueueHolds(ctx context.Context, c *appserver.Client, s *session, attempt string) (holds bool, err error) {
	queued, more, err := c.ThreadQueueList(ctx, s.threadID, queuePage)
	if err != nil {
		return false, err
	}
	return more || slices.ContainsFunc(queued, func(q appserver.QueuedSubmission) bool { return q.ClientUserMessageID == attempt }), nil
}

// wrongKind is the answer to a signal an attached session does not take: it is the
// person's.
func wrongKind(what string) error {
	return peer.NewError(adapterproto.CodeWrongEndpointKind, what+" is not possible for an attached session")
}
