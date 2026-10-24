package clocktest_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
)

var start = time.Date(2026, time.October, 24, 12, 0, 0, 0, time.UTC)

var (
	_ clock.Clock = (*clocktest.Fake)(nil)
	// The fake adapters receive the shared clock only as a plain function.
	_ func() time.Time = (*clocktest.Fake)(nil).Now
)

// fired reports whether a value is waiting on the timer's channel, without
// consuming it.
func fired(t clock.Timer) bool {
	select {
	case v := <-t.C():
		_ = v
		return true
	default:
		return false
	}
}

// value consumes the value waiting on the timer's channel and fails when there
// is none: the fake delivers synchronously, so a due timer is ready on return.
func value(t *testing.T, timer clock.Timer) time.Time {
	t.Helper()
	select {
	case v := <-timer.C():
		return v
	default:
		t.Fatal("no value waiting on the timer's channel")
		return time.Time{}
	}
}

func TestFakeNowStartsAtStartAndIsUTCWithoutAMonotonicReading(t *testing.T) {
	zone := time.FixedZone("UTC+9", 9*60*60)
	f := clocktest.NewFake(start.In(zone))
	now := f.Now()
	if !now.Equal(start) {
		t.Fatalf("Now() = %v, want %v", now, start)
	}
	if now.Location() != time.UTC {
		t.Fatalf("Now() location = %v, want UTC", now.Location())
	}

	f = clocktest.NewFake(time.Now())
	if strings.Contains(f.Now().String(), "m=") {
		t.Fatalf("Now() = %q carries a monotonic reading", f.Now().String())
	}
	f.Advance(time.Second)
	f.Set(f.Now().Add(time.Second))
	if strings.Contains(f.Now().String(), "m=") || f.Now().Location() != time.UTC {
		t.Fatalf("Now() = %q after Advance and Set, want UTC without a monotonic reading", f.Now().String())
	}
}

func TestNewFakeRejectsTheZeroTime(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewFake(zero time) did not panic")
		}
	}()
	clocktest.NewFake(time.Time{})
}

func TestFakeAdvanceMovesNowByExactlyD(t *testing.T) {
	f := clocktest.NewFake(start)
	f.Advance(90 * time.Second)
	if want := start.Add(90 * time.Second); !f.Now().Equal(want) {
		t.Fatalf("Now() = %v, want %v", f.Now(), want)
	}
	f.Advance(0)
	if want := start.Add(90 * time.Second); !f.Now().Equal(want) {
		t.Fatalf("Now() = %v after Advance(0), want %v", f.Now(), want)
	}
}

func TestFakeAdvanceRejectsANegativeDuration(t *testing.T) {
	f := clocktest.NewFake(start)
	defer func() {
		if recover() == nil {
			t.Fatal("Advance(-1) did not panic")
		}
		if !f.Now().Equal(start) {
			t.Fatalf("Now() = %v after a rejected Advance, want %v", f.Now(), start)
		}
	}()
	f.Advance(-1)
}

func TestFakeNowMethodValueFollowsTheClock(t *testing.T) {
	f := clocktest.NewFake(start)
	now := f.Now
	f.Advance(time.Minute)
	if want := start.Add(time.Minute); !now().Equal(want) {
		t.Fatalf("method value after Advance = %v, want %v", now(), want)
	}
	f.Set(start.Add(-time.Hour))
	if want := start.Add(-time.Hour); !now().Equal(want) {
		t.Fatalf("method value after Set = %v, want %v", now(), want)
	}
}

func TestFakeAdvanceFiresOnlyTimersDueAtOrBeforeTheNewTime(t *testing.T) {
	f := clocktest.NewFake(start)
	early := f.NewTimer(10 * time.Second)
	exact := f.NewTimer(20 * time.Second)
	late := f.NewTimer(30 * time.Second)

	f.Advance(20 * time.Second)

	if got := value(t, early); !got.Equal(start.Add(10 * time.Second)) {
		t.Fatalf("early timer value = %v, want its deadline %v", got, start.Add(10*time.Second))
	}
	if got := value(t, exact); !got.Equal(start.Add(20 * time.Second)) {
		t.Fatalf("timer due exactly at the new time: value = %v, want its deadline %v", got, start.Add(20*time.Second))
	}
	if fired(late) {
		t.Fatal("timer due after the new time fired")
	}
	if got := f.Timers(); got != 1 {
		t.Fatalf("Timers() = %d, want 1", got)
	}

	f.Advance(10 * time.Second)
	if got := value(t, late); !got.Equal(start.Add(30 * time.Second)) {
		t.Fatalf("late timer value = %v, want its deadline %v", got, start.Add(30*time.Second))
	}
}

