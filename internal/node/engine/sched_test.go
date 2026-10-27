package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/clock"
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

// clockDisk stands for the file the clock state is persisted in: the task that
// the composition root hands the engine writes what it is told into it, and a
// restart reads it back.
type clockDisk struct {
	mu     sync.Mutex
	hw     time.Time
	paused bool
	since  time.Time
	writes int
}

func (c *clockDisk) task(in engine.TaskInput) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hw, c.paused, c.since = in.HighWater, in.RollbackPaused, in.PausedSince
	c.writes++
	return nil
}

// pauseStart is the start of the pause the last write was told of.
func (c *clockDisk) pauseStart() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.since
}

func (c *clockDisk) state() (hw time.Time, paused bool, writes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hw, c.paused, c.writes
}

// Entering the pause of a wall clock set back is made durable in the command that
// finds it, not at the next period: a crash right after it must not give back the
// trust in a clock that was set back.
func TestThePauseOfAWallClockSetBackIsPersistedAtOnceAndSurvivesACrash(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	var disk clockDisk
	e := openWith(t, r, func(e *engine.Engine) {
		if err := e.AddClockState("clock-state", time.Minute, disk.task); err != nil {
			t.Fatal(err)
		}
	})
	ctx := context.Background()
	r.clk.Advance(time.Hour)
	settle(t, e) // the period passes and the mark moves to the hour
	if err := engine.AdmitNewWork(ctx, e, 1); err != nil {
		t.Fatal(err)
	}
	if _, paused, _ := disk.state(); paused {
		t.Fatal("the pause was persisted before the clock was set back")
	}
	_, _, before := disk.state()

	r.clk.Set(t0.Add(30 * time.Minute)) // half an hour behind the mark, and no period has passed
	wantReason(t, engine.AdmitNewWork(ctx, e, 1), engine.ReasonClockRollback)
	hw, paused, writes := disk.state()
	if !paused || writes != before+1 {
		t.Fatalf("after the command that found the clock set back: paused %v, %d writes (%d before); want the pause persisted by that command", paused, writes, before)
	}
	if want := t0.Add(time.Hour); !hw.Equal(want) {
		t.Errorf("the persisted high water mark is %v, want %v", hw, want)
	}
	wantReason(t, engine.AdmitNewWork(ctx, e, 1), engine.ReasonClockRollback)
	if _, _, again := disk.state(); again != writes {
		t.Errorf("a second command in the pause wrote the clock state again (%d writes, %d before)", again, writes)
	}

	// The process dies here. The next one starts from what was persisted.
	r.restart()
	r.paused, r.hw = paused, clock.NewHighWater(hw)
	e = r.start()
	wantReason(t, engine.AdmitNewWork(ctx, e, 1), engine.ReasonClockRollback)
}

// The task of the clock state is told when the pause began, so that the one place
// that knows (the loop, which also reports it as the alert's "since") is the only
// place: the time of the command that found the clock set back, kept while the
// pause lasts, the persisted start when the node started paused, and forgotten when
// the owner clears the pause so that a later pause begins at its own time.
func TestTheTaskOfTheClockStateIsToldWhenThePauseBegan(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	var disk clockDisk
	e := openWith(t, r, func(e *engine.Engine) {
		if err := e.AddClockState("clock-state", time.Minute, disk.task); err != nil {
			t.Fatal(err)
		}
	})
	ctx := context.Background()
	since := disk.pauseStart

	advance(t, r, e, time.Hour) // the mark is at the hour
	if !since().IsZero() {
		t.Errorf("a clock that was never set back has a pause since %v", since())
	}
	first := t0.Add(30 * time.Minute)
	r.clk.Set(first)
	wantReason(t, engine.AdmitNewWork(ctx, e, 1), engine.ReasonClockRollback)
	if got := since(); !got.Equal(first) {
		t.Errorf("the pause began at %v, the task was told %v", first, got)
	}
	advance(t, r, e, 5*time.Minute) // the period passes: the same start, not the time of this run
	if got := since(); !got.Equal(first) {
		t.Errorf("a pause that lasts: the task was told %v, want the start %v", got, first)
	}
	if _, err := e.Execute(ctx, clearCommand(81, "clear")); err != nil {
		t.Fatal(err)
	}
	settle(t, e)
	if got := since(); !got.IsZero() {
		t.Errorf("after the owner cleared the pause the task was told %v, want none", got)
	}
	second := r.clk.Now().Add(-30 * time.Minute)
	r.clk.Set(second)
	wantReason(t, engine.AdmitNewWork(ctx, e, 1), engine.ReasonClockRollback)
	if got := since(); !got.Equal(second) {
		t.Errorf("a later pause began at %v, the task was told %v", second, got)
	}
}

