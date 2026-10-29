package inproc

import (
	"context"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func TestRecipeDrainPreservesCompletedPrefixObligations(t *testing.T) {
	for _, tc := range []struct{ name, after string }{
		{"delivery-register-declare", "register"},
		{"session-blocked-recovered", "send"},
		{"session-lifecycle", "send-for-prompt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, tc.name), crashwalk.RunPlan{Targets: []crashwalk.Target{{Gen: 1, AfterStep: tc.after}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Reached) != 1 || !r.Reached[0] || len(r.Hits) != 2 || len(r.Violations) != 0 {
				t.Fatalf("after %s: reached=%v generations=%d violations=%v", tc.after, r.Reached, len(r.Hits), r.Violations)
			}
		})
	}
}
