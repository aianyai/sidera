package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

// Phase is where the engine is in its life.
type Phase uint8

// The phases, in order. A failed engine skips to PhaseFailed from any of the
// running ones.
const (
	// PhaseOpened: the scan entry has built the view; no loop runs.
	PhaseOpened Phase = iota + 1
	// PhaseRecovering: the loop runs and the node is starting. What only reads
	// answers, what changes state is refused, and what an adapter asks of its
	// endpoints waits.
	PhaseRecovering
	// PhaseRunning: every command is taken.
	PhaseRunning
	// PhaseStopping: shutdown has begun; outside commands are refused.
	PhaseStopping
	// PhaseStopped: the loop has ended in an orderly way.
	PhaseStopped
	// PhaseFailed: the loop has ended because of a fatal storage error.
	PhaseFailed
)

var phaseNames = [...]string{PhaseOpened: "opened", PhaseRecovering: "recovering", PhaseRunning: "running",
	PhaseStopping: "stopping", PhaseStopped: "stopped", PhaseFailed: "failed"}

func (p Phase) String() string {
	if p >= PhaseOpened && p <= PhaseFailed {
		return phaseNames[p]
	}
	return fmt.Sprintf("invalid(%d)", uint8(p))
}

// Engine is the single writer: the one goroutine that runs every command that
// changes state. Everything below the first blank line of the struct is owned by
// that goroutine and touched by no other once Start has been called.
type Engine struct {
	d   Deps
	p   Params
	log *slog.Logger

	phase    atomic.Uint32
	inbox    chan command
	stopCh   chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	shutdown chan struct{} // closed by BeginShutdown
	shutOnce sync.Once
	rearm    chan struct{} // makes the loop look at its schedule again: Serve changes the phase from outside it
	running  chan struct{} // closed by Serve
	runOnce  sync.Once
	started  atomic.Bool
	ctx      context.Context // the engine's own: the parent of every worker
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	workers  atomic.Int32
	// adapterCalls is how many of the workers are inside a call to an adapter, which
	// Quiesce does not count as work while the adapter holds the request on purpose.
	adapterCalls atomic.Int32
	waiting      atomic.Int32 // adapter requests waiting for the running phase
	activity     chan struct{}

	errMu sync.Mutex
	err   error

	// Owned by the loop from Start on.
	view   *view
	hw     clock.HighWater
	paused bool
	// pausedSince is when the pause began, as far as it is known: the persisted
	// start, or the time of the command that found the clock set back; zero when
	// the pause was persisted without one.
	pausedSince time.Time
	// retired: the owner retired the identity of the node. Nothing is signed in its
	// name from then on, and the node is going down.
	retired    bool
	startedAt  time.Time                // the time Start was called
	adapters   map[string]*adapterState // by instance; see adapters.go
	sched      *scheduler
	timer      clock.Timer
	periodic   map[string]*periodicTask
	order      []string  // names of the periodic tasks, in the order they were added
	clockTask  string    // the periodic task that persists the clock state, if one was added
	tr         Transport // set by SetTransport before Start
	outbox     *outbox
	fb         feedbackState
	sweep      *sweepPass // the pass of the archive sweep that is running, nil between passes
	waits      map[ident.EndpointID]*waiter
	ready      map[ident.EndpointID]bool        // endpoints with a wait that a command made a message ready for; see serveReady
	pages      map[delivery.LeaseID]*issuedPage // what each page that was handed out held, for an acknowledgement of the whole page
	cont       []continuation
	parked     map[ident.EndpointID][]command   // commands that wait for the drain of an endpoint
	ran        uint64                           // how many commands the loop has taken from the inbox
	fatal      error                            // set by classify; the loop stops after the command that set it
	probed     bool                             // the command that just ran was a probe of Quiesce
	installing atomic.Pointer[admin.RequestKey] // the request whose adapter package is being copied: one is at a time, nil when none
	inLoop     atomic.Bool                      // the loop is running a command or the due entries of the schedule: what spawn asserts

	// pendingNotices are the direct endpoints that a message became ready for since the
	// last step of the loop, to be told with endpoint.pending; see pending.go.
	pendingNotices map[ident.EndpointID]bool

	// creations are the calls of the session creations that are not settled, by the
	// key of the control record; see create.go.
	creations map[store.TargetKey]*creation

	// reconciling says step 11 of the start has begun: until then a connecting adapter is
	// not asked again for the creations it owes; see redriveCreates.
	reconciling bool

	// executions are the calls of the close and interrupt signals that are not
	// settled, by the key of the control record; see execute.go.
	executions map[store.TargetKey]*execution

	// prompts is what the loop remembers of the prompting of each session endpoint;
	// promptsDirty says something has changed that the prompt rules read, and
	// promptsStarted that the node has begun to serve and judged every session once.
	prompts        map[ident.EndpointID]*promptState
	promptsDirty   bool
	promptsStarted bool

	// blockedDirty are the session endpoints whose delivery is to be judged blocked or
	// not at the next step of the loop, and servingSince is when the node began to
	// serve: an adapter that has not connected since is offline since then, as far as
	// the node can vouch.
	blockedDirty map[ident.EndpointID]bool
	servingSince time.Time
	// releases are the closed session endpoints whose native session is owed to the
	// adapter, by endpoint; see release.go.
	releases map[ident.EndpointID]*release

	// leaseDirty are the session endpoints whose leases are to be looked at at the next
	// step of the loop; see lease.go. What is marked while the node starts stays
	// marked until it serves.
	leaseDirty map[ident.EndpointID]bool

	// dueTicks are the records whose entries of the schedule fell due and that are
	// not ticked yet; tickQueued says a command to tick them is queued.
	dueTicks   []string
	tickQueued bool
}

