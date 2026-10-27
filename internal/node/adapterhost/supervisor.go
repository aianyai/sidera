package adapterhost

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// How a wait of the supervisor ends when it is not the thing waited for.
var (
	errStopped = errors.New("adapterhost: the instance is being stopped")
	errAborted = errors.New("adapterhost: the host was aborted")
	errLimit   = errors.New("adapterhost: the time limit passed")
)

func isEnd(err error) bool { return errors.Is(err, errStopped) || errors.Is(err, errAborted) }

// supervisor runs one instance: it launches the adapter, takes it on, watches it
// and starts it again after a backoff, until it is stopped or the host ends.
//
// One goroutine runs it, and that goroutine is the only one that waits on the
// host's timers. It never blocks anywhere but in park, so a test that moved the
// clock can ask (counts) whether everything the move made due has been acted on,
// and the park it asks in answers: a call that can take long (the adapter's
// answer, any call of the engine) runs on a helper goroutine of its own while the
// supervisor parks. What it does without waiting for anyone else (launching the
// process, killing it, opening its log) is not a wait.
type supervisor struct {
	h   *Host
	id  string
	log *slog.Logger

	stop     chan struct{} // closed by requestStop
	stopOnce sync.Once
	done     chan struct{} // closed when the supervisor has finished
	prev     <-chan struct{}

	started    chan struct{} // closed when Start need no longer wait for this instance
	startOnce  sync.Once
	startTimer clock.Timer // nil once released, and for a supervisor Start did not make

	// asks is how counts asks the supervisor for the counts of the instance: the
	// answer is made by the supervisor's goroutine, inside the park it is in, so that
	// whether it is working and what is counted are of one moment.
	asks chan chan ConnCounts

	// Used by the supervisor's goroutine only.
	timers   map[clock.Timer]struct{}
	draining bool // a connection is being ended: a stop request no longer interrupts
	wg       sync.WaitGroup

	mu       sync.Mutex
	stopping bool  // guarded by mu
	cur      *conn // the latest connection, in service or not
	service  *conn // the connection in service, nil otherwise
}

func newSupervisor(h *Host, id string, prev <-chan struct{}, startWait bool) *supervisor {
	s := &supervisor{
		h:       h,
		id:      id,
		log:     h.log.With("instance", id),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		prev:    prev,
		started: make(chan struct{}),
		asks:    make(chan chan ConnCounts),
		timers:  map[clock.Timer]struct{}{},
	}
	if startWait {
		s.startTimer = s.newTimer(h.params.StartWait)
	}
	return s
}

// requestStop asks the supervisor to end the instance gracefully. It is the only
// method other goroutines call besides the counts and the lookups below.
func (s *supervisor) requestStop() {
	s.mu.Lock()
	s.stopping = true
	s.service = nil
	if s.cur != nil {
		s.cur.closing.Store(true)
	}
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stop) })
}

func (s *supervisor) isStopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopping
}

func (s *supervisor) stopRequested() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

// run is the supervisor's goroutine.
func (s *supervisor) run() {
	defer s.finish()
	if s.prev != nil {
		// The adapter of this instance that is still being ended holds the lock.
		if sl, _ := s.park(waits{over: s.prev}, true); sl == slotStop || sl == slotAbort {
			return
		}
	}
	p := s.h.params
	next := p.RestartBackoffMin
	for {
		stable, err := s.pass()
		if err != nil {
			return
		}
		wait := next
		if stable {
			wait = p.RestartBackoffMin
		}
		next = min(2*wait, p.RestartBackoffMax)
		t := s.newTimer(wait)
		sl, _ := s.park(waits{limit: t.C()}, true)
		s.stopTimer(t)
		if sl == slotStop || sl == slotAbort {
			return
		}
	}
}

func (s *supervisor) finish() {
	s.release()
	for t := range s.timers {
		t.Stop()
	}
	// The helpers end by themselves, but one may be a call of the engine, and the
	// engine may be asking for the counts of this instance: the supervisor keeps
	// answering while it waits.
	helpers := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(helpers)
	}()
	s.wait(waits{over: helpers})
	close(s.done)
	s.h.forget(s)
}

// release lets Start stop waiting for this instance.
func (s *supervisor) release() {
	s.startOnce.Do(func() {
		s.stopTimer(s.startTimer)
		s.startTimer = nil
		close(s.started)
	})
}

// startWaitExpired is what the supervisor does when Params.StartWait passed with
// the instance not yet started: the engine is told it is offline, Start goes on,
// and the supervisor keeps trying.
func (s *supervisor) startWaitExpired() {
	s.stopTimer(s.startTimer)
	s.startTimer = nil
	s.report(GoneStartFailed)
	s.release()
}

// report tells the engine why the instance has no connection. It is made with
// the host's context, not the connection's, and not at all once the instance is
// being stopped or the host ended: those report nothing. A stop or the end of the
// host that comes while the engine takes the report ends the wait, and the
// supervisor waits for the call before it finishes.
func (s *supervisor) report(cause GoneCause) {
	if s.h.ctx.Err() != nil || s.stopRequested() {
		return
	}
	err := s.engine(func(ctx context.Context) error { return s.h.env.Engine.AdapterGone(ctx, s.id, cause) })
	if err != nil && !isEnd(err) {
		s.log.Warn("the engine refused the report that the adapter is gone", "cause", cause.String(), "err", err)
	}
}

func (s *supervisor) newTimer(d time.Duration) clock.Timer {
	t := s.h.clk.NewTimer(d)
	s.timers[t] = struct{}{}
	return t
}

func (s *supervisor) stopTimer(t clock.Timer) {
	if t != nil {
		t.Stop()
		delete(s.timers, t)
	}
}