// A node that starts paused is told the start that was persisted.
func TestTheTaskOfTheClockStateIsToldThePersistedStartOfAPauseTheNodeStartedIn(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	r.paused, r.pausedSince = true, t0.Add(-time.Hour)
	var disk clockDisk
	e := openWith(t, r, func(e *engine.Engine) {
		if err := e.AddClockState("clock-state", time.Minute, disk.task); err != nil {
			t.Fatal(err)
		}
	})
	advance(t, r, e, time.Minute)
	if _, paused, _ := disk.state(); !paused {
		t.Fatal("the task was not told of the pause")
	}
	if got, want := disk.pauseStart(), t0.Add(-time.Hour); !got.Equal(want) {
		t.Errorf("the task was told the pause began at %v, want the persisted %v", got, want)
	}
}

// What the owner accepted when clearing the pause is the clock as it reads, so
// the tasks that were scheduled from the clock that was ahead are due from that
// clock: a task due one period after a reading that was days in the future would
// otherwise not run for those days, among them the check of the certificate that
// the clear is meant to let renew.
func TestClearingThePauseSchedulesThePeriodicTasksFromTheClockTheOwnerAccepted(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	rs := newRuns()
	e := openWith(t, r, func(e *engine.Engine) {
		if err := e.AddPeriodic("hourly", time.Hour, rs.task("hourly", nil)); err != nil {
			t.Fatal(err)
		}
		if err := e.AddPeriodic("daily", 24*time.Hour, rs.task("daily", nil)); err != nil {
			t.Fatal(err)
		}
	})
	ctx := context.Background()
	advance(t, r, e, 3*24*time.Hour) // the clock jumps ahead; both tasks run once, on the fast clock
	if rs.count("hourly") != 1 || rs.count("daily") != 1 {
		t.Fatalf("runs after the jump: hourly %d, daily %d; want 1 and 1", rs.count("hourly"), rs.count("daily"))
	}
	r.clk.Set(t0.Add(time.Hour)) // the clock is put right again
	wantReason(t, engine.AdmitNewWork(ctx, e, 1), engine.ReasonClockRollback)
	settle(t, e)
	if rs.count("hourly") != 1 {
		t.Fatalf("a task ran while the clock was in doubt: hourly %d", rs.count("hourly"))
	}

	if _, err := e.Execute(ctx, clearCommand(82, "clear")); err != nil {
		t.Fatal(err)
	}
	settle(t, e)
	cleared := t0.Add(time.Hour)
	for _, name := range []string{"hourly", "daily"} {
		if n := rs.count(name); n != 2 {
			t.Errorf("%s ran %d times after the clear, want it run at once", name, n)
			continue
		}
		if got := rs.at[name][1]; !got.Equal(cleared) {
			t.Errorf("%s ran at %v after the clear, want the clock the owner accepted, %v", name, got, cleared)
		}
	}
	advance(t, r, e, time.Hour)
	if rs.count("hourly") != 3 || rs.count("daily") != 2 {
		t.Errorf("one period after the clear: hourly %d, daily %d; want 3 and 2", rs.count("hourly"), rs.count("daily"))
	}
}

