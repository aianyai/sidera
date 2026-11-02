package engine_test

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

// A pull whose answer tells the sender what its record already holds writes nothing:
// the sender asks once a minute for as long as a delivery is unsettled, and a rewrite
// of the same record each time is a write for no change.
func TestAPullThatBringsNoNewsWritesNothing(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	send(t, h, "a", "b", "never received")
	quiet(t, h)
	// The first pull teaches the sender the state of the delivery; the next ones
	// repeat it.
	h.Advance(time.Minute)
	quiet(t, h)

	rule := h.Faults().Fail(fsxtest.Match{Tx: "source-update", Step: fsx.StepWrite}, fsxtest.DiskFull)
	// Every call from here on waits out a delay, so that the pulls can be counted: a
	// write that never happens proves nothing when no pull was made.
	const delay = time.Second
	h.Delay(delay)
	const pulls = 3
	for range pulls {
		h.Advance(time.Minute)
		letTheCallThrough(t, h, delay)
	}
	if n := callsOf(t, h, "pull"); n < pulls {
		t.Fatalf("the target answered %d pulls, want at least %d", n, pulls)
	}
	if n := rule.Hits(); n != 0 {
		t.Errorf("the sender's record was written %d times by pulls that brought nothing new", n)
	}
	h.Faults().Abandon()
}