// continuation is a command the loop queued for itself: the next chunk of a long
// batch. It runs after the commands that were in the inbox when it was queued and
// before those that arrived later, so a steady stream of new commands neither
// starves it nor is held up by it for longer than one chunk.
type continuation struct {
	c command
	// due is the count of inbox commands taken (Engine.ran) at which it runs: the
	// count at the time it was queued plus the commands the inbox held then.
	due uint64
}

type periodicTask struct {
	every time.Duration
	task  Task
}

// TaskInput is what a periodic task is told: the time of the command that runs
// it, the high water mark of the wall clock as the loop holds it, whether the
// clock is paused and since when, and whether the node may sign. A task that
// writes the mark and the pause to disk writes HighWater, RollbackPaused and
// PausedSince; a task that signs reads only SignRefusal.
type TaskInput struct {
	Now            time.Time
	HighWater      time.Time
	RollbackPaused bool
	// PausedSince is when the pause began: the persisted start for a node that
	// started paused, otherwise the time of the command that found the clock set
	// back. It is zero when the clock is not paused.
	PausedSince time.Time
	// SignRefusal is why the node may not sign now (SignRefusalOf), zero when it
	// may. It covers the retirement of the identity, which the pause alone does
	// not: between the retirement and the end of the daemon the loop goes on
	// running tasks.
	SignRefusal Reason
}

// Task is a periodic task. It runs in the loop, so it may write through the
// transaction layer and must not wait. An error that a retry may cure makes the
// loop run it again after Params.RetryBackoff; a fatal storage error stops the
// loop.
type Task func(in TaskInput) error

func (e *Engine) setPhase(p Phase) { e.phase.Store(uint32(p)) }

// Phase is where the engine is. It may be called from any goroutine.
func (e *Engine) Phase() Phase { return Phase(e.phase.Load()) }

// newEngine builds an engine over validated dependencies, before its scan.
func newEngine(d Deps, p Params) (*Engine, error) {
	switch {
	case d.Clock == nil:
		return nil, errors.New("engine: no clock")
	case d.Writer == nil || d.Store == nil || d.Config == nil:
		return nil, errors.New("engine: the writer, the store and the configuration are required")
	case d.Rand == nil:
		return nil, errors.New("engine: no source of randomness")
	case d.Node.IsZero():
		return nil, errors.New("engine: no node identifier")
	case d.Keys != nil && d.Keys.Node() != d.Node:
		return nil, errors.New("engine: the keys are those of another node")
	}
	log := d.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	e := &Engine{
		d: d, p: p, log: log,
		inbox:    make(chan command, p.InboxLen),
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
		shutdown: make(chan struct{}),
		running:  make(chan struct{}),
		activity: make(chan struct{}, 1),
		hw:       d.HighWater,
		paused:   d.RollbackPaused,
		adapters: map[string]*adapterState{},
		sched:    newScheduler(),
		rearm:    make(chan struct{}, 1),
		periodic: map[string]*periodicTask{},
		waits:    map[ident.EndpointID]*waiter{},
		ready:    map[ident.EndpointID]bool{},
		pages:    map[delivery.LeaseID]*issuedPage{},
		outbox:   newOutbox(),
		parked:   map[ident.EndpointID][]command{},

		creations:  map[store.TargetKey]*creation{},
		executions: map[store.TargetKey]*execution{},
		prompts:    map[ident.EndpointID]*promptState{},

		blockedDirty: map[ident.EndpointID]bool{},
		releases:     map[ident.EndpointID]*release{},
		leaseDirty:   map[ident.EndpointID]bool{},
	}
	e.pendingNotices = map[ident.EndpointID]bool{}
	if d.RollbackPaused {
		e.pausedSince = d.RollbackPausedSince
	}
	// What fell due while the node was down waits for the recovery steps of the
	// start, which come after the adapters have said which turns run.
	e.sched.held = func() bool { return e.Phase() == PhaseRecovering }
	e.ctx, e.cancel = context.WithCancel(context.Background())
	e.setPhase(PhaseOpened)
	return e, nil
}

