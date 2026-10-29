package inproc

import (
	"context"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func TestTerminalRecipesInterruptEveryWritingPoint(t *testing.T) {
	for _, recipe := range recipes.New(newBindings()).Terminals {
		t.Run(recipe.Name, func(t *testing.T) {
			b := harness.NewWalkBackend(t)
			clean, err := b.Run(context.Background(), bindTerminal(recipe), crashwalk.RunPlan{Record: true})
			if err != nil || len(clean.Violations) != 0 {
				t.Fatalf("clean: error=%v violations=%v", err, clean.Violations)
			}
			targets := crashwalk.Plan(clean.Hits[0], crashwalk.Full, nil)
			t.Logf("terminal scenario=%s points=%d budget=%d boundaries=%d", recipe.Name, len(targets), recipe.MaxPoints, len(bindTerminal(recipe).Steps()))
			absentResult := false
			rejectedRecovery := false
			for _, target := range targets {
				target.Gen = 1
				s := bindTerminal(recipe)
				judge := s.Check
				s.Check = func(f harness.TerminalFacts) error {
					if f.RecoveryRejected {
						rejectedRecovery = true
						if f.Audit != nil {
							t.Error("recovery refusal was hidden as a final audit")
						}
						if len(f.Persisted) == 0 {
							absentResult = true
							if len(f.Response) != 0 {
								t.Error("an unanswered interrupted request acquired an invented response")
							}
						}
					}
					return judge(f)
				}
				out, err := b.Run(context.Background(), s, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
				if err != nil || len(out.Violations) != 0 {
					t.Fatalf("target=%+v error=%v violations=%v", target, err, out.Violations)
				}
				if len(out.Hits) != 2 || len(out.Reached) != 1 || !out.Reached[0] {
					t.Fatalf("target did not own its actual recovery: %+v", out)
				}
				if err := crashwalk.CompareRuns(clean.Hits[0], out.Hits[0], target); err != nil {
					t.Fatal(err)
				}
				if err := checkDeclaredWrites(harness.ProgramTable(t), out); err != nil {
					t.Fatal(err)
				}
			}
			if recipe.Kind == recipes.Retire && (!rejectedRecovery || !absentResult) {
				t.Fatal("no actual irrevocable cut with an unanswered, unstored request was exercised")
			}
		})
	}
}