func TestFakeTimerFiresOnceUntilItIsReset(t *testing.T) {
	f := clocktest.NewFake(start)
	timer := f.NewTimer(time.Second)
	f.Advance(time.Hour)
	value(t, timer)
	f.Advance(time.Hour)
	if fired(timer) {
		t.Fatal("timer fired a second time without Reset")
	}
}

func TestFakeAdvanceFiresEveryDueTimerInOneCall(t *testing.T) {
	f := clocktest.NewFake(start)
	var timers []clock.Timer
	for _, d := range []time.Duration{3, 1, 2, 5, 4} {
		timers = append(timers, f.NewTimer(d*time.Minute))
	}
	f.Advance(time.Hour)
	for i, timer := range timers {
		if !fired(timer) {
			t.Fatalf("timer %d did not fire", i)
		}
	}
	if got := f.Timers(); got != 0 {
		t.Fatalf("Timers() = %d, want 0", got)
	}
}

func TestFakeSetForwardFiresDueTimersLikeAdvance(t *testing.T) {
	f := clocktest.NewFake(start)
	due := f.NewTimer(time.Minute)
	notDue := f.NewTimer(time.Hour)

	f.Set(start.Add(10 * time.Minute))

	if want := start.Add(10 * time.Minute); !f.Now().Equal(want) {
		t.Fatalf("Now() = %v, want %v", f.Now(), want)
	}
	if got := value(t, due); !got.Equal(start.Add(time.Minute)) {
		t.Fatalf("value = %v, want the deadline %v", got, start.Add(time.Minute))
	}
	if fired(notDue) {
		t.Fatal("timer not yet due fired on a forward Set")
	}
	if got := f.Timers(); got != 1 {
		t.Fatalf("Timers() = %d, want 1", got)
	}
}

// A timer measures elapsed time, so a wall clock set back does not move it: it
// keeps the duration that was left, as a System() timer does.
func TestFakeSetBackwardFiresNoTimerAndKeepsEachTimersRemainingDuration(t *testing.T) {
	f := clocktest.NewFake(start)
	timer := f.NewTimer(time.Minute)
	f.Advance(30 * time.Second)

	f.Set(start.Add(-24 * time.Hour))

	if want := start.Add(-24 * time.Hour); !f.Now().Equal(want) {
		t.Fatalf("Now() = %v, want %v", f.Now(), want)
	}
	if fired(timer) {
		t.Fatal("timer fired on a backward Set")
	}
	if got := f.Timers(); got != 1 {
		t.Fatalf("Timers() = %d after a backward Set, want 1", got)
	}

	f.Advance(29 * time.Second)
	if fired(timer) {
		t.Fatal("timer fired before its remaining 30s had elapsed")
	}
	f.Advance(time.Second)
	want := start.Add(-24*time.Hour + 30*time.Second)
	if got := value(t, timer); !got.Equal(want) {
		t.Fatalf("value = %v, want the shifted deadline %v", got, want)
	}
}

func TestFakeTimerCreatedAfterABackwardSetCountsFromTheNewNow(t *testing.T) {
	f := clocktest.NewFake(start)
	f.Set(start.Add(-time.Hour))
	timer := f.NewTimer(time.Minute)

	f.Advance(59 * time.Second)
	if fired(timer) {
		t.Fatal("timer fired before its deadline")
	}
	f.Advance(time.Second)
	if got := value(t, timer); !got.Equal(start.Add(-time.Hour + time.Minute)) {
		t.Fatalf("value = %v, want %v", got, start.Add(-time.Hour+time.Minute))
	}
}

