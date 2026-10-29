package recipes

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func bodyRecoveryRecipes(b Bindings) []ScriptRecipe {
	return []ScriptRecipe{bodyCleanupRecipe(b, false), bodyCleanupRecipe(b, true)}
}

func bodyCleanupRecipe(b Bindings, archive bool) ScriptRecipe {
	name := "recovery-body-feedback"
	budget := 48
	if archive {
		name = "recovery-body-archive"
		budget = 74
	}
	facts := &DeliveryEvidence{}
	var message adapterproto.MessageID
	var target string
	classified, accepted, lost, closed, advanced := false, false, false, false, false
	const body = "A damaged source must be reported before its retained bytes disappear."
	return ScriptRecipe{
		Script: scenario.Script{Name: name, Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: budget}, Steps: []scenario.Step{
			{Name: "send-to-absent-node", Do: func(e scenario.Env) error {
				var err error
				target, err = absentNodeAddress(e.Addr("receiver"))
				if err != nil {
					return err
				}
				r, err := e.Agent("sender").Send(e.Ctx(), fakeagent.SendInput{To: []string{target}, Body: body})
				if err == nil {
					if invalid := r.MessageID.Validate(); invalid != nil {
						return fmt.Errorf("source send returned an invalid message: %w", invalid)
					}
					if len(r.To) != 1 || r.To[0] != target || r.Replayed {
						return fmt.Errorf("source send did not create the requested delivery: %+v", r)
					}
					message = r.MessageID
					facts.Accepted(r, body)
				}
				return err
			}},
		}},
		RecoveryTriggers: []crashwalk.Trigger{crashwalk.CorruptOffline("source-body", "flip-body")},
		Drain: func(e scenario.Env) error {
			if !classified && message != "" {
				status, err := e.Agent("sender").Status(e.Ctx(), fakeagent.StatusInput{MessageID: message})
				if err != nil {
					return err
				}
				if len(status.Recipients) != 1 || string(status.Recipients[0].To) != target {
					return fmt.Errorf("source status does not name its actual recipient: %+v", status)
				}
				switch status.Recipients[0].State {
				case adapterproto.DeliveryQueued:
					facts.RequireFeedback(message, target, delivery.KindExpiredUnconfirmed)
				case adapterproto.DeliveryUnknown:
					lost = true
					facts.RequireFeedback(message, target, delivery.KindLostAtSender)
				default:
					return fmt.Errorf("unexpected source before body cleanup: %+v", status)
				}
				classified, accepted = true, true
			}
			if archive && lost && !closed {
				// These creates precede any visible record replacement. Keep the
				// real lost source and its body until the archive sweep owns them.
				if err := b.Delivery.RetainLostSourceBody(e); err != nil {
					return err
				}
			}
			if !closed {
				if !advanced {
					e.Advance(4 * time.Hour)
					advanced = true
					if err := e.Quiesce(); err != nil {
						return err
					}
				}
				if err := drainFeedback(e, facts, "sender"); err != nil {
					return err
				}
				if archive {
					if err := facts.Check(e); err != nil {
						return err
					}
					r, err := b.Management.CloseEndpoint(e, ManagementRequest{ID: admin.OperationID{91}}, e.Addr("sender"))
					if err != nil {
						return err
					}
					if r.Code != "" || r.Value != "closed" {
						return fmt.Errorf("reported source sender did not close: %+v", r)
					}
					closed = true
				}
			}
			if archive {
				e.Advance(3*24*time.Hour + time.Hour)
				return e.Quiesce()
			}
			return nil
		},
		Check: func(e scenario.Env) error {
			if err := facts.Check(e); err != nil {
				return err
			}
			got, err := b.Maintenance.InspectRetention(e, "")
			if err != nil {
				return err
			}
			if got.Bodies != 0 || archive && (got.Records != 0 || accepted && got.Summaries == 0 || !closed) {
				return fmt.Errorf("retained source body or unswept records: %+v closed=%v", got, closed)
			}
			return b.Recovery.CheckClosedRecords(e)
		},
	}
}