// SystemAddress is the address of the system endpoint of this node: the sender
// of the node's own feedback. It is not an endpoint anyone registers.
func (e *Engine) SystemAddress() ident.Address {
	return ident.Address{Node: e.d.Node, Endpoint: ident.SystemEndpoint()}
}

// AddPeriodic registers a task to run every period, the first time one period
// after Start, and again one period after each run. The names are unique. It is
// for the composition root, before Start: from then on the loop owns the
// schedule.
func (e *Engine) AddPeriodic(name string, every time.Duration, t Task) error {
	switch {
	case e.Phase() != PhaseOpened || e.started.Load():
		return errors.New("engine: a periodic task is added before Start")
	case name == "":
		return errors.New("engine: a periodic task has a name")
	case every <= 0:
		return fmt.Errorf("engine: periodic task %q has no period", name)
	case t == nil:
		return fmt.Errorf("engine: periodic task %q has no task", name)
	case e.periodic[name] != nil:
		return fmt.Errorf("engine: periodic task %q is added twice", name)
	}
	e.periodic[name] = &periodicTask{every: every, task: t}
	e.order = append(e.order, name)
	return nil
}

// AddClockState is AddPeriodic for the task that persists the high water mark of
// the wall clock and the rollback pause (TaskInput.HighWater and RollbackPaused).
// Besides its period, the loop runs it at once, in the command that finds the wall
// clock set back: a pause that only memory knows is lost by a crash, and the clock
// that was set back is then trusted again after the restart. At most one task is
// the clock state's.
func (e *Engine) AddClockState(name string, every time.Duration, t Task) error {
	if e.clockTask != "" {
		return errors.New("engine: the clock state has a task already")
	}
	if err := e.AddPeriodic(name, every, t); err != nil {
		return err
	}
	e.clockTask = name
	return nil
}

// SetTransport gives the engine the transport its outbound worker calls. It is
// for the composition root, before Start: the transport is made with the engine
// as its handler, so it cannot exist before the engine does.
func (e *Engine) SetTransport(t Transport) error {
	if e.Phase() != PhaseOpened || e.started.Load() {
		return errors.New("engine: the transport is set before Start")
	}
	e.tr = t
	return nil
}

// Start runs the loop, in the phase of recovery. The periodic tasks are due one
// period from now.
func (e *Engine) Start() {
	if !e.started.CompareAndSwap(false, true) {
		panic("engine: Start twice")
	}
	now := e.d.Clock.Now()
	e.startedAt = now
	for _, name := range e.order {
		e.sched.set(schedKey{schedPeriodic, name}, now.Add(e.periodic[name].every))
	}
	e.timer = e.d.Clock.NewTimer(time.Hour)
	e.timer.Stop()
	e.setPhase(PhaseRecovering)
	go e.loop()
}

// Serve makes the engine take every command: the node has finished starting. The
// entries of the schedule that the recovery steps held back are given out from now
// on, and the loop is woken to look at them.
func (e *Engine) Serve() {
	if e.Phase() == PhaseRecovering {
		e.setPhase(PhaseRunning)
		select {
		case e.rearm <- struct{}{}:
		default:
		}
	}
	e.runOnce.Do(func() { close(e.running) })
}

