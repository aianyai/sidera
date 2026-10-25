package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
)

// runs records when each task ran, by the clock the loop was given.
type runs struct {
	mu  sync.Mutex
	log []string
	at  map[string][]time.Time
}

func newRuns() *runs { return &runs{at: map[string][]time.Time{}} }

func (r *runs) task(name string, fail func(n int) error) engine.Task {
	return func(in engine.TaskInput) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.log = append(r.log, name)
		r.at[name] = append(r.at[name], in.Now)
		if fail != nil {
			return fail(len(r.at[name]))
		}
		return nil
	}
}

func (r *runs) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.at[name])
}

func (r *runs) order() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.log...)
}

// advance moves the test clock and waits until the engine has done what that
// made due.
func advance(t testing.TB, r *rig, e *engine.Engine, d time.Duration) {
	t.Helper()
	r.clk.Advance(d)
	settle(t, e)
}

func openWith(t testing.TB, r *rig, add func(e *engine.Engine)) *engine.Engine {
	t.Helper()
	e, _, err := r.open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	add(e)
	e.Start()
	if err := e.RecoverDelivery(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Serve()
	r.eng = e
	t.Cleanup(func() { r.stop(e) })
	return e
}

// A periodic task is an entry of the scheduler: due one period after the engine
// started, and again one period after each run. Time is the injected clock's:
// nothing happens until the test moves it.
func TestAPeriodicTaskRunsOncePerPeriodOfTheInjectedClock(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	rs := newRuns()
	e := openWith(t, r, func(e *engine.Engine) {
		if err := e.AddPeriodic("tick", time.Minute, rs.task("tick", nil)); err != nil {
			t.Fatal(err)
		}
	})
	settle(t, e)
	if n := rs.count("tick"); n != 0 {
		t.Fatalf("the task ran %d times before a period passed", n)
	}
	advance(t, r, e, 59*time.Second)
	if n := rs.count("tick"); n != 0 {
		t.Fatalf("the task ran %d times 59 seconds in", n)
	}
	advance(t, r, e, time.Second)
	if n := rs.count("tick"); n != 1 {
		t.Fatalf("the task ran %d times after one period, want 1", n)
	}
	// A clock that jumps over several periods runs it once, not once for each.
	advance(t, r, e, 5*time.Minute)
	if n := rs.count("tick"); n != 2 {
		t.Errorf("after a jump of five periods the task ran %d times, want 2", n)
	}
	advance(t, r, e, time.Minute)
	if n := rs.count("tick"); n != 3 {
		t.Errorf("one period after the run the task ran %d times, want 3", n)
	}
	if got, want := rs.at["tick"][0], t0.Add(time.Minute); !got.Equal(want) {
		t.Errorf("the first run saw the time %v, want %v", got, want)
	}
}

// Entries that are due at the same time run in the order they were added, so
// that the sequence of the writes of a command does not depend on the heap.
func TestEntriesDueTogetherRunInTheOrderTheyWereAdded(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	rs := newRuns()
	e := openWith(t, r, func(e *engine.Engine) {
		for _, name := range []string{"first", "second", "third"} {
			if err := e.AddPeriodic(name, time.Minute, rs.task(name, nil)); err != nil {
				t.Fatal(err)
			}
		}
	})
	advance(t, r, e, time.Minute)
	advance(t, r, e, time.Minute)
	want := []string{"first", "second", "third", "first", "second", "third"}
	if got := rs.order(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Errorf("order %v, want %v", got, want)
	}
}

// A task that fails with an error a retry may cure is run again after the
// back off, not at once and not only a period later; after it succeeds the
// period resumes.
func TestAFailedPeriodicTaskIsRetriedAfterTheBackoff(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10, withParams(engine.Params{RetryBackoff: 10 * time.Second}))
	rs := newRuns()
	e := openWith(t, r, func(e *engine.Engine) {
		fail := func(n int) error {
			if n == 1 {
				return errors.New("the disk is busy")
			}
			return nil
		}
		if err := e.AddPeriodic("job", time.Minute, rs.task("job", fail)); err != nil {
			t.Fatal(err)
		}
	})
	advance(t, r, e, time.Minute)
	if n := rs.count("job"); n != 1 {
		t.Fatalf("%d runs, want the failing one", n)
	}
	advance(t, r, e, 9*time.Second)
	if n := rs.count("job"); n != 1 {
		t.Errorf("%d runs after 9 seconds, want the retry not yet due", n)
	}
	advance(t, r, e, time.Second)
	if n := rs.count("job"); n != 2 {
		t.Errorf("%d runs after the back off, want the retry", n)
	}
	advance(t, r, e, 59*time.Second)
	if n := rs.count("job"); n != 2 {
		t.Errorf("%d runs a period less a second after the retry, want 2", n)
	}
	advance(t, r, e, time.Second)
	if n := rs.count("job"); n != 3 {
		t.Errorf("%d runs a period after the retry, want 3", n)
	}
	if e.Phase() != engine.PhaseRunning {
		t.Errorf("phase %v, want the engine to go on after a task failed", e.Phase())
	}
}

func TestAFatalErrorOfAPeriodicTaskStopsTheEngine(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	rs := newRuns()
	fatal := &txn.Error{Tx: "x", Class: fsx.Fatal, Err: errors.New("directory sync failed")}
	e := openWith(t, r, func(e *engine.Engine) {
		if err := e.AddPeriodic("job", time.Minute, rs.task("job", func(int) error { return fatal })); err != nil {
			t.Fatal(err)
		}
	})
	r.clk.Advance(time.Minute)
	select {
	case <-e.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the engine did not stop")
	}
	if !errors.Is(e.Err(), fatal) || e.Phase() != engine.PhaseFailed {
		t.Errorf("Err = %v, phase %v; want the task's fatal error and the failed phase", e.Err(), e.Phase())
	}
}

func TestAddPeriodicRefusesWhatCannotBeScheduled(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e, _, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	ok := func(engine.TaskInput) error { return nil }
	if err := e.AddPeriodic("job", time.Minute, ok); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"a name used twice": e.AddPeriodic("job", time.Minute, ok),
		"no period":         e.AddPeriodic("other", 0, ok),
		"a negative period": e.AddPeriodic("other", -time.Second, ok),
		"no name":           e.AddPeriodic("", time.Minute, ok),
		"no task":           e.AddPeriodic("another", time.Minute, nil),
	} {
		if err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	e.Start()
	t.Cleanup(func() { r.stop2(e) })
	if err := e.AddPeriodic("late", time.Minute, ok); err == nil {
		t.Error("a task was added to a running engine: the loop owns the schedule from Start on")
	}
}