// spawn runs f on a goroutine that the supervisor waits for before it finishes.
func (s *supervisor) spawn(f func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		f()
	}()
}

type slot uint8

// What woke a park.
const (
	slotRes slot = iota + 1
	slotLimit
	slotTick
	slotStable
	slotOver
	slotStop
	slotAbort
)

// waits are the events a park waits for; a nil one is not waited for. A stop
// request, the end of the host and the start wait are always waited for, unless
// final is set.
type waits struct {
	res    <-chan error
	limit  <-chan time.Time
	tick   <-chan time.Time
	stable <-chan time.Time
	over   <-chan struct{}
	// final: neither a stop request nor the end of the host ends the wait, because
	// what is waited for ends by itself and must have ended before the supervisor
	// goes on (a helper goroutine whose context was cancelled, the forwarder).
	final bool
}

// park waits for one of w, for a stop request (unless a connection is being
// ended) or for the host to end, and answers whoever asks for the counts. An idle
// park is one where the supervisor waits for something that is not its own work
// (the adapter, a timer, a request to stop): there it is not working unless
// something is due. A park that is not idle is the supervisor waiting on work of
// its own, such as a call of the engine, and it is working.
func (s *supervisor) park(w waits, idle bool) (slot, error) {
	stop, ended := s.stop, s.h.ctx.Done()
	if s.draining {
		stop = nil
	}
	if w.final {
		stop, ended = nil, nil
	}
	for {
		var start <-chan time.Time
		if s.startTimer != nil {
			start = s.startTimer.C()
		}
		select {
		case err := <-w.res:
			return slotRes, err
		case <-w.limit:
			return slotLimit, nil
		case <-w.tick:
			return slotTick, nil
		case <-w.stable:
			return slotStable, nil
		case <-w.over:
			return slotOver, nil
		case <-stop:
			return slotStop, nil
		case <-ended:
			return slotAbort, nil
		case <-start:
			s.startWaitExpired()
		case reply := <-s.asks:
			reply <- s.answer(w, idle)
		}
	}
}

// wait waits for what ends by itself (see waits.final) and answers the counts
// while it does.
func (s *supervisor) wait(w waits) {
	w.final = true
	s.park(w, false)
}

// answer is the counts of the instance at the moment the supervisor is in a park:
// the counts of the latest connection, whether a connection is in service, and
// whether the supervisor is working. It runs on the supervisor's goroutine.
func (s *supervisor) answer(w waits, idle bool) ConnCounts {
	s.mu.Lock()
	cur, service := s.cur, s.service
	s.mu.Unlock()
	var out ConnCounts
	if cur != nil {
		out = cur.counts()
	}
	out.Connected = service != nil
	out.Working = !idle || s.due(w)
	return out
}

// due reports whether anything the supervisor would act on is waiting: a timer
// of the host that is due and not taken (only a test clock shows that: a real
// timer's channel is empty until it is read), an answer, the end of the
// connection, or a request to stop or the end of the host that the park would
// return for.
func (s *supervisor) due(w waits) bool {
	for t := range s.timers {
		if len(t.C()) > 0 {
			return true
		}
	}
	if w.res != nil && len(w.res) > 0 {
		return true
	}
	if w.over != nil {
		select {
		case <-w.over:
			return true
		default:
		}
	}
	if !w.final && (s.h.ctx.Err() != nil || (!s.draining && s.stopRequested())) {
		return true
	}
	return false
}

// counts is what the host counts of the connection of the instance, as the
// supervisor answers it from the park it is in. The supervisor blocks only in
// park, so this returns as soon as it reaches one; and a request is counted
// before the supervisor parks to wait for its answer, so the counts that come with
// "not working" include every request the supervisor waits for. A supervisor that
// has finished counts nothing.
func (s *supervisor) counts() ConnCounts {
	reply := make(chan ConnCounts, 1)
	select {
	case s.asks <- reply:
		return <-reply
	case <-s.done:
		return ConnCounts{}
	}
}

// rpc sends a request to the adapter and waits for the answer for at most limit
// on the host's clock. The result is decoded and validated into result.
func (s *supervisor) rpc(c *conn, method string, params, result adapterproto.Message, limit time.Duration) error {
	// The host ending must not make the peer send a cancellation: abort sends
	// nothing, so this context ends only when the supervisor gives up.
	ctx, cancel := context.WithCancel(context.WithoutCancel(s.h.ctx))
	defer cancel()
	t := s.newTimer(limit)
	defer s.stopTimer(t)
	res := make(chan error, 1)
	// The request is counted from here, before the supervisor parks, until rpc
	// returns, after the supervisor has taken the answer: both ends are on the
	// supervisor's goroutine, so a caller that finds the host idle with the request
	// no longer counted knows the supervisor has acted on its answer. A helper that
	// stopped counting it would leave a moment when the answer is on its way to
	// the supervisor and the host looks quiet.
	defer c.track(method)()
	s.spawn(func() { res <- c.send(ctx, method, params, result) })
	sl, err := s.park(waits{res: res, limit: t.C()}, true)
	switch sl {
	case slotRes:
		return err
	case slotLimit:
		err = errLimit
	case slotStop:
		err = errStopped
	default:
		err = errAborted
		c.abortNow()
	}
	cancel()
	s.wait(waits{res: res})
	return err
}

// engine runs a call of the engine on a helper goroutine while the supervisor
// waits for it: the call is the supervisor's work, so it counts as working, but a
// stop request, the end of the host and the start wait are still seen.
func (s *supervisor) engine(f func(ctx context.Context) error) error {
	res := make(chan error, 1)
	s.spawn(func() { res <- f(s.h.ctx) })
	sl, err := s.park(waits{res: res}, false)
	switch sl {
	case slotRes:
		return err
	case slotStop:
		return errStopped
	}
	return errAborted
}
