package timing

import (
	"testing"
	"time"
)

func TestClockTolerance(t *testing.T) {
	if ClockTolerance != 2*time.Minute {
		t.Fatalf("ClockTolerance = %v, want 2m", ClockTolerance)
	}
}
