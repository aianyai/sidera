package harness

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent/engineagent"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// recorder is a testing.TB that records what the checks under test report
// instead of failing the real test.
type recorder struct {
	testing.TB
	errs     []string
	cleanups []func()
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(f string, a ...any) {
	r.errs = append(r.errs, strings.TrimSpace(fmt.Sprintf(f, a...)))
}
func (r *recorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }
func (r *recorder) finish() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}

func TestQuiesceWaitsForEveryConditionOfTheFixtureAndNamesTheOneThatDoesNotHold(t *testing.T) {
	h := New(t, Options{})
	var open atomic.Bool
	h.addQuiet("the fake is idle", func() (bool, uint64) { return open.Load(), 0 })

	err := h.Quiesce(scenario.WithTimeout(100 * time.Millisecond))
	var qe *QuiesceError
	if !errors.As(err, &qe) {
		t.Fatalf("Quiesce returned %v, want a *QuiesceError", err)
	}
	if len(qe.Unmet) != 1 || qe.Unmet[0] != "the fake is idle" {
		t.Errorf("unmet %v, want the fake's condition", qe.Unmet)
	}
	if !strings.Contains(err.Error(), "the fake is idle") {
		t.Errorf("error %q does not name the condition", err)
	}
	open.Store(true)
	if err := h.Quiesce(); err != nil {
		t.Fatalf("Quiesce once the condition holds: %v", err)
	}
}

// Work the engine does can make the fakes busy again, so a round in which a
// condition of the fixture changed is not the last one.
func TestQuiesceChecksTheFixtureAgainAfterTheEngineWasQuiet(t *testing.T) {
	h := New(t, Options{})
	var checks atomic.Int32
	h.addQuiet("busy twice", func() (bool, uint64) { return checks.Add(1) > 3, 0 })
	if err := h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	if got := checks.Load(); got < 4 {
		t.Errorf("the condition was asked %d times, want at least 4", got)
	}
}