// A pause that cannot be made durable for good stops the engine, like any other
// write that failed after its commit point, and nothing runs after it.
func TestAFatalErrorWhilePersistingThePauseStopsTheEngine(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	fatal := &txn.Error{Tx: "clock-high-water", Class: fsx.Fatal, Err: errors.New("directory sync failed")}
	e := openWith(t, r, func(e *engine.Engine) {
		task := func(in engine.TaskInput) error {
			if in.RollbackPaused {
				return fatal
			}
			return nil
		}
		if err := e.AddClockState("clock-state", time.Minute, task); err != nil {
			t.Fatal(err)
		}
	})
	ctx := context.Background()
	r.clk.Advance(time.Hour)
	settle(t, e)
	r.clk.Set(t0.Add(30 * time.Minute))
	wantReason(t, engine.AdmitNewWork(ctx, e, 1), engine.ReasonStorageFatal)
	select {
	case <-e.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the engine did not stop")
	}
	if !errors.Is(e.Err(), fatal) || e.Phase() != engine.PhaseFailed {
		t.Errorf("Err = %v, phase %v; want the fatal error and the failed phase", e.Err(), e.Phase())
	}
}

func TestTheClockStateHasOneTask(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e, _, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	ok := func(engine.TaskInput) error { return nil }
	if err := e.AddClockState("clock-state", time.Minute, ok); err != nil {
		t.Fatal(err)
	}
	if err := e.AddClockState("another", time.Minute, ok); err == nil {
		t.Error("a second task of the clock state was accepted")
	}
	e.Start()
	t.Cleanup(func() { r.stop2(e) })
}

// A periodic task is told whether the node may sign, by the one rule the engine
// signs by: a task that signs reads only that, so it cannot sign after the
// retirement of the identity (before the daemon is down) or while the clock is in
// doubt. The retirement is judged before the clock, as everything that signs does.
func TestAPeriodicTaskIsToldWhetherTheNodeMaySign(t *testing.T) {
	var mu sync.Mutex
	var last engine.TaskInput
	task := func(in engine.TaskInput) error {
		mu.Lock()
		defer mu.Unlock()
		last = in
		return nil
	}
	refusal := func() engine.Reason {
		mu.Lock()
		defer mu.Unlock()
		return last.SignRefusal
	}
	setup := func(t *testing.T) (*rig, *engine.Engine) {
		noLeaks(t)
		r := newRig(t, 10, withKeys())
		return r, openWith(t, r, func(e *engine.Engine) {
			if err := e.AddPeriodic("signs", time.Minute, task); err != nil {
				t.Fatal(err)
			}
		})
	}
	retire := func(t *testing.T, r *rig, e *engine.Engine) {
		t.Helper()
		if res, err := e.Execute(context.Background(), retireCommand(110, "retire", r.node)); err != nil || !res.Stop {
			t.Fatalf("the retirement: %+v, %v", res, err)
		}
	}
	t.Run("a node that holds its keys on a clock it trusts may sign", func(t *testing.T) {
		r, e := setup(t)
		advance(t, r, e, time.Minute)
		if got := refusal(); got != 0 {
			t.Errorf("SignRefusal = %v, want none", got)
		}
	})
	t.Run("a retired identity may not", func(t *testing.T) {
		r, e := setup(t)
		retire(t, r, e)
		advance(t, r, e, time.Minute)
		if got := refusal(); got != engine.ReasonIdentityRetired {
			t.Errorf("SignRefusal after the retirement = %v, want %v", got, engine.ReasonIdentityRetired)
		}
	})
	t.Run("a clock that was set back may not", func(t *testing.T) {
		r, e := setup(t)
		advance(t, r, e, time.Hour)
		r.clk.Set(t0.Add(30 * time.Minute))
		settle(t, e)
		advance(t, r, e, 40*time.Minute) // past the mark again, and the period: the pause stays
		if got := refusal(); got != engine.ReasonClockRollback {
			t.Errorf("SignRefusal on a clock set back = %v, want %v", got, engine.ReasonClockRollback)
		}
	})
	t.Run("the retirement is judged before the clock", func(t *testing.T) {
		r, e := setup(t)
		advance(t, r, e, time.Hour)
		r.clk.Set(t0.Add(30 * time.Minute))
		settle(t, e)
		retire(t, r, e)
		advance(t, r, e, 40*time.Minute)
		if got := refusal(); got != engine.ReasonIdentityRetired {
			t.Errorf("SignRefusal of a retired node on a clock set back = %v, want %v", got, engine.ReasonIdentityRetired)
		}
	})
}
