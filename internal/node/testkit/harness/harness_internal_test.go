package harness

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent/engineagent"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/internal/node/txn"
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

func (o *holdFirst) Reached(p fsx.Point, _ txn.Kind) {
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
