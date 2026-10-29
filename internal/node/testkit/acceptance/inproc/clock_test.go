package inproc

import (
	"context"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func TestClockClearInterruptedResultKeepsTheDurableClock(t *testing.T) {
	for _, name := range []string{"clock-clear/clock.create#1", "clock-clear/clock.write#1", "clock-clear/clock.rename#1"} {
		t.Run(name, func(t *testing.T) {
			point, err := fsx.ParsePoint(name)
			if err != nil {
				t.Fatal(err)
			}
			result, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, "management-maintenance"), crashwalk.RunPlan{Targets: []crashwalk.Target{{Gen: 1, Point: point}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Hits) != 2 || len(result.Reached) != 1 || !result.Reached[0] {
				t.Fatalf("clock cut reached=%v generations=%d", result.Reached, len(result.Hits))
			}
			if len(result.Violations) != 0 {
				t.Fatal(result.Violations)
			}
		})
	}
}
