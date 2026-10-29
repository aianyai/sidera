package inproc

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestControlWriteExpiryRecoveryKeepsItsActualResult(t *testing.T) {
	for _, tc := range []struct {
		name, at, after string
		state           adapterproto.DeliveryState
		outcome         adapterproto.ControlCreateOutcome
	}{
		{"complete-deadline", "", "control-report-deadline", adapterproto.DeliveryClosedUnconfirmed, 0},
		{"closing-tick-uncommitted", "tick/ctl.create#1", "", adapterproto.DeliveryDelivered, adapterproto.CreateReplyUnconfirmed},
		{"closing-tick-committed", "tick/out.create#1", "", adapterproto.DeliveryClosedUnconfirmed, 0},
		{"created-before-deadline", "", "create", adapterproto.DeliveryDelivered, adapterproto.CreateReplyCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := freshScenario(t, "session-control-write-expiry").(*harness.WalkScenario)
			check := s.Check
			s.Check = func(h *harness.H) error {
				if err := check(h); err != nil {
					return err
				}
				log, err := h.Ledger()
				if err != nil {
					return err
				}
				for _, returned := range log.Kind(ledger.KindClientResult) {
					if returned.Attrs.Action != "control" || returned.Attrs.Category != "ok" {
						continue
					}
					status, err := h.Agent("sender").Status(h.Ctx(), fakeagent.StatusInput{MessageID: adapterproto.MessageID(returned.Attrs.MessageID)})
					if err != nil {
						return err
					}
					var outcome adapterproto.ControlCreateOutcome
					if status.ControlReply != nil && status.ControlReply.Create != nil {
						outcome = status.ControlReply.Create.Outcome
					}
					if len(status.Recipients) != 1 || status.Recipients[0].State != tc.state || outcome != tc.outcome {
						return fmt.Errorf("unexpected actual control result: status=%+v outcome=%v", status, outcome)
					}
					t.Logf("actual control state=%s creation-outcome=%v", tc.state, outcome)
					return nil
				}
				return fmt.Errorf("no successful control result in the external ledger")
			}
			target := crashwalk.Target{Gen: 1, AfterStep: tc.after}
			if tc.at != "" {
				point, err := fsx.ParsePoint(tc.at)
				if err != nil {
					t.Fatal(err)
				}
				target.Point = point
			}
			r, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Reached) != 1 || !r.Reached[0] || len(r.Hits) != 2 || len(r.Violations) != 0 {
				t.Fatalf("actual outcome: reached=%v generations=%d violations=%v", r.Reached, len(r.Hits), r.Violations)
			}
		})
	}
}

func TestUncontrolledCreationJudgementRequiresItsFailureAndOwnerAlert(t *testing.T) {
	for _, tc := range []struct {
		name, violation string
		change          func(*recipes.Bindings)
	}{
		{"missing-write-failure", "control whose reply could not commit has an unexpected result", func(b *recipes.Bindings) {
			b.Sessions.FailCreateSettlement = func(scenario.Env) error { return nil }
		}},
		{"missing-owner-alert", "no unique owner alert", func(b *recipes.Bindings) {
			b.Sessions.HasUnknownCreationAlert = func(scenario.Env, string) (bool, error) { return false, nil }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBindings()
			tc.change(&b)
			for _, r := range recipes.New(b).Scripts {
				if r.Script.Name != "session-control-write-expiry" {
					continue
				}
				s := &harness.WalkScenario{Script: r.Script, Options: recipeOptions(r.Needs), Drain: func(h *harness.H) error { return r.Drain(h) }, Check: func(h *harness.H) error { return r.Check(h) }}
				result, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Record: true})
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Violations) != 1 || !strings.Contains(result.Violations[0], tc.violation) {
					t.Fatalf("uncontrolled creation without %s was accepted: %v", tc.name, result.Violations)
				}
				return
			}
			t.Fatal("control write-expiry recipe missing")
		})
	}
}
