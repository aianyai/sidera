package engine

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/record"
)

// The hooks the tests of the engine use to reach what only the loop may touch.

type cmdStall struct {
	started chan struct{}
	release chan struct{}
}

func (c *cmdStall) gate() gate             { return gateAlways }
func (c *cmdStall) fail(error)             {}
func (c *cmdStall) run(*Engine, time.Time) { close(c.started); <-c.release }

// StallLoop makes the loop stop taking commands until the returned function is
// called, with the command that holds it already running.
func StallLoop(t testing.TB, e *Engine) (release func()) {
	t.Helper()
	c := &cmdStall{started: make(chan struct{}), release: make(chan struct{})}
	if err := e.enqueue(context.Background(), submitInternal, c); err != nil {
		t.Fatalf("StallLoop: %v", err)
	}
	select {
	case <-c.started:
	case <-time.After(10 * time.Second):
		t.Fatal("StallLoop: the loop did not take the command")
	}
	var once sync.Once
	release = func() { once.Do(func() { close(c.release) }) }
	t.Cleanup(release)
	return release
}

// WaitInbox returns when n commands wait in the inbox.
func WaitInbox(t testing.TB, e *Engine, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(e.inbox) != n {
		if time.Now().After(deadline) {
			t.Fatalf("WaitInbox: %d commands wait, want %d", len(e.inbox), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// WaitingForRunning is how many adapter requests wait for the running phase.
func WaitingForRunning(e *Engine) int { return int(e.waiting.Load()) }

type cmdProbeAdmit struct {
	call[struct{}]
	recipients int
}

func (c *cmdProbeAdmit) gate() gate { return gateAlways }
func (c *cmdProbeAdmit) run(e *Engine, _ time.Time) {
	c.done(struct{}{}, e.classify(e.admitNewWork(c.recipients)))
}

// AdmitNewWork runs the capacity check as a command of the loop, the way the
// intake of new work does, for recipients recipients.
func AdmitNewWork(ctx context.Context, e *Engine, recipients int) error {
	c := &cmdProbeAdmit{call: newCall[struct{}](), recipients: recipients}
	_, err := roundTrip(ctx, e, submitExternal, c, c.call)
	return err
}

// SpawnWorker starts a goroutine the engine owns and counts, as the workers of
// the workers added later are: from a command of the loop, the only place the
// engine starts one.
func SpawnWorker(e *Engine, _ string, fn func(ctx context.Context)) {
	if err := InLoop(context.Background(), e, func(l LoopSide) { l.e.spawn(fn) }); err != nil {
		panic("SpawnWorker: " + err.Error())
	}
}

// SpawnOffTheLoop calls spawn from the goroutine of the caller, which the engine
// must refuse.
func SpawnOffTheLoop(e *Engine) { e.spawn(func(context.Context) {}) }

type cmdClearRollback struct{ call[struct{}] }

func (c *cmdClearRollback) gate() gate { return gateAlways }
func (c *cmdClearRollback) run(e *Engine, now time.Time) {
	e.clearRollback(now)
	c.done(struct{}{}, nil)
}

// ClearRollback is what clearing the pause of a wall clock set back does inside
// the loop; the management operation that persists it is added later.
func ClearRollback(ctx context.Context, e *Engine) error {
	c := &cmdClearRollback{call: newCall[struct{}]()}
	_, err := roundTrip(ctx, e, submitExternal, c, c.call)
	return err
}

type cmdViewVsDisk struct {
	call[[]string]
	sc *store.Scan
}

func (c *cmdViewVsDisk) gate() gate { return gateAlways }
func (c *cmdViewVsDisk) run(e *Engine, _ time.Time) {
	var diff []string
	fresh, err := buildView(e.d.Node, c.sc)
	if err != nil {
		c.done(nil, err)
		return
	}
	same := func(what string, a, b store.Record) {
		x, err1 := store.EncodeRecord(a)
		y, err2 := store.EncodeRecord(b)
		if err1 != nil || err2 != nil || !bytes.Equal(x, y) {
			diff = append(diff, what)
		}
	}
	for k, in := range fresh.in {
		if mine, ok := e.view.in[k]; !ok {
			diff = append(diff, "in "+k.String()+" is missing from the view")
		} else {
			same("in "+k.String(), mine, in)
		}
	}
	for k := range e.view.in {
		if _, ok := fresh.in[k]; !ok {
			diff = append(diff, "in "+k.String()+" is in the view only")
		}
	}
	for k, ctl := range fresh.ctl {
		if mine, ok := e.view.ctl[k]; !ok {
			diff = append(diff, "ctl "+k.String()+" is missing from the view")
		} else {
			same("ctl "+k.String(), mine, ctl)
		}
	}
	if e.view.unfinished != fresh.unfinished {
		diff = append(diff, fmt.Sprintf("unfinished %d, from the disk %d", e.view.unfinished, fresh.unfinished))
	}
	if !reflect.DeepEqual(e.view.bodyRefs, fresh.bodyRefs) {
		diff = append(diff, fmt.Sprintf("body references %v, from the disk %v", e.view.bodyRefs, fresh.bodyRefs))
	}
	if len(e.view.queued) != len(fresh.queued) {
		diff = append(diff, "the queues differ")
	}
	c.done(diff, nil)
}

// ViewDiffersFromDisk lists where the view of the loop differs from the view a
// fresh scan builds: records by their encoding, the count of unfinished
// deliveries, the references to bodies.
func ViewDiffersFromDisk(ctx context.Context, e *Engine, sc *store.Scan) ([]string, error) {
	c := &cmdViewVsDisk{call: newCall[[]string](), sc: sc}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}

// TestWait is a waiter registered through the hooks below.
type TestWait struct{ w *waiter }

type cmdTestWait struct {
	call[struct{}]
	w *waiter
}

func (c *cmdTestWait) gate() gate { return gateAlways }
func (c *cmdTestWait) run(e *Engine, _ time.Time) {
	e.waitFor(c.w)
	c.done(struct{}{}, nil)
}

// WaitFor registers a waiter for the endpoint, in a command of the loop.
func WaitFor(ctx context.Context, e *Engine, endpoint ident.EndpointID, deadline time.Time) (*TestWait, error) {
	c := &cmdTestWait{call: newCall[struct{}](), w: newWaiter(endpoint, deadline)}
	if _, err := roundTrip(ctx, e, submitInternal, c, c.call); err != nil {
		return nil, err
	}
	return &TestWait{w: c.w}, nil
}

// AwaitWait waits the way the caller of a recv does, and says how the wait
// ended.
func AwaitWait(ctx context.Context, e *Engine, t *TestWait) (end string, page []byte, err error) {
	o, err := e.awaitWaiter(ctx, t.w)
	if err != nil {
		return "", nil, err
	}
	return o.End.String(), o.Page, nil
}

// Outcome says how the wait ended, if it has: the name of the end and the page.
func (t *TestWait) Outcome() (end string, page []byte, ended bool) {
	select {
	case o := <-t.w.out:
		return o.End.String(), o.Page, true
	default:
		return "", nil, false
	}
}

type cmdTestWake struct {
	call[bool]
	endpoint ident.EndpointID
	page     []byte
}

func (c *cmdTestWake) gate() gate { return gateAlways }
func (c *cmdTestWake) run(e *Engine, _ time.Time) {
	c.done(e.wake(c.endpoint, c.page), nil)
}

// Wake hands a page to the waiter of the endpoint, in a command of the loop.
func Wake(ctx context.Context, e *Engine, endpoint ident.EndpointID, page []byte) (bool, error) {
	c := &cmdTestWake{call: newCall[bool](), endpoint: endpoint, page: page}
	return roundTrip(ctx, e, submitInternal, c, c.call)
}

// Unwait drops the wait, as the caller that gave up does.
func Unwait(ctx context.Context, e *Engine, t *TestWait) error {
	return e.enqueue(ctx, submitInternal, cmdUnwait{w: t.w})
}

// PutBack makes the outcome a poll read available again.
func PutBack(t *TestWait, end string, page []byte) {
	var how waitEnd
	for h := waitWoken; h <= waitShutdown; h++ {
		if h.String() == end {
			how = h
		}
	}
	t.w.out <- waitOutcome{End: how, Page: page}
}

// cmdFn is a command that runs a function in the loop. It is answered to no one:
// a test that needs the end of it waits with Quiesce.
type cmdFn struct{ fn func() }

func (c cmdFn) gate() gate             { return gateAlways }
func (c cmdFn) fail(error)             {}
func (c cmdFn) run(*Engine, time.Time) { c.fn() }

// LoopSide is what a hook that runs in the loop may do besides what it is for.
type LoopSide struct{ e *Engine }

// Later queues fn as a continuation of the loop, as the chunks of a long batch
// are queued.
func (l LoopSide) Later(fn func()) { l.e.later(cmdFn{fn}) }

// Submit puts fn in the inbox behind what it holds, without waiting; it reports
// whether there was room.
func (l LoopSide) Submit(fn func()) bool {
	select {
	case l.e.inbox <- cmdFn{fn}:
		return true
	default:
		return false
	}
}

type cmdHook struct {
	call[struct{}]
	fn func(LoopSide)
}

func (c *cmdHook) gate() gate { return gateAlways }
func (c *cmdHook) run(e *Engine, _ time.Time) {
	c.fn(LoopSide{e})
	c.done(struct{}{}, nil)
}

// InLoop runs fn as a command of the loop and returns when it has run.
func InLoop(ctx context.Context, e *Engine, fn func(LoopSide)) error {
	c := &cmdHook{call: newCall[struct{}](), fn: fn}
	_, err := roundTrip(ctx, e, submitInternal, c, c.call)
	return err
}

// cmdRefill puts itself back in the inbox every time it runs, until it is told to
// stop: with a few of them the inbox is never empty.
type cmdRefill struct{ stop *atomic.Bool }

func (c cmdRefill) gate() gate { return gateAlways }
func (c cmdRefill) fail(error) {}
func (c cmdRefill) run(e *Engine, _ time.Time) {
	if c.stop.Load() {
		return
	}
	select {
	case e.inbox <- c:
	default:
	}
}

// KeepInboxNonEmpty fills the inbox with n commands that put themselves back each
// time the loop runs one, so that it never holds fewer than n, until stop is
// called.
func KeepInboxNonEmpty(t testing.TB, e *Engine, n int) (stop func()) {
	t.Helper()
	var flag atomic.Bool
	for i := 0; i < n; i++ {
		if err := e.enqueue(context.Background(), submitInternal, cmdRefill{&flag}); err != nil {
			t.Fatalf("KeepInboxNonEmpty: %v", err)
		}
	}
	var once sync.Once
	stop = func() { once.Do(func() { flag.Store(true) }) }
	t.Cleanup(stop)
	return stop
}

// TestRun is what an operation made by NewTestOperation is told of the command
// that runs it.
type TestRun struct {
	Now         time.Time
	Key         admin.RequestKey
	Fingerprint admin.Fingerprint
	Expected    admin.Version
	Name        string
}

// NewTestOperation is an operation whose effect is the given function, run in
// the loop like the engine's own operations. It lets the tests of the door show
// what the door does for an operation whose result is kept somewhere other than
// the one place the engine's two operations keep theirs.
func NewTestOperation(op admin.Operation, stops bool, run func(TestRun) ([]byte, error)) Operation {
	return &testOperation{op: op, stop: stops, run_: run}
}

type testOperation struct {
	op   admin.Operation
	stop bool
	run_ func(TestRun) ([]byte, error)
}

func (o *testOperation) Admin() admin.Operation { return o.op }
func (o *testOperation) stops() bool            { return o.stop }
func (o *testOperation) run(_ *Engine, x *opRun) ([]byte, error) {
	return o.run_(TestRun{Now: x.now, Key: x.key, Fingerprint: x.fingerprint, Expected: x.expected, Name: x.name})
}

type cmdSign struct {
	call[record.Record]
	in store.InRecord
}

func (c *cmdSign) gate() gate { return gateAlways }
func (c *cmdSign) run(e *Engine, now time.Time) {
	rec, err := e.signReport(now, c.in)
	c.done(rec, e.classify(err))
}

// SignReport runs the signing of a report of the target record as a command of the
// loop, the way a pull and an intake sign.
func SignReport(ctx context.Context, e *Engine, in store.InRecord) (record.Record, error) {
	c := &cmdSign{call: newCall[record.Record](), in: in}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}
