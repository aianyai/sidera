package adapter

import (
	"context"
	"sync"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

const (
	// restoreConcurrency is how many sessions adapter.start has the App Server load
	// at once.
	restoreConcurrency = 4
	// answerReserve is what adapter.start leaves of the time its context allows, so
	// that the answer is sent before the deadline.
	answerReserve = time.Second
)

// restore is the loading of the sessions of one adapter.start, which goes on after
// the answer when it does not finish by the deadline. The sessions it did not
// finish are answered unreachable, as when the server cannot be reached; what it
// finds out about them afterwards is reported like what the reconnect loop finds
// out: a turn that runs is reported started.
type restore struct {
	mu       sync.Mutex
	answered bool
	states   []adapterproto.SessionState
	// finished is closed when every session has been dealt with.
	finished chan struct{}
}

func newRestore(order []*session) *restore {
	r := &restore{states: make([]adapterproto.SessionState, len(order)), finished: make(chan struct{})}
	for i, s := range order {
		r.states[i] = adapterproto.SessionState{Endpoint: s.endpoint, State: adapterproto.AttachUnreachable}
	}
	return r
}

// settle records what became of session i. It returns true when the answer was
// sent already: the state is then not in it, and the caller reports a turn it
// found running.
func (r *restore) settle(i int, state adapterproto.AttachState, active bool) (late bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answered {
		return true
	}
	r.states[i].State, r.states[i].TurnActive = state, active
	return false
}

// answer fixes the states as they are and returns them.
func (r *restore) answer() []adapterproto.SessionState {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answered = true
	return append([]adapterproto.SessionState{}, r.states...)
}

func (r *restore) wasAnswered() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.answered
}

// answerWait is how long adapter.start waits for the sessions: the option when
// set, otherwise until answerReserve before the context's deadline. ok is false
// when there is no bound.
func (a *App) answerWait(ctx context.Context) (wait time.Duration, ok bool) {
	if a.opts.StartWait > 0 {
		return a.opts.StartWait, true
	}
	if dl, has := ctx.Deadline(); has {
		return max(time.Until(dl)-answerReserve, 0), true
	}
	return 0, false
}

// run connects to the App Server and loads the sessions, restoreConcurrency at a
// time, in the background: an adapter with many sessions, or a server that has to
// start, takes longer than adapter.start may. It ends with ctx.
func (r *restore) run(ctx context.Context, a *App, order []*session) {
	defer close(r.finished)
	cctx, cancel := context.WithTimeout(ctx, dialTimeout)
	c, err := a.ensureClient(cctx)
	cancel()
	if err != nil {
		a.log.Warn("the App Server cannot be reached", "error", err)
		a.startReconnect()
	} else if r.wasAnswered() {
		a.publishAvailability(false)
	}

	next := make(chan int)
	var wg sync.WaitGroup
	for range min(restoreConcurrency, len(order)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				r.session(ctx, a, c, i, order[i])
			}
		}()
	}
feed:
	for i := range order {
		select {
		case next <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	wg.Wait()
	if r.wasAnswered() {
		a.publishAvailability(false)
		// The check made after the answer found the sessions not yet loaded, which count
		// as busy; now their state is known, and a replacement that was waiting for it
		// need not wait for the next periodic check.
		a.checkServer(ctx)
	}
}

// session loads one session, c being nil when the server could not be reached, and
// settles it.
func (r *restore) session(ctx context.Context, a *App, c *appserver.Client, i int, s *session) {
	if s.attached {
		sctx, cancel := context.WithTimeout(ctx, dialTimeout)
		state, active := a.resumeAttached(sctx, c, s)
		cancel()
		r.finish(a, i, s, state, active)
		return
	}
	if c == nil {
		return // the reconnect loop loads it
	}
	// The thread is loaded by one caller at a time, and not again when the reconnect
	// loop or a delivery loaded it first: that caller reported what it found.
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	if state, active, ok := a.loadedBy(s); ok {
		r.settle(i, state, active)
		return
	}
	sctx, cancel := context.WithTimeout(ctx, dialTimeout)
	state, active := a.attachLocked(sctx, c, s, false)
	cancel()
	if state == adapterproto.AttachUnreachable {
		// The reconnect loop loads the sessions still not loaded.
		a.startReconnect()
	}
	r.finish(a, i, s, state, active)
}

// finish settles a session and, when the answer to adapter.start has gone without
// it, reports the turn it found running: the daemon thinks the session idle.
func (r *restore) finish(a *App, i int, s *session, state adapterproto.AttachState, active bool) {
	if !r.settle(i, state, active) || state != adapterproto.AttachAttached {
		return
	}
	a.mu.Lock()
	turn := s.activeTurn
	a.mu.Unlock()
	if turn != "" {
		a.reportStarted(s, turn, "")
	}
}
