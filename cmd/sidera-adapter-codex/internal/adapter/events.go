package adapter

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/codexcfg"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/serverreq"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// startedWait is how long the event of a turn waits for the answer to the turn
// start that caused it. The answer normally comes first; the order is not promised.
const startedWait = 2 * time.Second

func nowMS() int64 { return time.Now().UnixMilli() }

// onNotification handles the notifications of the server, one at a time and in
// order.
func (a *App) onNotification(n appserver.Notification) {
	switch n.Method {
	case appserver.NotifyTurnStarted:
		if p, err := appserver.DecodeParams[appserver.ThreadNotification](n); err == nil {
			a.turnStarted(p)
		}
	case appserver.NotifyTurnCompleted:
		if p, err := appserver.DecodeParams[appserver.ThreadNotification](n); err == nil {
			a.turnCompleted(p)
		}
	case appserver.NotifyItemCompleted:
		if p, err := appserver.DecodeParams[appserver.ItemNotification](n); err == nil {
			a.itemCompleted(p)
		}
	case appserver.NotifyThreadStatus:
		if p, err := appserver.DecodeParams[appserver.StatusNotification](n); err == nil {
			a.mu.Lock()
			if s := a.byThread[p.ThreadID]; s != nil {
				s.status = p.Status.Type
			}
			a.mu.Unlock()
		}
	case appserver.NotifyMCPStatus:
		if p, err := appserver.DecodeParams[appserver.MCPStatusNotification](n); err == nil && p.Name == codexcfg.ServerName {
			a.mu.Lock()
			a.mcp[p.ThreadID] = p.Status
			s := a.byThread[p.ThreadID]
			a.mu.Unlock()
			if s != nil {
				s.signal()
			}
		}
	}
}

// turnStarted reports a turn that began. The attempt that caused it is known from
// the answer to the turn start, which may not have been processed yet.
func (a *App) turnStarted(p appserver.ThreadNotification) {
	a.mu.Lock()
	s := a.byThread[p.ThreadID]
	if s == nil {
		a.mu.Unlock()
		return
	}
	rec := s.turn(p.Turn.ID)
	if rec.started {
		a.mu.Unlock()
		return
	}
	pending := s.pending
	known := rec.attempt != ""
	a.mu.Unlock()
	if !known && pending != nil {
		select {
		case <-pending.resolved:
		case <-time.After(startedWait):
		}
	}
	a.mu.Lock()
	rec = s.turn(p.Turn.ID)
	if rec.started {
		a.mu.Unlock()
		return
	}
	rec.started = true
	s.activeTurn, s.status = p.Turn.ID, appserver.ThreadActive
	attempt := rec.attempt
	a.mu.Unlock()
	s.signal()
	a.reportStarted(s, p.Turn.ID, attempt)
}

// turnCompleted reports a turn that ended. A turn whose start was never seen (it
// began and ended while the adapter was not looking) is reported started first, so
// that every turn has its pair.
func (a *App) turnCompleted(p appserver.ThreadNotification) {
	a.mu.Lock()
	s := a.byThread[p.ThreadID]
	if s == nil {
		a.mu.Unlock()
		return
	}
	rec := s.turn(p.Turn.ID)
	if rec.ended {
		a.mu.Unlock()
		return
	}
	late := !rec.started
	rec.started, rec.ended = true, true
	attempt := rec.attempt
	if s.activeTurn == p.Turn.ID {
		s.activeTurn, s.status = "", appserver.ThreadIdle
	}
	a.mu.Unlock()
	s.signal()
	if late {
		a.reportStarted(s, p.Turn.ID, attempt)
	}
	a.reportEnded(s, p.Turn.ID, p.Turn.Status)
}

// itemCompleted notes that a prompt reached the record. The prompt a delivery
// accepted is then reported persisted.
func (a *App) itemCompleted(p appserver.ItemNotification) {
	if p.Item.Type != appserver.ItemUserMessage || p.Item.ClientID == nil {
		return
	}
	a.mu.Lock()
	s := a.byThread[p.ThreadID]
	if s == nil {
		a.mu.Unlock()
		return
	}
	notify := s.notePersisted(*p.Item.ClientID)
	a.mu.Unlock()
	s.signal()
	if notify {
		a.reportPersisted(s, adapterproto.AttemptID(*p.Item.ClientID))
	}
}

// notePersisted notes that the record holds the prompt with the client id given and
// says whether its persisting is to be reported: a delivery accepted that prompt
// and the report was not made. The caller holds App.mu.
func (s *session) notePersisted(clientID string) bool {
	s.persisted.add(clientID)
	delete(s.queued, adapterproto.AttemptID(clientID)) // taken from the queue
	rec := s.prompts[adapterproto.AttemptID(clientID)]
	if rec == nil || rec.result.Stage != adapterproto.PromptAccepted || rec.persistedSent {
		return false
	}
	rec.persistedSent = true
	return true
}