func TestQuiesceRefusesToWaitOnAContextThatIsOver(t *testing.T) {
	h := New(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.quiesce(ctx, scenario.QuiesceOptions{Timeout: time.Minute}); !errors.Is(err, context.Canceled) {
		t.Errorf("quiesce on a cancelled context returned %v", err)
	}
}

// own is a goroutine of the fixture's own packages, started by a function of this
// file, which the leak check finds by the marker it is given.
func leaker(stop chan struct{}) { <-stop }

// earlier is a goroutine that is already running when a check is set up.
func earlier(stop chan struct{}) { <-stop }

func waitUntilRunning(marker string) {
	for len(marked([]string{marker})) == 0 {
		runtime.Gosched()
	}
}

func TestTheLeakCheckReportsAGoroutineOfTheMarkedPackages(t *testing.T) {
	rec := &recorder{TB: t}
	checkNoLeaks(rec, []string{"harness.leaker"}, 100*time.Millisecond)
	stop := make(chan struct{})
	go leaker(stop)
	waitUntilRunning("harness.leaker") // a goroutine that has not started yet is not in the stacks
	rec.finish()
	if len(rec.errs) == 0 {
		t.Error("the leak check did not see the goroutine that is still running")
	}
	close(stop)

	// A goroutine that was running before the check was set up is not the test's.
	stop = make(chan struct{})
	go earlier(stop)
	waitUntilRunning("harness.earlier")
	rec = &recorder{TB: t}
	checkNoLeaks(rec, []string{"harness.earlier"}, 100*time.Millisecond)
	rec.finish()
	if len(rec.errs) != 0 {
		t.Errorf("the leak check reported a goroutine that was there before it: %v", rec.errs)
	}
	close(stop)

	// A goroutine that ends in time is not reported.
	rec = &recorder{TB: t}
	checkNoLeaks(rec, []string{"harness.leaker"}, 2*time.Second)
	stop = make(chan struct{})
	go leaker(stop)
	close(stop)
	rec.finish()
	if len(rec.errs) != 0 {
		t.Errorf("the leak check reported a goroutine that had ended: %v", rec.errs)
	}
}

// A condition of the fixture that holds, stops holding while the engine settles
// and holds again is the case the second look is for: quiet is both at once.
func TestQuiesceDoesNotReturnOnAConditionThatStoppedHoldingWhileTheEngineSettled(t *testing.T) {
	h := New(t, Options{})
	var checks atomic.Int32
	h.addQuiet("busy once, in the middle", func() (bool, uint64) { return checks.Add(1) != 2, 0 })
	if err := h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	if got := checks.Load(); got < 3 {
		t.Errorf("the condition was asked %d times: the answer of the second look was not used", got)
	}
}

// holdFirst holds the first step of one transaction until it is released.
type holdFirst struct {
	tx      string
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (o *holdFirst) Reached(observation fsx.Observation, _ txn.Kind) {
	p := observation.Point

	if p.Tag.Tx != o.tx {
		return
	}
	o.once.Do(func() {
		close(o.reached)
		<-o.release
	})
}

// A registration that a crash interrupted has no answer: the name is not taken
// from a process that was abandoned, since nothing says the endpoint exists in
// the one that runs now.
func TestADirectCallThatWasInFlightAtACrashIsNotRecordedUnderItsName(t *testing.T) {
	o := &holdFirst{tx: "direct-register", reached: make(chan struct{}), release: make(chan struct{})}
	h := New(t, Options{Observer: o})
	done := make(chan error, 1)
	go func() { _, err := h.Direct("a"); done <- err }()
	<-o.reached
	h.Crash(ProcessCrash)
	close(o.release)
	err := <-done
	if got := fakeagent.CategoryOf(engineagent.MapError(err)); got != fakeagent.CategoryOutcomeUnknown {
		t.Errorf("Direct returned %v, which an agent would see as %q, want outcome_unknown", err, got)
	}
	h.mu.Lock()
	_, recorded := h.names["a"]
	h.mu.Unlock()
	if recorded {
		t.Error("the name of a registration that a crash interrupted was recorded")
	}
}

// A fake that takes work while the engine is asked, finishes it, emits a
// notification and is idle again holds on both looks, but the engine has not seen
// that notification: only the count shows it. Quiet is a round in which no count
// moved.
func TestQuiesceDoesNotReturnWhileAConditionThatKeepsHoldingStillMakesProgress(t *testing.T) {
	h := New(t, Options{})
	var asks atomic.Int32
	const settles = 5 // the count moves with each of the first asks, then stays
	h.addQuiet("a fake that is always idle and keeps sending", func() (bool, uint64) {
		return true, uint64(min(asks.Add(1), settles))
	})
	if err := h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	if got := asks.Load(); got < settles+1 {
		t.Errorf("the condition was asked %d times: Quiesce returned before it saw the count stand still across a round", got)
	}
}

// The count of a condition that stands still is what lets a round be the last.
func TestQuiesceReturnsAfterOneRoundWhenNoCountMoves(t *testing.T) {
	h := New(t, Options{})
	var asks atomic.Int32
	h.addQuiet("a fake that is idle and silent", func() (bool, uint64) {
		asks.Add(1)
		return true, 7
	})
	if err := h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	if got := asks.Load(); got != 2 {
		t.Errorf("the condition was asked %d times, want once before the engine and once after it", got)
	}
}

// The report says what was found. A loop that never answered is inside a command;
// an engine that refused the probe (it is shutting down) answered, and a fixture
// condition that did not hold never reached the engine at all: neither is a
// command that is still running.
func TestAQuiesceErrorSaysACommandIsRunningOnlyWhenTheLoopDidNotAnswer(t *testing.T) {
	const running = "a command is still running"

	h := New(t, Options{})
	if err := h.Engine().Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := h.Quiesce(scenario.WithTimeout(time.Second))
	var qe *QuiesceError
	if !errors.As(err, &qe) {
		t.Fatalf("Quiesce on a stopped engine returned %v, want a *QuiesceError", err)
	}
	if strings.Contains(err.Error(), running) {
		t.Errorf("error %q blames a running command for the engine's refusal", err)
	}
	if r, ok := engine.ReasonOf(err); !ok || r != engine.ReasonShuttingDown {
		t.Errorf("error %v does not carry the engine's refusal", err)
	}

	h = New(t, Options{})
	h.addQuiet("a fake that is busy", func() (bool, uint64) { return false, 0 })
	err = h.Quiesce(scenario.WithTimeout(50 * time.Millisecond))
	if strings.Contains(err.Error(), running) {
		t.Errorf("error %q blames a running command although the engine was never asked", err)
	}
}

// adaptersDouble is an engine.Adapters that records the calls that reach it and,
// when told to, holds each one until it is released, so that a test can abandon the
// process while a call is in flight.
type adaptersDouble struct {
	mu      sync.Mutex
	reached []string
	hold    bool
	entered chan string
	release chan struct{}
}

func newAdaptersDouble(hold bool) *adaptersDouble {
	return &adaptersDouble{hold: hold, entered: make(chan string, 16), release: make(chan struct{})}
}

func (d *adaptersDouble) note(method string) {
	d.mu.Lock()
	d.reached = append(d.reached, method)
	d.mu.Unlock()
	if d.hold {
		d.entered <- method
		<-d.release
	}
}

func (d *adaptersDouble) calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.reached...)
}

var errDouble = errors.New("the double's own answer")

