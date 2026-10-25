package harness

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/scenario"
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
	h.addQuiet("the fake is idle", func() bool { return open.Load() })

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
	h.addQuiet("busy twice", func() bool { return checks.Add(1) > 3 })
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

func TestTheLeakCheckReportsAGoroutineOfTheMarkedPackages(t *testing.T) {
	rec := &recorder{TB: t}
	checkNoLeaks(rec, []string{"harness.leaker"}, 100*time.Millisecond)
	stop := make(chan struct{})
	go leaker(stop)
	rec.finish()
	if len(rec.errs) == 0 {
		t.Error("the leak check did not see the goroutine that is still running")
	}
	close(stop)

	// A goroutine that was running before the check was set up is not the test's.
	stop = make(chan struct{})
	go earlier(stop)
	for len(marked([]string{"harness.earlier"})) == 0 {
		runtime.Gosched()
	}
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
