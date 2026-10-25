package adapterhost

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
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
// clock can ask (working) whether everything the move made due has been acted on:
// a call that can take long (the adapter's answer, the engine) runs on a helper
// goroutine of its own while the supervisor parks.
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

	// busy is the number of things the supervisor is doing; it is one from the
	// start and the supervisor takes it down only while parked on something that is
	// not its own work. asks lets working ask a parked supervisor whether
	// anything is due.
	busy atomic.Int32
	asks chan chan bool

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
		asks:    make(chan chan bool),
		timers:  map[clock.Timer]struct{}{},
	}
	s.busy.Store(1)
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
	s.wg.Wait()
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
// being stopped or the host ended: those report nothing.
func (s *supervisor) report(cause GoneCause) {
	if s.h.ctx.Err() != nil || s.stopRequested() {
		return
	}
	if err := s.h.env.Engine.AdapterGone(s.h.ctx, s.id, cause); err != nil {
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
// request, the end of the host and the start wait are always waited for.
type waits struct {
	res    <-chan error
	limit  <-chan time.Time
	tick   <-chan time.Time
	stable <-chan time.Time
	over   <-chan struct{}
}

// park waits for one of w, for a stop request (unless a connection is being
// ended) or for the host to end. An idle park is one where the supervisor waits
// for something that is not its own work (the adapter, a timer, a request to
// stop); working reports false only for a supervisor parked idle with nothing
// due. A park that is not idle is the supervisor waiting on work of its own.
func (s *supervisor) park(w waits, idle bool) (slot, error) {
	stop := s.stop
	if s.draining {
		stop = nil
	}
	if idle {
		s.busy.Add(-1)
		defer s.busy.Add(1)
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
		case <-s.h.ctx.Done():
			return slotAbort, nil
		case <-start:
			if idle {
				s.busy.Add(1)
			}
			s.startWaitExpired()
			if idle {
				s.busy.Add(-1)
			}
		case reply := <-s.asks:
			reply <- s.due(w)
		}
	}
}

// due reports whether anything the supervisor would act on is waiting: a timer
// of the host that is due and not taken (only a test clock shows that: a real
// timer's channel is empty until it is read), an answer, or the end of the
// connection. It runs on the supervisor's goroutine, from park.
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
	return false
}

// working reports whether the supervisor is doing something of its own or has
// something due. A supervisor that is parked answers from inside its wait. One
// that is neither busy nor parked is between two waits, or has just answered an
// earlier question and is on its way back to waiting: it is asked again a few
// times, and is counted as working if it is still not there.
func (s *supervisor) working() bool {
	reply := make(chan bool, 1)
	for range askAttempts {
		select {
		case <-s.done:
			return false
		default:
		}
		if s.busy.Load() > 0 {
			return true
		}
		select {
		case s.asks <- reply:
			return <-reply
		default:
			runtime.Gosched()
		}
	}
	return true
}

// askAttempts is how many times working asks a supervisor that is on its way back
// to waiting before it counts it as working.
const askAttempts = 1000

// counts is what the host counts of the connection of the instance. Whether the
// supervisor is working is asked before the counts are read and again after: a
// supervisor that was parked and idle at both ends was idle while they were read,
// and one that went from the start of a request to waiting for its answer between
// them is reported as working, so a caller never sees "idle" together with counts
// that were taken before the request the supervisor is waiting for was counted.
func (s *supervisor) counts() ConnCounts {
	select {
	case <-s.done:
		return ConnCounts{}
	default:
	}
	working := s.working()
	s.mu.Lock()
	cur, service := s.cur, s.service
	s.mu.Unlock()
	var out ConnCounts
	if cur != nil {
		out = cur.counts()
	}
	out.Connected = service != nil
	out.Working = working || s.working()
	return out
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
	// The request is counted here, before the supervisor parks: a caller that finds
	// the host idle must find the request in the counts.
	done := c.track(method)
	s.spawn(func() {
		err := c.send(ctx, method, params, result)
		done()
		res <- err
	})
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
	<-res
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