// BeginShutdown makes the engine refuse commands from outside, and ends the
// waits of adapter requests that are waiting for the running phase. Commands the
// loop already holds run or are refused; none is left unanswered.
func (e *Engine) BeginShutdown() {
	for {
		p := e.Phase()
		if p >= PhaseStopping {
			break
		}
		if e.phase.CompareAndSwap(uint32(p), uint32(PhaseStopping)) {
			break
		}
	}
	e.shutOnce.Do(func() {
		close(e.shutdown)
		if e.started.Load() {
			select {
			case e.inbox <- cmdEndWaits{}:
			default: // the inbox is full: the waiting side watches the shutdown itself
			}
		}
	})
}

// Stop ends the loop after the command it is running and waits for it and for
// every goroutine the engine owns. It is safe to call twice. A command that has
// not run is answered ReasonShuttingDown.
func (e *Engine) Stop(ctx context.Context) error {
	e.BeginShutdown()
	e.stopOnce.Do(func() {
		close(e.stopCh)
		if !e.started.Load() {
			e.finish()
		}
	})
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Done is closed when the loop has ended, whatever the reason.
func (e *Engine) Done() <-chan struct{} { return e.done }

// Err is why the loop ended when it ended in a failure, and nil otherwise.
func (e *Engine) Err() error {
	e.errMu.Lock()
	defer e.errMu.Unlock()
	return e.err
}

// finish ends the engine: it answers what no one will run, stops the workers and
// closes Done. It runs once, on the loop's goroutine or, for an engine that was
// never started, on the caller's.
func (e *Engine) finish() {
	if e.Phase() != PhaseFailed {
		e.setPhase(PhaseStopped)
	}
	e.cancel()
	e.wg.Wait()
	e.endWaits()
	refuse := refuse(ReasonShuttingDown)
	for _, k := range e.cont {
		k.c.fail(refuse)
	}
	e.cont = nil
	for _, cs := range e.parked {
		for _, c := range cs {
			c.fail(refuse)
		}
	}
	e.parked = nil
	close(e.done)
	for {
		select {
		case c := <-e.inbox:
			c.fail(refuse)
		default:
			return
		}
	}
}

// poke tells whoever waits for the engine to be quiet that something happened.
func (e *Engine) poke() {
	select {
	case e.activity <- struct{}{}:
	default:
	}
}

// spawn starts a goroutine of the engine: it runs under the engine's context,
// which ends when the engine does, and the engine counts it as work in flight
// until it returns. It returns false, without starting it, once the engine has
// ended.
//
// It runs only on the loop's goroutine, inside a command or a due entry of the
// schedule. finish waits for the goroutines with WaitGroup.Wait on the same
// goroutine, and a WaitGroup may not be added to concurrently with a Wait that
// starts from a zero count; the loop is also what makes "ended" and "starting a
// worker" exclude each other. It panics when it is called while the loop is not
// running a command, which is what a call from another goroutine is in all but the
// rare moment the loop is busy; the flag is a guard against misuse, not a proof.
func (e *Engine) spawn(fn func(ctx context.Context)) bool {
	if !e.inLoop.Load() {
		panic("engine: spawn is called only from the loop")
	}
	select {
	case <-e.done:
		return false
	default:
	}
	e.workers.Add(1)
	e.wg.Add(1)
	go func() {
		defer func() {
			e.workers.Add(-1)
			e.wg.Done()
			e.poke()
		}()
		fn(e.ctx)
	}()
	return true
}

// ---------------------------------------------------------------------------
// Commands

// command is one thing the loop does. Each kind of command is a type, so that
// what the loop can do is a list that a reader and a crash walk can count.
type command interface {
	// gate says in which phases the command runs.
	gate() gate
	// run executes the command at now, on the loop, and answers it.
	run(e *Engine, now time.Time)
	// fail answers a command that was not run.
	fail(err error)
}

// gate is the rule of a command about the phase of the engine.
type gate uint8

const (
	// gateAlways: lifecycle facts and the engine's own work; run in every phase
	// in which the loop runs.
	gateAlways gate = iota + 1
	// gateRead: a read; refused only when the engine is stopping.
	gateRead
	// gateWrite: a command from outside that changes state; refused while the
	// engine recovers and when it stops.
	gateWrite
	// gateLoopback: what the node's own transport brings or asks of the engine,
	// and the results of what the engine sent: taken while the node recovers, as
	// the going on of the outgoing messages is a step of the start, and refused
	// when the engine stops.
	gateLoopback
	// gateAdapter: what an adapter asks of its endpoints. Its caller waits for
	// the running phase before it submits; the loop refuses it as a write.
	gateAdapter
)

// outcome is the answer of a command.
type outcome[T any] struct {
	v   T
	err error
}

// call is the answer channel of a command: capacity one, so that the loop never
// waits for a caller that has gone.
type call[T any] struct{ ch chan outcome[T] }

func newCall[T any]() call[T] { return call[T]{ch: make(chan outcome[T], 1)} }

func (c call[T]) done(v T, err error) { c.ch <- outcome[T]{v, err} }
func (c call[T]) fail(err error) {
	var zero T
	c.ch <- outcome[T]{zero, err}
}

// submitClass is who submits: an outside caller is refused when the inbox stays
// full; an internal one waits.
type submitClass uint8

const (
	submitExternal submitClass = iota + 1
	submitInternal
)

// enqueue puts a command in the inbox.
func (e *Engine) enqueue(ctx context.Context, class submitClass, c command) error {
	select {
	case <-e.done:
		return refuse(ReasonShuttingDown)
	default:
	}
	if !e.started.Load() {
		return refuse(ReasonRecovering) // no loop yet: the command would wait for ever
	}
	if class == submitExternal {
		select {
		case e.inbox <- c:
			return nil
		default:
		}
		wait := clock.System().NewTimer(e.p.SubmitWait)
		defer wait.Stop()
		select {
		case e.inbox <- c:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-e.done:
			return refuse(ReasonShuttingDown)
		case <-wait.C():
			return refuse(ReasonLoopBusy)
		}
	}
	select {
	case e.inbox <- c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-e.done:
		return refuse(ReasonShuttingDown)
	}
}

// roundTrip submits a command and waits for its answer. A command that entered
// the inbox is run even when the caller's context ends first: the loop does not
// take back what it was handed, and the caller learns only that it did not hear.
func roundTrip[T any](ctx context.Context, e *Engine, class submitClass, c command, ans call[T]) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if c.gate() == gateAdapter {
		if err := e.awaitRunning(ctx); err != nil {
			return zero, err
		}
	}
	if err := e.enqueue(ctx, class, c); err != nil {
		return zero, err
	}
	select {
	case o := <-ans.ch:
		return o.v, o.err
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-e.done:
		select {
		case o := <-ans.ch:
			return o.v, o.err
		default:
			return zero, refuse(ReasonShuttingDown)
		}
	}
}