func (a *App) reportPersisted(s *session, attempt adapterproto.AttemptID) {
	a.send("prompt.persisted", func(ctx context.Context) error {
		return a.conn.PromptPersisted(ctx, adapterproto.PromptPersistedParams{Endpoint: s.endpoint, Attempt: attempt})
	})
}

// send makes a notification to the daemon; one that cannot be queued is logged,
// since the daemon's own checks (reconcile, the next snapshot) cover its loss.
func (a *App) send(what string, f func(ctx context.Context) error) {
	ctx, cancel := context.WithTimeout(a.bgCtx, 10*time.Second)
	defer cancel()
	if err := f(ctx); err != nil {
		a.log.Warn("cannot send a notification", "what", what, "error", err)
	}
}

func (a *App) reportStarted(s *session, turnID, attempt string) {
	a.send("turn.started", func(ctx context.Context) error {
		return a.conn.TurnStarted(ctx, adapterproto.TurnStartedParams{
			Endpoint: s.endpoint, Turn: adapterproto.TurnRef(turnID), Attempt: adapterproto.AttemptID(attempt), AtMS: nowMS(),
		})
	})
}

func (a *App) reportEnded(s *session, turnID, status string) {
	a.send("turn.ended", func(ctx context.Context) error {
		return a.conn.TurnEnded(ctx, adapterproto.TurnEndedParams{
			Endpoint: s.endpoint, Turn: adapterproto.TurnRef(turnID), Final: turnFinal(status), AtMS: nowMS(),
		})
	})
}

// turnFinal maps how the server says a turn ended to the protocol's word. A turn
// the server no longer lists is failed, like one whose process died.
func turnFinal(status string) adapterproto.TurnFinal {
	switch status {
	case appserver.TurnCompleted:
		return adapterproto.TurnCompleted
	case appserver.TurnInterrupted:
		return adapterproto.TurnInterrupted
	}
	return adapterproto.TurnFailed
}

// catchUp reads the newest turns of a thread that was just loaded and settles the
// books with them: the turn the adapter thought running may have ended, and a turn
// may be running that the adapter did not know of. With report the daemon is told
// of both; without, the busy state is taken silently.
func (a *App) catchUp(ctx context.Context, c *appserver.Client, s *session, report bool) {
	res, err := c.ThreadTurnsList(ctx, appserver.ThreadTurnsListParams{ThreadID: s.threadID, Limit: 5, ItemsView: "notLoaded"})
	if err != nil {
		a.log.Warn("cannot read the turns of a session", "endpoint", s.endpoint, "error", err)
		return
	}
	type end struct{ id, status string }
	var ended []end
	var adopt string

	a.mu.Lock()
	if s.activeTurn != "" {
		found := false
		for _, t := range res.Data {
			if t.ID != s.activeTurn {
				continue
			}
			found = true
			if !t.Running() {
				ended = append(ended, end{t.ID, t.Status})
			}
		}
		if !found {
			ended = append(ended, end{s.activeTurn, appserver.TurnFailed})
		}
	}
	for _, e := range ended {
		s.turn(e.id).ended = true
		if s.activeTurn == e.id {
			s.activeTurn = ""
		}
	}
	if len(res.Data) > 0 && res.Data[0].Running() && s.turn(res.Data[0].ID).started == false {
		adopt = res.Data[0].ID
		s.turn(adopt).started = true
		s.activeTurn, s.status = adopt, appserver.ThreadActive
	}
	a.mu.Unlock()
	s.signal()

	if !report {
		return
	}
	for _, e := range ended {
		a.reportEnded(s, e.id, e.status)
	}
	if adopt != "" {
		a.reportStarted(s, adopt, "")
	}
}

// onServerRequest answers a request of the server from the preset, at once, and
// reports how it was answered. A request that belongs to no session of the adapter
// is answered as a request of no preset: it approves nothing.
func (a *App) onServerRequest(method string, params json.RawMessage) (any, *appserver.RPCError) {
	threadID := serverreq.ThreadID(params)
	a.mu.Lock()
	endpoint := a.runtimeEP
	approves := false
	if s := a.byThread[threadID]; s != nil {
		endpoint = s.endpoint
		if e, ok := a.presets[s.preset]; ok {
			approves = e.preset.Approves()
		}
	}
	a.mu.Unlock()

	res := serverreq.Handle(method, approves)
	if endpoint != "" && len(method) <= adapterproto.MaxIdentLen {
		a.send("native_request.handled", func(ctx context.Context) error {
			return a.conn.NativeRequestHandled(ctx, adapterproto.NativeRequestHandledParams{
				Endpoint: endpoint, Method: method, Disposition: res.Disposition, AtMS: nowMS(),
			})
		})
	}
	if res.Error != nil {
		return nil, &appserver.RPCError{Code: res.Error.Code, Message: res.Error.Message}
	}
	return res.Reply, nil
}