func TestFakeSetToTheCurrentTimeFiresNothingNew(t *testing.T) {
	f := clocktest.NewFake(start)
	timer := f.NewTimer(time.Minute)
	f.Set(start)
	if fired(timer) || f.Timers() != 1 {
		t.Fatalf("Set(now) fired a timer or changed Timers() = %d", f.Timers())
	}
}

func TestFakeNewTimerWithNonPositiveDurationFiresAtOnce(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		f := clocktest.NewFake(start)
		timer := f.NewTimer(d)
		if got := f.Timers(); got != 0 {
			t.Fatalf("NewTimer(%v): Timers() = %d, want 0", d, got)
		}
		if got := value(t, timer); !got.Equal(start) {
			t.Fatalf("NewTimer(%v) value = %v, want the current time %v", d, got, start)
		}
	}
}

func TestFakeTimersCountsArmedTimersOnly(t *testing.T) {
	f := clocktest.NewFake(start)
	if got := f.Timers(); got != 0 {
		t.Fatalf("Timers() = %d on a new clock, want 0", got)
	}
	a := f.NewTimer(time.Minute)
	b := f.NewTimer(2 * time.Minute)
	c := f.NewTimer(3 * time.Minute)
	if got := f.Timers(); got != 3 {
		t.Fatalf("Timers() = %d, want 3", got)
	}
	b.Stop()
	if got := f.Timers(); got != 2 {
		t.Fatalf("Timers() = %d after Stop, want 2", got)
	}
	f.Advance(time.Minute)
	if got := f.Timers(); got != 1 {
		t.Fatalf("Timers() = %d after one fired, want 1", got)
	}
	// A fired timer that was Reset is armed again.
	value(t, a)
	a.Reset(time.Hour)
	if got := f.Timers(); got != 2 {
		t.Fatalf("Timers() = %d after Reset, want 2", got)
	}
	c.Stop()
	a.Stop()
	if got := f.Timers(); got != 0 {
		t.Fatalf("Timers() = %d after stopping all, want 0", got)
	}
}

// The timer semantics follow the standard library since Go 1.23: after Stop or
// Reset returns, a value from the earlier setting is never received, and a
// value that was due but not yet received still counts as pending.

func TestFakeStopOnAnArmedTimerReturnsTrueAndPreventsFiring(t *testing.T) {
	f := clocktest.NewFake(start)
	timer := f.NewTimer(time.Minute)
	if !timer.Stop() {
		t.Fatal("Stop() = false on an armed timer, want true")
	}
	f.Advance(time.Hour)
	if fired(timer) {
		t.Fatal("timer fired after Stop")
	}
	if timer.Stop() {
		t.Fatal("second Stop() = true, want false")
	}
}

func TestFakeStopAfterTheValueWasReceivedReturnsFalse(t *testing.T) {
	f := clocktest.NewFake(start)
	timer := f.NewTimer(time.Minute)
	f.Advance(time.Minute)
	value(t, timer)
	if timer.Stop() {
		t.Fatal("Stop() = true after the value was received, want false")
	}
}

func TestFakeStopBeforeTheFiredValueIsReceivedReturnsTrueAndDiscardsIt(t *testing.T) {
	f := clocktest.NewFake(start)
	timer := f.NewTimer(time.Minute)
	f.Advance(time.Minute) // due, not received
	if !timer.Stop() {
		t.Fatal("Stop() = false although the value was not received, want true")
	}
	if fired(timer) {
		t.Fatal("a stale value was received after Stop")
	}
	if timer.Stop() {
		t.Fatal("second Stop() = true, want false")
	}
}

func TestFakeResetOnAnArmedTimerReturnsTrueAndMovesTheDeadline(t *testing.T) {
	f := clocktest.NewFake(start)
	timer := f.NewTimer(time.Minute)
	if !timer.Reset(time.Hour) {
		t.Fatal("Reset() = false on an armed timer, want true")
	}
	if got := f.Timers(); got != 1 {
		t.Fatalf("Timers() = %d, want 1", got)
	}
	f.Advance(time.Minute)
	if fired(timer) {
		t.Fatal("timer fired at its old deadline after Reset")
	}
	f.Advance(59 * time.Minute)
	if got := value(t, timer); !got.Equal(start.Add(time.Hour)) {
		t.Fatalf("value = %v, want the new deadline %v", got, start.Add(time.Hour))
	}
}

