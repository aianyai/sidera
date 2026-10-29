package inproc

import (
	"context"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func TestRecipeDrainPreservesCompletedPrefixObligations(t *testing.T) {
	for _, tc := range []struct{ name, after string }{
		{"delivery-register-declare", "register"},
		{"session-blocked-recovered", "send"},
		{"session-lifecycle", "send-for-prompt"},
		{"session-create-expiry", "create"},
		{"session-create-expiry", "control-deadline"},
		{"delivery-unreachable", "send-to-an-absent-node"},
		{"delivery-unreachable", "report-deadline"},
	} {
		t.Run(tc.name+"/"+tc.after, func(t *testing.T) {
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

func TestRecipeDrainPreservesClockProgressAfterAnotherCrash(t *testing.T) {
	for _, tc := range []struct {
		name, after string
		points      []string
	}{
		{"session-create-expiry", "create", []string{"control-reply/out.create#1", "ack/body.remove#1"}},
		{"delivery-unreachable", "send-to-an-absent-node", []string{"tick/out.create#1", "feedback-form/notice.create#1"}},
		{"maintenance-retention", "send-from-session", []string{"archive-summaries/archive.open#1", "archive-sweep/endpoint.remove#1", "archive-expire/file.remove#1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := crashwalk.Target{Gen: 1, AfterStep: tc.after}
			baseline, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, tc.name), crashwalk.RunPlan{Targets: []crashwalk.Target{first}})
			if err != nil {
				t.Fatal(err)
			}
			if len(baseline.Reached) != 1 || !baseline.Reached[0] || len(baseline.Hits) != 2 || len(baseline.Violations) != 0 {
				t.Fatalf("incomplete drain baseline: reached=%v generations=%d violations=%v", baseline.Reached, len(baseline.Hits), baseline.Violations)
			}
			for _, name := range tc.points {
				t.Run(name, func(t *testing.T) {
					point, err := fsx.ParsePoint(name)
					if err != nil {
						t.Fatal(err)
					}
					target := crashwalk.Target{Gen: 2, Point: point}
					r, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, tc.name), crashwalk.RunPlan{Targets: []crashwalk.Target{first, target}})
					if err != nil {
						t.Fatal(err)
					}
					if len(r.Reached) != 2 || !r.Reached[0] || !r.Reached[1] {
						t.Fatalf("repeated drain did not reach both cuts: %v", r.Reached)
					}
					// The shared checker judges the second cut and final generation;
					// the explicit first boundary has already been checked above.
					r.Reached = r.Reached[1:]
					if err := crashwalk.CheckRecoveryTarget(baseline.Hits[1], target, r); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}
