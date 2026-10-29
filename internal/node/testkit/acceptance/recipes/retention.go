package recipes

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// RetentionFacts reduces the final storage audit to facts about this recipe.
type RetentionFacts struct {
	Records, Bodies, Archives, Summaries int
	SessionPresent                       bool
}
type MaintenanceOps struct {
	InspectRetention func(scenario.Env, string) (RetentionFacts, error)
}

func maintenanceRecipes(b Bindings) []ScriptRecipe { return []ScriptRecipe{retentionRecipe(b)} }

func retentionRecipe(b Bindings) ScriptRecipe {
	session := &createdSession{}
	facts := newDeliveryEvidence(b)
	var feedback feedbackDrain
	var closing adapterproto.MessageID
	summarized := false
	// A clock move takes effect before its resulting writes can be interrupted.
	// Preserve that progress so another drain cannot age out unread evidence.
	advanceOnce := func(name string, delay time.Duration) scenario.Step {
		advanced := false
		return scenario.Step{Name: name, Do: func(e scenario.Env) error {
			if !advanced {
				advanced = true
				e.Advance(delay)
			}
			return nil
		}}
	}
	recordRetention := advanceOnce("record-retention", 3*24*time.Hour)
	endpointSweep := advanceOnce("endpoint-sweep", time.Hour)
	archiveRetention := advanceOnce("archive-retention", 31*24*time.Hour)
	const initial = "Read this before the session and its records are retired."
	return ScriptRecipe{Needs: Needs{RuntimeAdapter: true, ArchiveSweepEvery: time.Hour}, Script: scenario.Script{Name: "maintenance-retention", Setup: scenario.Setup{Direct: []string{"sender"}}, Crash: scenario.CrashPlan{MaxPoints: 246}, Steps: []scenario.Step{
		session.create(b, initial), scenario.Advance("creation-pull", time.Minute), session.resolve(b, initial, facts),
		{Name: "read-initial", Do: func(e scenario.Env) error { return facts.RecvAndAck(e, b.Sessions.Session(e, session.endpoint)) }},
		{Name: "send-from-session", Do: func(e scenario.Env) error {
			r, err := b.Sessions.Session(e, session.endpoint).Send(e.Ctx(), fakeagent.SendInput{To: []string{e.Addr("sender")}, Body: "Keep my sender sequence until every source record is swept."})
			if err == nil {
				facts.Accepted(r, "Keep my sender sequence until every source record is swept.")
			}
			return err
		}},
		{Name: "read-session-message", Do: func(e scenario.Env) error { return facts.RecvAndAck(e, e.Agent("sender")) }},
		scenario.Advance("delivery-pull", time.Minute),
		{Name: "close", Do: func(e scenario.Env) error {
			r, err := e.Agent("sender").Control(e.Ctx(), fakeagent.ControlInput{To: session.address, Signal: adapterproto.SignalSessionClose, IdemKey: "retention-close"})
			if err == nil {
				closing = r.MessageID
			}
			return err
		}}, scenario.Advance("close-pull", time.Minute),
		{Name: "closed-reply", Do: func(e scenario.Env) error {
			r, err := e.Agent("sender").Status(e.Ctx(), fakeagent.StatusInput{MessageID: closing})
			if err != nil {
				return err
			}
			if r.ControlReply == nil || r.ControlReply.Close == nil || r.ControlReply.Close.Outcome != adapterproto.CloseReplyClosed {
				return fmt.Errorf("retiring session has no closed reply: %+v", r)
			}
			return b.Sessions.CheckClosedSession(e, session.endpoint)
		}},
		recordRetention, endpointSweep,
		{Name: "summaries-and-sweep", Do: func(e scenario.Env) error {
			got, err := b.Maintenance.InspectRetention(e, session.endpoint)
			if err != nil {
				return err
			}
			if got.Records != 0 || got.Bodies != 0 || got.SessionPresent || got.Summaries == 0 {
				return fmt.Errorf("retained records did not become real archive summaries: %+v", got)
			}
			if err := facts.Check(e); err != nil {
				return err
			}
			summarized = true
			return nil
		}},
		archiveRetention,
	}}, Drain: func(e scenario.Env) error {
		log, err := e.Ledger()
		if err != nil {
			return err
		}
		if len(log.Kind(ledger.KindFixtureCrash)) == 0 {
			return nil
		}
		// A cut abandons the remaining steps. Resume only from externally known
		// creation/close results, then finish the bounded retention periods.
		if session.endpoint == "" && session.control != "" {
			e.Advance(time.Minute)
			if err := e.Quiesce(); err != nil {
				return err
			}
			status, err := e.Agent("sender").Status(e.Ctx(), fakeagent.StatusInput{MessageID: session.control})
			if err != nil {
				return err
			}
			if status.ControlReply != nil && status.ControlReply.Create != nil && status.ControlReply.Create.Outcome == adapterproto.CreateReplyCreated {
				if err := session.resolve(b, initial, facts).Do(e); err != nil {
					return err
				}
			}
		}
		if session.endpoint != "" && closing == "" {
			if err := facts.RecvAndAck(e, b.Sessions.Session(e, session.endpoint)); err != nil {
				return err
			}
			r, err := e.Agent("sender").Control(e.Ctx(), fakeagent.ControlInput{To: session.address, Signal: adapterproto.SignalSessionClose, IdemKey: "retention-close"})
			if err != nil {
				return err
			}
			closing = r.MessageID
		}
		// Settle any session source before retention starts. Receiving only at the
		// end of the feedback drain leaves its source pending until the next pull.
		if err := facts.RecvAndAck(e, e.Agent("sender")); err != nil {
			return err
		}
		if err := feedback.run(e, facts, "sender"); err != nil {
			return err
		}
		for _, step := range []scenario.Step{recordRetention, endpointSweep} {
			if err := step.Do(e); err != nil {
				return err
			}
			if err := e.Quiesce(); err != nil {
				return err
			}
		}
		// A restart rearms the periodic task. One further ordinary pass completes
		// a sweep cut before its last source or dependent endpoint was removed.
		e.Advance(time.Hour)
		if err := e.Quiesce(); err != nil {
			return err
		}
		if !summarized && session.control != "" {
			got, err := b.Maintenance.InspectRetention(e, session.endpoint)
			if err != nil {
				return err
			}
			if got.Records != 0 || got.Bodies != 0 || got.SessionPresent || got.Summaries == 0 {
				return fmt.Errorf("retained records did not become real archive summaries after a crash: %+v", got)
			}
			summarized = true
		}
		if err := archiveRetention.Do(e); err != nil {
			return err
		}
		return e.Quiesce()
	}, Check: func(e scenario.Env) error {
		got, err := b.Maintenance.InspectRetention(e, session.endpoint)
		if err != nil {
			return err
		}
		if got.Archives != 0 || got.Records != 0 || got.Bodies != 0 || got.SessionPresent {
			return fmt.Errorf("expired archive or retired live records remain: %+v", got)
		}
		return facts.Check(e)
	}}
}