func (d *adaptersDouble) EnsureRunning(string) { d.note("EnsureRunning") }
func (d *adaptersDouble) Stop(string)          { d.note("Stop") }
func (d *adaptersDouble) Stopping(string) bool { return false }
func (d *adaptersDouble) NotifyPending(string, adapterproto.PendingParams) {
	d.note("NotifyPending")
}
func (d *adaptersDouble) CreateSession(context.Context, string, adapterproto.SessionCreateParams) (adapterproto.SessionCreateResult, error) {
	d.note("CreateSession")
	return adapterproto.SessionCreateResult{Outcome: adapterproto.CreateCreated}, errDouble
}
func (d *adaptersDouble) DeliverPrompt(context.Context, string, adapterproto.PromptDeliverParams) (adapterproto.PromptDeliverResult, error) {
	d.note("DeliverPrompt")
	return adapterproto.PromptDeliverResult{Stage: adapterproto.PromptAccepted}, errDouble
}
func (d *adaptersDouble) Reconcile(context.Context, string, adapterproto.ReconcileParams) (adapterproto.ReconcileResult, error) {
	d.note("Reconcile")
	return adapterproto.ReconcileResult{Verdict: adapterproto.VerdictFound}, errDouble
}
func (d *adaptersDouble) Interrupt(context.Context, string, adapterproto.InterruptParams) (adapterproto.InterruptResult, error) {
	d.note("Interrupt")
	return adapterproto.InterruptResult{Stage: adapterproto.InterruptRequested}, errDouble
}
func (d *adaptersDouble) Close(context.Context, string, adapterproto.CloseParams) (adapterproto.CloseResult, error) {
	d.note("Close")
	return adapterproto.CloseResult{Outcome: adapterproto.CloseClosed}, errDouble
}
func (d *adaptersDouble) Counts(string) adapterhost.ConnCounts {
	return adapterhost.ConnCounts{Connected: true, Handled: 3, Buffered: 1}
}

// call is one method of engine.Adapters made through a wrapper; a method that has a
// result returns the error and whether the result came back as the double made it.
type adapterCall struct {
	name string
	// result is true for a method that answers the engine with a result and an error.
	result bool
	run    func(a engine.Adapters) (zero bool, err error)
}

var adapterCalls = []adapterCall{
	{"EnsureRunning", false, func(a engine.Adapters) (bool, error) { a.EnsureRunning("chat"); return true, nil }},
	{"Stop", false, func(a engine.Adapters) (bool, error) { a.Stop("chat"); return true, nil }},
	{"NotifyPending", false, func(a engine.Adapters) (bool, error) {
		a.NotifyPending("chat", adapterproto.PendingParams{})
		return true, nil
	}},
	{"CreateSession", true, func(a engine.Adapters) (bool, error) {
		r, err := a.CreateSession(context.Background(), "chat", adapterproto.SessionCreateParams{})
		return reflect.ValueOf(r).IsZero(), err
	}},
	{"DeliverPrompt", true, func(a engine.Adapters) (bool, error) {
		r, err := a.DeliverPrompt(context.Background(), "chat", adapterproto.PromptDeliverParams{})
		return reflect.ValueOf(r).IsZero(), err
	}},
	{"Reconcile", true, func(a engine.Adapters) (bool, error) {
		r, err := a.Reconcile(context.Background(), "chat", adapterproto.ReconcileParams{})
		return reflect.ValueOf(r).IsZero(), err
	}},
	{"Interrupt", true, func(a engine.Adapters) (bool, error) {
		r, err := a.Interrupt(context.Background(), "chat", adapterproto.InterruptParams{})
		return reflect.ValueOf(r).IsZero(), err
	}},
	{"Close", true, func(a engine.Adapters) (bool, error) {
		r, err := a.Close(context.Background(), "chat", adapterproto.CloseParams{})
		return reflect.ValueOf(r).IsZero(), err
	}},
}

// While the process lives, a call goes through and comes back as the host made it.
func TestACallToTheAdapterHostOfALiveProcessIsPassedOn(t *testing.T) {
	for _, c := range adapterCalls {
		t.Run(c.name, func(t *testing.T) {
			d := newAdaptersDouble(false)
			var abandoned atomic.Bool
			zero, err := c.run(abandonable(d, &abandoned))
			if got := d.calls(); len(got) != 1 || got[0] != c.name {
				t.Fatalf("the host was called %v, want %s once", got, c.name)
			}
			if c.result && (zero || !errors.Is(err, errDouble)) {
				t.Errorf("the result came back as zero=%v err=%v, want the double's own", zero, err)
			}
		})
	}
}