func TestFakeResetCountsFromTheCurrentTime(t *testing.T) {
	f := clocktest.NewFake(start)
	timer := f.NewTimer(time.Minute)
	f.Advance(30 * time.Second)
	timer.Reset(time.Minute)
	f.Advance(59 * time.Second)
	if fired(timer) {
		t.Fatal("timer fired before the new deadline")
	}
	f.Advance(time.Second)
	if got := value(t, timer); !got.Equal(start.Add(90 * time.Second)) {
		t.Fatalf("value = %v, want %v", got, start.Add(90*time.Second))
	}
}

func TestFakeResetOnAStoppedTimerReturnsFalseAndRearms(t *testing.T) {
	f := clocktest.NewFake(start)
	timer := f.NewTimer(time.Minute)
	timer.Stop()
	if timer.Reset(time.Minute) {
		t.Fatal("Reset() = true on a stopped timer, want false")
	}
	f.Advance(time.Minute)
	value(t, timer)
}

func TestFakeResetAfterTheValueWasReceivedReturnsFalseAndRearms(t *testing.T) {
	f := clocktest.NewFake(start)
	timer := f.NewTimer(time.Minute)
	f.Advance(time.Minute)
	value(t, timer)
	if timer.Reset(time.Minute) {
		t.Fatal("Reset() = true after the value was received, want false")
	}
	f.Advance(time.Minute)
	value(t, timer)
}

func TestFakeResetBeforeTheFiredValueIsReceivedReturnsTrueAndDiscardsIt(t *testing.T) {
	f := clocktest.NewFake(start)
	timer := f.NewTimer(time.Minute)
	f.Advance(time.Minute) // due, not received
	if !timer.Reset(time.Hour) {
		t.Fatal("Reset() = false although the value was not received, want true")
	}
	if fired(timer) {
		t.Fatal("a stale value was received after Reset")
	}
	if got := f.Timers(); got != 1 {
		t.Fatalf("Timers() = %d, want 1", got)
	}
	f.Advance(time.Hour)
	if got := value(t, timer); !got.Equal(start.Add(time.Minute + time.Hour)) {
		t.Fatalf("value = %v, want %v", got, start.Add(time.Minute+time.Hour))
	}
}

func TestFakeResetWithNonPositiveDurationFiresAtOnce(t *testing.T) {
	f := clocktest.NewFake(start)
	timer := f.NewTimer(time.Hour)
	if !timer.Reset(0) {
		t.Fatal("Reset(0) = false on an armed timer, want true")
	}
	if got := f.Timers(); got != 0 {
		t.Fatalf("Timers() = %d, want 0", got)
	}
	if got := value(t, timer); !got.Equal(start) {
		t.Fatalf("value = %v, want the current time %v", got, start)
	}
}

// TestFakeIsSafeForConcurrentUse runs under -race: engines, waiters and the
// test driving the clock touch the same fake from different goroutines.
func TestFakeIsSafeForConcurrentUse(t *testing.T) {
	f := clocktest.NewFake(start)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				timer := f.NewTimer(time.Duration(i%7) * time.Second)
				_ = f.Now()
				_ = f.Timers()
				timer.Reset(time.Duration(i%5) * time.Second)
				select {
				case <-timer.C():
				default:
				}
				timer.Stop()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 200 {
			f.Advance(time.Second)
			if i%50 == 0 {
				f.Set(f.Now().Add(-time.Minute))
			}
		}
	}()
	wg.Wait()
}

func TestFakeAdvanceWakesAWaiterBlockedOnItsTimer(t *testing.T) {
	f := clocktest.NewFake(start)
	timer := f.NewTimer(time.Minute)
	got := make(chan time.Time, 1)
	go func() { got <- <-timer.C() }()

	f.Advance(time.Minute)

	select {
	case v := <-got:
		if !v.Equal(start.Add(time.Minute)) {
			t.Fatalf("waiter received %v, want %v", v, start.Add(time.Minute))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked waiter was not woken by Advance")
	}
}
