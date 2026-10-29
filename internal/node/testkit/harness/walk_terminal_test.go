package harness_test

import (
	"context"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func TestTerminalWalkKeepsRecoveryAndFinalAuditSeparate(t *testing.T) {
	for _, op := range []harness.TerminalOperation{harness.TerminalStop, harness.TerminalRetire} {
		t.Run(op.String(), func(t *testing.T) {
			var facts harness.TerminalFacts
			s := &harness.TerminalWalkScenario{Label: "terminal", Operation: op, RequestID: admin.OperationID{61}, PointBudget: 50, Check: func(f harness.TerminalFacts) error { facts = f; return nil }}
			b := harness.NewWalkBackend(t)
			clean, err := b.Run(context.Background(), s, crashwalk.RunPlan{Record: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(clean.Hits) != 1 || !facts.Exited || facts.Audit == nil || !facts.Audit.Attempted {
				t.Fatalf("clean lifecycle lost its audit: %+v facts=%+v", clean, facts)
			}
			if op == harness.TerminalStop && (!facts.Audit.Started || len(facts.Audit.Replay) == 0 || len(facts.Audit.Query) == 0) {
				t.Fatalf("stop did not restart and replay: %+v", facts)
			}
			if op == harness.TerminalRetire && (!facts.Retired || !facts.Audit.IdentityRetired || !facts.Audit.SigningUnchanged) {
				t.Fatalf("retirement was not verified: %+v", facts)
			}
			targets := crashwalk.Plan(clean.Hits[0], crashwalk.Full, nil)
			for _, target := range targets {
				target.Gen = 1
				out, err := b.Run(context.Background(), s, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
				if err != nil {
					t.Fatal(err)
				}
				if len(out.Hits) != 2 || !out.Reached[0] {
					t.Fatalf("cut lost its real recovery generation: %+v", out)
				}
				if err := crashwalk.CompareRuns(clean.Hits[0], out.Hits[0], target); err != nil {
					t.Fatal(err)
				}
			}
			out, err := b.Run(context.Background(), s, crashwalk.RunPlan{Targets: []crashwalk.Target{{Gen: 1, AfterStep: "request-termination"}}})
			if err != nil {
				t.Fatal(err)
			}
			if out.Reached[0] || len(out.Hits) != 1 {
				t.Fatalf("natural exit claimed a target: %+v", out)
			}
		})
	}
}