// A call made after the process was abandoned is never sent: a stale engine must
// not act on an adapter, and nobody could read what it would answer.
func TestACallToTheAdapterHostAfterTheProcessWasAbandonedIsNeverSent(t *testing.T) {
	for _, c := range adapterCalls {
		t.Run(c.name, func(t *testing.T) {
			d := newAdaptersDouble(false)
			var abandoned atomic.Bool
			a := abandonable(d, &abandoned)
			abandoned.Store(true)
			zero, err := c.run(a)
			if got := d.calls(); len(got) != 0 {
				t.Errorf("the host was called %v after the process was abandoned", got)
			}
			if c.result && (!zero || !errors.Is(err, engineagent.ErrProcessEnded)) {
				t.Errorf("the call returned zero=%v err=%v, want a zero result and ErrProcessEnded", zero, err)
			}
		})
	}
}

// A call that was in flight when the process was abandoned returns after it: the
// engine of that process is gone, so whatever the host answered is not an answer
// anyone read, and the call must never be a confirmation.
func TestACallToTheAdapterHostThatReturnsAfterTheProcessWasAbandonedIsNeverAConfirmation(t *testing.T) {
	for _, c := range adapterCalls {
		if !c.result {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			d := newAdaptersDouble(true)
			var abandoned atomic.Bool
			a := abandonable(d, &abandoned)
			type outcome struct {
				zero bool
				err  error
			}
			done := make(chan outcome, 1)
			go func() { zero, err := c.run(a); done <- outcome{zero, err} }()
			if got := <-d.entered; got != c.name {
				t.Fatalf("the call that reached the host is %s", got)
			}
			abandoned.Store(true)
			close(d.release)
			o := <-done
			if !o.zero || !errors.Is(o.err, engineagent.ErrProcessEnded) {
				t.Errorf("the call returned zero=%v err=%v, want a zero result and ErrProcessEnded", o.zero, o.err)
			}
			if !strings.Contains(o.err.Error(), errDouble.Error()) {
				t.Errorf("the error %q does not say what the host answered", o.err)
			}
		})
	}
}

// What the host counts is not a call that can be confirmed: the quiet check reads
// it for the process that runs, and it is the same before and after.
func TestTheCountsOfTheAdapterHostPassThroughWhetherOrNotTheProcessWasAbandoned(t *testing.T) {
	d := newAdaptersDouble(false)
	var abandoned atomic.Bool
	a := abandonable(d, &abandoned)
	want := adapterhost.ConnCounts{Connected: true, Handled: 3, Buffered: 1}
	if got := a.Counts("chat"); got.Handled != want.Handled || got.Buffered != want.Buffered || !got.Connected {
		t.Errorf("Counts = %+v, want %+v", got, want)
	}
	abandoned.Store(true)
	if got := a.Counts("chat"); got.Handled != want.Handled || got.Buffered != want.Buffered || !got.Connected {
		t.Errorf("Counts after the process was abandoned = %+v, want %+v", got, want)
	}
}

// Opening the same ledger stream twice would fork its sequence, so every launch of
// an instance, however many are alive at once, writes through the one writer the
// launcher opened for it.
func TestEveryLaunchOfAnInstanceWritesThroughTheSameLedgerWriter(t *testing.T) {
	clk := clocktest.NewFake(DefaultStart)
	l := NewAdapterLauncher(LauncherConfig{
		T: t, Ctx: context.Background(), Now: clk.Now, Ledgers: ledger.NewMemory(),
		Manifest: AdapterManifest(adapterproto.MaxDirectEndpoints),
	})
	t.Cleanup(l.Close)
	inst := adapterhost.Instance{ID: Instance, LockPath: t.TempDir() + "/adapter-chat.lock"}
	for range 2 {
		if _, err := l.Launch(context.Background(), inst); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(l.Launches(Instance)); n != 2 {
		t.Fatalf("%d launches, want 2", n)
	}
	l.mu.Lock()
	writers := len(l.writers)
	l.mu.Unlock()
	if writers != 1 {
		t.Errorf("%d ledger writers for one instance, want 1", writers)
	}
}

func TestProbeWaitFollowsTheLatestLaunchAndAcceptsItsEnd(t *testing.T) {
	l := NewAdapterLauncher(LauncherConfig{
		T: t, Ctx: context.Background(), Now: clocktest.NewFake(DefaultStart).Now,
		Ledgers: ledger.NewMemory(), Manifest: AdapterManifest(adapterproto.MaxDirectEndpoints),
	})
	t.Cleanup(l.Close)
	inst := adapterhost.Instance{ID: Instance, LockPath: t.TempDir() + "/adapter-chat.lock"}
	for range 2 {
		if _, err := l.Launch(context.Background(), inst); err != nil {
			t.Fatal(err)
		}
	}
	ls := l.Launches(Instance)
	ls[0].wrote([]byte(`{"method":"adapter.probe"}`))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := l.awaitProbe(ctx, Instance); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unprobed latest launch with a probed predecessor: %v", err)
	}
	ls[1].End()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := l.awaitProbe(ctx, Instance); err != nil {
		t.Fatalf("ended latest launch: %v", err)
	}
}
