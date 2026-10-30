//go:build acceptance

package acceptance

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

func TestBinaryRetentionResumesAfterRecordedCrash(t *testing.T) {
	scenarios, _ := bindCatalog()
	for _, scenario := range scenarios {
		if scenario.Name() != "maintenance-retention" {
			continue
		}
		for _, tc := range []struct {
			name   string
			target crashwalk.Target
		}{
			{"writing-point", crashwalk.Target{Gen: 1, Point: point(t, "ack/body.remove#1")}},
			{"after-step", crashwalk.Target{Gen: 1, AfterStep: "read-initial"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				backend, ctx := binaryBackend(t)
				result, err := backend.Run(ctx, scenario, crashwalk.RunPlan{Targets: []crashwalk.Target{tc.target}})
				checkRun(t, result, err, 2)
			})
		}
		return
	}
	t.Fatal("maintenance-retention recipe is missing")
}