// awaitRunning holds an adapter request until the engine serves. The wait takes
// a place of the adapter's connection: a connection whose requests all wait
// answers the next one as overloaded, which the adapter retries.
func (e *Engine) awaitRunning(ctx context.Context) error {
	select {
	case <-e.running:
		return nil
	default:
	}
	e.waiting.Add(1)
	defer e.waiting.Add(-1)
	select {
	case <-e.running:
		return nil
	case <-e.shutdown:
		return refuse(ReasonShuttingDown)
	case <-ctx.Done():
		return ctx.Err()
	case <-e.done:
		return refuse(ReasonShuttingDown)
	}
}

// ---------------------------------------------------------------------------
// The loop

func (e *Engine) loop() {
	defer e.finish()
	for {
		e.armTimer()
		if len(e.cont) > 0 && e.cont[0].due <= e.ran {
			// The commands that were ahead of the first continuation have run. The
			// timer and the stop signal still come first, so a chain of
			// continuations does not hold the loop.
			select {
			case <-e.timer.C():
				e.fireDue()
			case <-e.stopCh:
				return
			default:
				k := e.cont[0]
				e.cont = e.cont[1:]
				e.step(k.c)
			}
		} else {
			select {
			case c := <-e.inbox:
				e.ran++
				e.step(c)
			case <-e.timer.C():
				e.fireDue()
			case <-e.rearm:
			case <-e.stopCh:
				return
			}
		}
		if e.fatal == nil {
			e.serveReady()
			e.servePending()
			e.serveBlocked()
			e.serveLeases()
			e.servePrompts()
		}
		if e.fatal != nil {
			e.failLoop()
			return
		}
		if e.probed {
			e.probed = false
		} else {
			e.poke()
		}
	}
}

// parkUntilDrained holds a command until the records queued for a closed endpoint
// are all closed, and then queues it behind the commands the inbox holds. It is
// not queued again at once and tried until it passes: when a chunk fails, the
// drain is retried after RetryBackoff, and a command that spun in the meantime
// would hold the loop for nothing.
func (e *Engine) parkUntilDrained(id ident.EndpointID, c command) {
	e.parked[id] = append(e.parked[id], c)
}

