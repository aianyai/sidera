package clock_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/clock"
)

// receiveWithin returns the value delivered on c, or fails the test when none
// arrives in a generous real-time bound.
func receiveWithin(t *testing.T, c <-chan time.Time) time.Time {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("no value delivered within 5s")
		return time.Time{}
	}
}

func TestSystemNowIsUTCWithoutAMonotonicReading(t *testing.T) {
	// Sanity check on the detector: a plain time.Now carries a reading, and the
	// String form of a time shows it as "m=…". Without this the assertion below
	// could never fail.
	if !strings.Contains(time.Now().String(), "m=") {
		t.Fatal("test detector is broken: time.Now().String() shows no monotonic reading")
	}

	now := clock.System().Now()
	if now.Location() != time.UTC {
		t.Fatalf("System().Now() location = %v, want UTC", now.Location())
	}
	if strings.Contains(now.String(), "m=") {
		t.Fatalf("System().Now() = %q carries a monotonic reading", now.String())
	}
}

func TestSystemNowIsCloseToTheWallClock(t *testing.T) {
	before := time.Now()
	got := clock.System().Now()
	after := time.Now()
	if got.Before(before.Round(0)) || got.After(after.Round(0)) {
		t.Fatalf("System().Now() = %v, want within [%v, %v]", got, before, after)
	}
}

func TestSystemTimerFiresOnceAfterItsDuration(t *testing.T) {
	timer := clock.System().NewTimer(time.Millisecond)
	receiveWithin(t, timer.C())
	if timer.Stop() {
		t.Fatal("Stop() = true after the value was received, want false")
	}
}

func TestSystemTimerStopPreventsFiring(t *testing.T) {
	timer := clock.System().NewTimer(time.Hour)
	if !timer.Stop() {
		t.Fatal("Stop() = false on an armed timer, want true")
	}
	if timer.Stop() {
		t.Fatal("second Stop() = true, want false")
	}
	select {
	case v := <-timer.C():
		t.Fatalf("received %v after Stop", v)
	default:
	}
}

func TestSystemTimerStopAfterUnreceivedExpiryReturnsTrue(t *testing.T) {
	timer := clock.System().NewTimer(time.Millisecond)
	// Let the setting expire without receiving the value: it still counts as
	// pending, and Stop discards it.
	time.Sleep(50 * time.Millisecond)
	if !timer.Stop() {
		t.Fatal("Stop() = false on a timer whose value was not received, want true")
	}
	select {
	case v := <-timer.C():
		t.Fatalf("received %v after Stop", v)
	default:
	}
}

func TestSystemTimerResetRearmsAndLeavesNoStaleValue(t *testing.T) {
	timer := clock.System().NewTimer(time.Millisecond)
	// Let the first setting expire without receiving it.
	time.Sleep(50 * time.Millisecond)
	if !timer.Reset(time.Hour) {
		t.Fatal("Reset() = false on a timer whose value was not received, want true")
	}
	select {
	case v := <-timer.C():
		t.Fatalf("received stale value %v after Reset", v)
	default:
	}
	if !timer.Stop() {
		t.Fatal("Stop() = false after Reset armed the timer, want true")
	}
	if timer.Reset(time.Millisecond) {
		t.Fatal("Reset() = true on a stopped timer, want false")
	}
	receiveWithin(t, timer.C())
}
