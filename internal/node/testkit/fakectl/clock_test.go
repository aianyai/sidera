package fakectl_test

import (
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
)

// TestClockFollowsTheFixture: a fake process has no clock of its own; it reads
// the time the fixture last set, in UTC.
func TestClockFollowsTheFixture(t *testing.T) {
	c := fakectl.NewClock(1792832400000)
	want := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	if got := c.Now(); !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("Now() = %v, want %v in UTC", got, want)
	}
	c.SetMS(1792832460000)
	if got := c.Now(); !got.Equal(want.Add(time.Minute)) {
		t.Fatalf("after SetMS Now() = %v", got)
	}
	c.SetMS(1792832400000) // backwards is allowed: a rollback test needs it
	if got := c.Now(); !got.Equal(want) {
		t.Fatalf("after setting back Now() = %v", got)
	}
}

func TestClockIsSafeForConcurrentUse(t *testing.T) {
	c := fakectl.NewClock(1)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				c.SetMS(int64(j))
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = c.Now()
			}
		}()
	}
	wg.Wait()
}