// releaseParked queues what waited for the drain of the endpoint, in the order it
// came.
func (e *Engine) releaseParked(id ident.EndpointID) {
	for _, c := range e.parked[id] {
		e.later(c)
	}
	delete(e.parked, id)
}

// later queues a command for the loop itself, behind the commands the inbox holds
// now and ahead of those that arrive afterwards. It is for the loop's own
// goroutine.
func (e *Engine) later(c command) {
	e.cont = append(e.cont, continuation{c: c, due: e.ran + uint64(len(e.inbox))})
}

// armTimer points the one timer of the loop at the earliest entry of the
// schedule that may fire. Every periodic task keeps an entry, so the timer never
// sleeps for longer than the longest period: a wall clock that jumps forward is
// noticed by then.
func (e *Engine) armTimer() {
	next, ok := e.sched.next()
	if !ok {
		e.timer.Stop()
		return
	}
	e.timer.ResetAt(next)
}

// observe reads the wall clock into the high water mark: a reading far behind it
// pauses everything that depends on a deadline until the owner clears it. The
// pause is made durable before the command goes on, by running the task of the
// clock state now.
func (e *Engine) observe(now time.Time) {
	if v, _ := e.hw.Observe(now); v == clock.WallRolledBack && !e.paused {
		e.paused, e.pausedSince = true, now
		e.log.Warn("the wall clock was set back; new work that depends on a deadline is refused until the owner clears it")
		if e.clockTask != "" {
			e.fire(schedKey{schedPeriodic, e.clockTask}, now)
		}
	}
}

// clearRollback ends the pause of a wall clock set back: the clock the owner has
// looked at is the one the high water mark follows from now on. The management
// operation that asks for it persists the same change in the same transaction;
// this is the part that the loop owns.
//
// The periodic tasks are due again at once. They were scheduled from readings of
// the clock while it was wrong, and the clock was ahead for as long as the pause
// lasted may have been days: one period after such a reading is not one period
// from the clock the owner accepted, and the tasks that keep the node healthy (the
// check of the certificate, the sweep of the archive) would not run until then. A
// periodic task may run at any time, so running them now is harmless. The entries
// of the other kinds are not moved: most are deadlines of records, which are not
// relative to the reading they were made on, and the few that are relative
// backoffs have no mark that says so (see the engine reference).
func (e *Engine) clearRollback(now time.Time) {
	e.hw.Reset(now)
	e.paused, e.pausedSince = false, time.Time{}
	for _, name := range e.order {
		e.sched.set(schedKey{schedPeriodic, name}, now)
	}
	e.promptsDirty = true
	e.noteBlockedAll()
}

// step runs one command: one reading of the clock for all of it, then the phase
// gate, then the command.
func (e *Engine) step(c command) {
	e.inLoop.Store(true)
	defer e.inLoop.Store(false)
	now := e.d.Clock.Now()
	e.observe(now)
	if e.fatal != nil { // making the pause durable failed for good
		c.fail(refuse(ReasonStorageFatal))
		return
	}
	ph := e.Phase()
	switch c.gate() {
	case gateRead, gateLoopback:
		if ph >= PhaseStopping {
			c.fail(refuse(ReasonShuttingDown))
			return
		}
	case gateWrite, gateAdapter:
		switch {
		case ph == PhaseRecovering:
			c.fail(refuse(ReasonRecovering))
			return
		case ph >= PhaseStopping:
			c.fail(refuse(ReasonShuttingDown))
			return
		}
	}
	c.run(e, now)
}

// fireDue runs every entry of the schedule that is due, as one command: one
// reading of the clock, the entries in the order of their due times.
func (e *Engine) fireDue() {
	e.inLoop.Store(true)
	defer e.inLoop.Store(false)
	now := e.d.Clock.Now()
	e.observe(now)
	if e.fatal != nil {
		return
	}
	for _, key := range e.sched.takeDue(now) {
		e.fire(key, now)
		if e.fatal != nil {
			return
		}
	}
}

