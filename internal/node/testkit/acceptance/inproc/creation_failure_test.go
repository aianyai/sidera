package inproc

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func TestFailedCreationRecipesPreserveRefusalAcrossRestart(t *testing.T) {
	point, err := fsx.ParsePoint("control-intake/derived.rename#1")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		target crashwalk.Target
	}{
		{"session-create-failure", crashwalk.Target{Gen: 1, Point: point}},
		{"session-create-unknown-preset", crashwalk.Target{Gen: 1, AfterStep: "create"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := harness.NewWalkBackend(t)
			clean, err := backend.Run(t.Context(), freshScenario(t, tc.name), crashwalk.RunPlan{Record: true})
			if err != nil {
				t.Fatal(err)
			}
			assertDeclaredWrites(t, clean)
			if len(clean.Hits) != 1 || len(clean.Violations) != 0 {
				t.Fatalf("clean refusal: generations=%d violations=%v", len(clean.Hits), clean.Violations)
			}
			if err := crashwalk.CheckBudget(freshScenario(t, tc.name), clean.Hits[0]); err != nil {
				t.Fatal(err)
			}
			r, err := backend.Run(t.Context(), freshScenario(t, tc.name), crashwalk.RunPlan{Targets: []crashwalk.Target{tc.target}})
			if err != nil {
				t.Fatal(err)
			}
			assertDeclaredWrites(t, r)
			t.Logf("target=%+v reached=%v generations=%d", tc.target, r.Reached, len(r.Hits))
			if len(r.Hits) != 2 || len(r.Reached) != 1 || !r.Reached[0] || len(r.Violations) != 0 {
				t.Fatalf("interrupted refusal: generations=%d reached=%v violations=%v", len(r.Hits), r.Reached, r.Violations)
			}
			if tc.target.AfterStep == "" {
				if err := crashwalk.CompareRuns(clean.Hits[0], r.Hits[0], tc.target); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
