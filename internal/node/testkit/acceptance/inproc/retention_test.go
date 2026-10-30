package inproc

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func TestRetentionJudgementRejectsAnArchiveThatWasNeverExpired(t *testing.T) {
	s := freshScenario(t, "maintenance-retention").(*harness.WalkScenario)
	for i := range s.Script.Steps {
		if s.Script.Steps[i].Name == "archive-retention" {
			s.Script.Steps[i].Do = func(scenario.Env) error { return nil }
		}
	}
	r, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Record: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Violations) != 1 || !strings.Contains(r.Violations[0], "expired archive") {
		t.Fatalf("unexpired archive accepted: %v", r.Violations)
	}
}

func TestRetentionDrainCompletesARealCutBeforeAndDuringTheSweep(t *testing.T) {
	for _, step := range []string{"creation-pull", "send-from-session", "record-retention", "archive-retention"} {
		t.Run(step, func(t *testing.T) {
			s := freshScenario(t, "maintenance-retention")
			r, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Targets: []crashwalk.Target{{Gen: 1, AfterStep: step}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Reached) != 1 || !r.Reached[0] || len(r.Hits) != 2 || len(r.Violations) != 0 {
				t.Fatalf("retention cut reached=%v generations=%d violations=%v", r.Reached, len(r.Hits), r.Violations)
			}
		})
	}
}

func TestRetentionDrainResumesInterruptedSessionOperations(t *testing.T) {
	clean, err := harness.NewWalkBackend(t).Run(t.Context(), freshScenario(t, "maintenance-retention"), crashwalk.RunPlan{Record: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(clean.Hits) != 1 || len(clean.Violations) != 0 {
		t.Fatalf("clean retention: generations=%d violations=%v", len(clean.Hits), clean.Violations)
	}
	if err := crashwalk.CheckBudget(freshScenario(t, "maintenance-retention"), clean.Hits[0]); err != nil {
		t.Fatal(err)
	}
	for _, cut := range []struct{ first, second string }{
		{"intake/out.create#1", ""},
		{"intake/commit#1", "ack/body.remove#1"},
		{"intake/commit#3", ""},
		{"ack/body.remove#1", "intake/commit#1"},
	} {
		at := cut.first
		t.Run(at, func(t *testing.T) {
			point, err := fsx.ParsePoint(at)
			if err != nil {
				t.Fatal(err)
			}
			first := crashwalk.Target{Gen: 1, Point: point}
			s := freshScenario(t, "maintenance-retention").(*harness.WalkScenario)
			if at == "intake/out.create#1" {
				check := s.Check
				s.Check = func(h *harness.H) error {
					log, err := h.Ledger()
					if err != nil {
						return err
					}
					if len(log.Kind(ledger.KindRuntimeSessionCreated)) != 0 || len(log.Kind(ledger.KindModelSaw)) != 0 {
						return fmt.Errorf("pre-commit cut unexpectedly created or read a session")
					}
					return check(h)
				}
			}
			backend := harness.NewWalkBackend(t)
			baseline, err := backend.Run(t.Context(), s, crashwalk.RunPlan{Targets: []crashwalk.Target{first}})
			if err != nil {
				t.Fatal(err)
			}
			if len(baseline.Reached) != 1 || !baseline.Reached[0] || len(baseline.Hits) != 2 || len(baseline.Violations) != 0 {
				t.Fatalf("retention cut reached=%v generations=%d violations=%v", baseline.Reached, len(baseline.Hits), baseline.Violations)
			}
			if err := crashwalk.CompareRuns(clean.Hits[0], baseline.Hits[0], first); err != nil {
				t.Fatal(err)
			}
			if cut.second == "" {
				return
			}
			// An unanswered creation must retain its real receipt after body removal.
			// An interrupted initial read must finish before a later close is cut.
			next, err := fsx.ParsePoint(cut.second)
			if err != nil {
				t.Fatal(err)
			}
			second := crashwalk.Target{Gen: 2, Point: next}
			r, err := backend.Run(t.Context(), freshScenario(t, "maintenance-retention"), crashwalk.RunPlan{Targets: []crashwalk.Target{first, second}})
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Reached) != 2 || !r.Reached[0] || !r.Reached[1] {
				t.Fatalf("retention recovery cuts reached=%v", r.Reached)
			}
			r.Reached = r.Reached[1:]
			if err := crashwalk.CheckRecoveryTarget(baseline.Hits[1], second, r); err != nil {
				t.Fatal(err)
			}
		})
	}
}