func (e *Engine) fire(key schedKey, now time.Time) {
	switch key.kind {
	case schedWait:
		if id, err := ident.ParseEndpointID(key.id); err == nil {
			e.expireWait(id)
		}
	case schedOutbound:
		e.dispatchOutbound(key.id, now)
	case schedFeedback:
		_ = e.runFeedback(now) // a fatal error is e.fatal, which the caller reads
	case schedFeedbackClear:
		e.retryClears(now)
	case schedTick:
		e.tickDue(key.id)
	case schedCreateDeadline, schedCreateRetry:
		k, err := store.ParseTargetKey(key.id)
		if err != nil {
			e.log.Error("the schedule names a control record that is not one", "record", key.id)
			return
		}
		if key.kind == schedCreateDeadline {
			e.createDeadline(now, k)
		} else {
			e.startCreate(now, k)
		}
	case schedExecuteDeadline, schedExecuteRetry, schedInterruptConfirm:
		k, err := store.ParseTargetKey(key.id)
		if err != nil {
			e.log.Error("the schedule names a control record that is not one", "record", key.id)
			return
		}
		switch key.kind {
		case schedExecuteDeadline:
			e.executeDeadline(now, k)
		case schedExecuteRetry:
			e.executeRetry(now, k)
		default:
			e.interruptConfirmDue(now, k)
		}
	case schedReleaseRetry, schedReleaseDeadline:
		id, err := ident.ParseEndpointID(key.id)
		if err != nil {
			e.log.Error("the schedule names an endpoint that is not one", "endpoint", key.id)
			return
		}
		if key.kind == schedReleaseRetry {
			e.startRelease(now, id)
		} else {
			e.releaseDeadline(now, id)
		}
	case schedDrain:
		if id, err := ident.ParseEndpointID(key.id); err == nil {
			e.later(&cmdDrain{endpoint: id})
		}
	case schedPromptRetry:
		e.promptsDirty = true
	case schedBlocked:
		if id, err := ident.ParseEndpointID(key.id); err == nil {
			e.blockedDirty[id] = true
		}
	case schedRenew:
		if id, err := ident.ParseEndpointID(key.id); err == nil {
			e.markLeasesFor(id)
		}
	case schedProbeFallback:
		e.probeFallback(now, key.id)
	case schedPromptReconcile:
		if id, err := ident.ParseEndpointID(key.id); err == nil {
			e.startReconcile(now, id)
		}
	case schedPromptCallDeadline:
		if id, err := ident.ParseEndpointID(key.id); err == nil {
			e.promptCallDeadline(id)
		}
	case schedPromptSettle:
		if id, err := ident.ParseEndpointID(key.id); err == nil {
			e.fireSettle(now, id)
		}
	case schedInstanceDrain:
		e.later(&cmdInstanceDrain{instance: key.id})
	case schedPeriodic:
		p := e.periodic[key.id]
		err := p.task(TaskInput{Now: now, HighWater: e.hw.Value(), RollbackPaused: e.paused, PausedSince: e.pausedSince, SignRefusal: e.signRefusal()})
		switch {
		case err == nil:
			e.sched.set(key, now.Add(p.every))
		case txn.IsFatal(err):
			e.fatal = err
		default:
			e.log.Warn("periodic task failed; it is tried again", "task", key.id, "error", err.Error())
			e.sched.set(key, now.Add(e.p.RetryBackoff))
		}
	}
}

// failLoop ends the loop after a fatal storage error: nothing more is run and
// nothing more is written, and the process is to exit and rescan.
func (e *Engine) failLoop() {
	e.errMu.Lock()
	e.err = e.fatal
	e.errMu.Unlock()
	e.setPhase(PhaseFailed)
	e.log.Error("fatal storage error; the engine stops", "error", e.fatal.Error())
}

// classify turns the error of a write into what the caller is told. A refusal
// stays as it is. A failure that is fatal stops the loop after the command and
// is answered as an outcome that is not known. A write that failed before its
// commit point is back pressure, with the cause: nothing was acknowledged.
// Anything else is an unexpected failure and is returned as it is.
func (e *Engine) classify(err error) error {
	var ref *Refusal
	var te *txn.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ref):
		return err
	case txn.IsFatal(err):
		if e.fatal == nil {
			e.fatal = err
		}
		return &Refusal{Reason: ReasonStorageFatal, Err: err}
	case fsx.IsNoSpace(err):
		return &Refusal{Reason: ReasonStorageFull, Err: err}
	case errors.As(err, &te):
		return &Refusal{Reason: ReasonStorageUnavailable, Err: err}
	}
	return err
}

// randomReader is the reader the loop draws from.
func (e *Engine) randomReader() io.Reader { return e.d.Rand }
