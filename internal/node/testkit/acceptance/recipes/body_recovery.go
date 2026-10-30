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
	budget := 66
	if archive {
		name = "recovery-body-archive"
		budget = 98
	}
	facts := newDeliveryEvidence(b)
	var feedback feedbackDrain
	var message adapterproto.MessageID
	var target string
	classified, accepted, lost, closed, advanced := false, false, false, false, false
	retentionPrepared := false
	feedbackCompleted := false
	const body = "A damaged source must be reported before its retained bytes disappear."
	needs := Needs{Damage: DamageSourceBody, ArchiveSweepEvery: time.Hour}
	if archive {
		needs.FSFaults = []FSFault{FaultRetainLostBody}
	}
	return ScriptRecipe{
		Needs: needs,
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
					if len(r.To) != 1 || r.To[0] != target || r.Replay != fakeagent.ReplayFresh {
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
			if archive && lost && !retentionPrepared {
				// These creates precede any visible record replacement. Keep the
				// real lost source and its body for this process's archive sweep.
				// A restart may already replay the feedback before Drain resumes.
				if err := b.Delivery.RetainLostSourceBody(e); err != nil {
					return err
				}
				retentionPrepared = true
			}
			if !feedbackCompleted {
				if !advanced {
					e.Advance(4 * time.Hour)
					advanced = true
					if err := e.Quiesce(); err != nil {
						return err
					}
				}
				if err := feedback.run(e, facts, "sender"); err != nil {
					return err
				}
				if archive {
					if err := facts.Check(e); err != nil {
						return err
					}
					feedbackCompleted = true
				}
			}
			if archive && !closed {
				// The close may commit before its response is recorded. Retry the
				// same request without receiving again from the closed endpoint.
				r, err := b.Management.CloseEndpoint(e, ManagementRequest{ID: admin.OperationID{91}}, e.Addr("sender"))
				if err != nil {
					return err
				}
				if r.Code != "" || r.Value != "closed" && r.Value != "already_closed" {
					return fmt.Errorf("reported source sender did not close: %+v", r)
				}
				closed = true
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
			if err := checkRecoveryDamage(b, e); err != nil {
				return err
			}
			return b.Recovery.CheckClosedRecords(e)
		},
	}
}
