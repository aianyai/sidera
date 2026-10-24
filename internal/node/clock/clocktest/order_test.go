package clocktest

import (
	"testing"
	"time"
)

// Which timer fires first is not observable through the timers' channels, which
// are separate and read by separate goroutines, so the order is checked on the
// list a firing walks.
func TestTakeDueReturnsTimersInDeadlineOrderThenArmingOrder(t *testing.T) {
	start := time.Date(2026, time.October, 24, 12, 0, 0, 0, time.UTC)
	f := NewFake(start)

	var created []*fakeTimer
	// Deadlines in minutes, created out of order; the two 2-minute timers tie.
	for _, minutes := range []int{5, 2, 9, 1, 2, 7} {
		created = append(created, f.NewTimer(time.Duration(minutes)*time.Minute).(*fakeTimer))
	}
	// Re-arming moves a timer to the back of its tie.
	created[1].Reset(2 * time.Minute)

	f.mu.Lock()
	due := f.takeDue(start.Add(7 * time.Minute))
	f.mu.Unlock()

	want := []*fakeTimer{created[3], created[4], created[1], created[0], created[5]}
	if len(due) != len(want) {
		t.Fatalf("takeDue returned %d timers, want %d", len(due), len(want))
	}
	for i := range want {
		if due[i] != want[i] {
			t.Fatalf("position %d: got the timer due at +%v, want the one due at +%v",
				i, due[i].deadline.Sub(start), want[i].deadline.Sub(start))
		}
	}
}
