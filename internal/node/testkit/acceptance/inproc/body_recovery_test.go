package inproc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func TestBodyCleanupActualCuts(t *testing.T) {
	for _, name := range []string{"recovery-body-feedback", "recovery-body-archive"} {
		t.Run(name, func(t *testing.T) {
			clean, err := recordScenario(t, freshScenario(t, name))
			if err != nil {
				t.Fatal(err)
			}
			assertDeclaredWrites(t, clean)
			t.Run("main", func(t *testing.T) {
				points := crashwalk.Plan(clean.Hits[0], crashwalk.Full, nil)
				t.Logf("main points=%d budget=%d", len(points), freshScenario(t, name).MaxPoints())
				for _, target := range points {
					t.Run(target.Point.String(), func(t *testing.T) {
						r, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, name), crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
						if err != nil {
							t.Fatal(err)
						}
						assertDeclaredWrites(t, r)
						if len(r.Reached) != 1 || !r.Reached[0] || len(r.Hits) != 2 || len(r.Violations) != 0 {
							t.Fatalf("body cut reached=%v generations=%d violations=%v", r.Reached, len(r.Hits), r.Violations)
						}
						if err := crashwalk.CompareRuns(clean.Hits[0], r.Hits[0], target); err != nil {
							t.Fatal(err)
						}
					})
				}
			})
			t.Run("recovery-clock", func(t *testing.T) {
				trigger := crashwalk.CorruptOffline("source-body", "flip-body")
				baseline, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, name), crashwalk.RunPlan{Trigger: &trigger})
				if err != nil {
					t.Fatal(err)
				}
				assertDeclaredWrites(t, baseline)
				if err := crashwalk.CheckRecoveryRecord(clean.Hits[0], trigger, baseline); err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, hit := range baseline.Hits[1] {
					if hit.Point.Tag.Tx != "clock-high-water" || crashwalk.IsSync(hit.Point.Step) {
						continue
					}
					count++
					target := crashwalk.Target{Gen: 2, Point: hit.Point}
					t.Run(target.Point.String(), func(t *testing.T) {
						r, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, name), crashwalk.RunPlan{Trigger: &trigger, Targets: []crashwalk.Target{target}})
						if err != nil {
							t.Fatal(err)
						}
						assertDeclaredWrites(t, r)
						if err := crashwalk.CheckRecoveryTrigger(clean.Hits[0], trigger, r); err != nil {
							t.Fatal(err)
						}
						if err := crashwalk.CheckRecoveryTarget(baseline.Hits[1], target, r); err != nil {
							t.Fatal(err)
						}
					})
				}
				if count == 0 {
					t.Fatal("body recovery recorded no clock writes")
				}
				t.Logf("recovery clock targets=%d", count)
			})
		})
	}
}

func TestBodyCleanupRequiresAValidCloseResponse(t *testing.T) {
	for _, tc := range []struct{ name, code, value string }{
		{"invalid-value", "", "open"},
		{"error-code", "endpoint_unknown", "closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBindings()
			actual := b.Management.CloseEndpoint
			b.Management.CloseEndpoint = func(e scenario.Env, q recipes.ManagementRequest, address string) (recipes.ManagementOutcome, error) {
				out, err := actual(e, q, address)
				if err == nil {
					out.Code, out.Value = tc.code, tc.value
				}
				return out, err
			}
			s := bodyArchiveScenario(t, b)
			_, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Record: true})
			if err == nil || !strings.Contains(err.Error(), "reported source sender did not close") {
				t.Fatalf("invalid close response accepted: %v", err)
			}
		})
	}
}

func TestBodyCleanupResendsAnUnansweredClose(t *testing.T) {
	b := newBindings()
	actual := b.Management.CloseEndpoint
	unanswered := errors.New("close response was not received")
	var requests []recipes.ManagementRequest
	var addresses []string
	b.Management.CloseEndpoint = func(e scenario.Env, q recipes.ManagementRequest, address string) (recipes.ManagementOutcome, error) {
		requests = append(requests, q)
		addresses = append(addresses, address)
		out, err := actual(e, q, address)
		if err == nil && len(requests) == 1 {
			return recipes.ManagementOutcome{}, unanswered
		}
		return out, err
	}
	s := bodyArchiveScenario(t, b)
	drain := s.Drain
	s.Drain = func(h *harness.H) error {
		if err := drain(h); !errors.Is(err, unanswered) {
			t.Fatalf("unanswered close did not propagate its error: %v", err)
		}
		return drain(h)
	}
	r, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Record: true})
	if err != nil {
		t.Fatal(err)
	}
	assertDeclaredWrites(t, r)
	if len(r.Violations) != 0 {
		t.Fatalf("retry failed: %v", r.Violations)
	}
	if len(requests) != 2 || requests[0] != requests[1] || requests[0].ID != (admin.OperationID{91}) || addresses[0] != addresses[1] {
		t.Fatalf("unanswered close was not resent unchanged: requests=%+v addresses=%v", requests, addresses)
	}
}

func bodyArchiveScenario(t *testing.T, b recipes.Bindings) *harness.WalkScenario {
	t.Helper()
	for _, r := range recipes.New(b).Scripts {
		if r.Script.Name == "recovery-body-archive" {
			return &harness.WalkScenario{Script: r.Script, Options: recipeOptions(r.Needs), Drain: func(h *harness.H) error { return r.Drain(h) }, Check: func(h *harness.H) error { return r.Check(h) }}
		}
	}
	t.Fatal("body archive recipe missing")
	return nil
}

func TestBodyRetentionJudgementRejectsAnUnsweptSource(t *testing.T) {
	s := freshScenario(t, "recovery-body-archive").(*harness.WalkScenario)
	// Keep the actual periodic task beyond the drain's bounded retention window.
	s.Options.ArchiveSweepEvery = 365 * 24 * time.Hour
	trigger := crashwalk.CorruptOffline("source-body", "flip-body")
	r, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Trigger: &trigger})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Violations) != 1 || !strings.Contains(r.Violations[0], "retained source body") {
		t.Fatalf("unswept source accepted: %v", r.Violations)
	}
}

// A send cut before returning has no public message identifier to remember, but
// its actual persisted source still has to finish through the bounded drain.
func TestBodyCleanupDrainsASendCutBeforeItsResult(t *testing.T) {
	for _, name := range []string{"recovery-body-feedback", "recovery-body-archive"} {
		t.Run(name, func(t *testing.T) {
			target := crashwalk.Target{Gen: 1, Point: fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "intake", Role: "out"}, Step: fsx.StepRename}, Hit: 1}}
			r, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, name), crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Reached) != 1 || !r.Reached[0] || len(r.Hits) != 2 || len(r.Violations) != 0 {
				t.Fatalf("send cut reached=%v generations=%d violations=%v", r.Reached, len(r.Hits), r.Violations)
			}
		})
	}
}
