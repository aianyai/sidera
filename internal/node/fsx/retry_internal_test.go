package fsx

import (
	"errors"
	"testing"
	"time"
)

func TestRetry(t *testing.T) {
	transient := errors.New("transient")
	permanent := errors.New("permanent")

	t.Run("success-needs-no-sleep", func(t *testing.T) {
		var slept []time.Duration
		err := retry(func(d time.Duration) { slept = append(slept, d) }, func() (bool, error) { return false, nil })
		if err != nil || len(slept) != 0 {
			t.Errorf("err = %v, slept = %v; want nil and no sleeping", err, slept)
		}
	})
	t.Run("an-error-that-cannot-be-retried-returns-at-once", func(t *testing.T) {
		calls := 0
		var slept []time.Duration
		err := retry(func(d time.Duration) { slept = append(slept, d) }, func() (bool, error) { calls++; return false, permanent })
		if !errors.Is(err, permanent) || calls != 1 || len(slept) != 0 {
			t.Errorf("err = %v, calls = %d, slept = %v; want permanent, 1, none", err, calls, slept)
		}
	})
	t.Run("a-transient-error-is-retried-with-doubling-sleeps", func(t *testing.T) {
		calls := 0
		var slept []time.Duration
		err := retry(func(d time.Duration) { slept = append(slept, d) }, func() (bool, error) {
			calls++
			if calls <= 4 {
				return true, transient
			}
			return false, nil
		})
		want := []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 8 * time.Millisecond}
		if err != nil || calls != 5 || !equalDurations(slept, want) {
			t.Errorf("err = %v, calls = %d, slept = %v; want nil, 5, %v", err, calls, slept, want)
		}
	})
	t.Run("the-budget-is-two-seconds-of-sleeping-and-the-last-error-is-returned", func(t *testing.T) {
		calls := 0
		var slept []time.Duration
		err := retry(func(d time.Duration) { slept = append(slept, d) }, func() (bool, error) {
			calls++
			return true, transient
		})
		var total time.Duration
		for _, d := range slept {
			total += d
		}
		if !errors.Is(err, transient) {
			t.Errorf("err = %v, want the last transient error", err)
		}
		if total != 2*time.Second {
			t.Errorf("slept %v in total, want exactly 2s", total)
		}
		if calls != len(slept)+1 {
			t.Errorf("calls = %d for %d sleeps, want one more call than sleeps", calls, len(slept))
		}
		for i := 1; i < len(slept)-1; i++ {
			if slept[i] != 2*slept[i-1] {
				t.Errorf("sleep %d = %v after %v, want doubling until the last, shortened, one", i, slept[i], slept[i-1])
			}
		}
		if slept[0] != time.Millisecond {
			t.Errorf("first sleep = %v, want 1ms", slept[0])
		}
	})
	t.Run("the-last-error-seen-wins", func(t *testing.T) {
		calls := 0
		err := retry(func(time.Duration) {}, func() (bool, error) {
			calls++
			if calls < 3 {
				return true, transient
			}
			return false, permanent
		})
		if !errors.Is(err, permanent) {
			t.Errorf("err = %v, want permanent", err)
		}
	})
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sleepSystem really waits, and no less than asked.
func TestSleepSystemWaitsAtLeastTheDuration(t *testing.T) {
	start := time.Now()
	sleepSystem(20 * time.Millisecond)
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Errorf("slept %v, want at least 20ms", elapsed)
	}
}
