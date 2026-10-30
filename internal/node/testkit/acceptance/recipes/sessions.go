package recipes

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// SessionOps binds only session operations missing from scenario.Env.
type SessionOps struct {
	RuntimeAddr             func(scenario.Env, string) string
	Session                 func(scenario.Env, string) fakeagent.Agent
	CheckpointPut           func(scenario.Env, string, adapterproto.CheckpointPutParams) error
	FailCreateSettlement    func(scenario.Env) error
	HasUnknownCreationAlert func(scenario.Env, string) (bool, error)
	CheckClosedSession      func(scenario.Env, string) error
}

func sessionRecipes(b Bindings) []ScriptRecipe {
	out := []ScriptRecipe{sessionLifecycleRecipe(b), failedCreationRecipe(b, "session-create-failure", "default", &fakeadapter.Outcome{Kind: fakeadapter.OutcomeFail, Reason: string(adapterproto.CreateFailPresetInvalid)}, 80), failedCreationRecipe(b, "session-create-unknown-preset", "unconfigured-preset", nil, 42), sessionExpiryRecipe(b), controlWriteExpiryRecipe(b), blockedRecipe(b)}
	for i := range out {
		out[i].Needs.RuntimeAdapter = true
	}
	return out
}

type createdSession struct {
	control                   adapterproto.MessageID
	endpoint, address, native string
}

func (s *createdSession) create(b Bindings, body string) scenario.Step {
	return scenario.Step{Name: "create", Do: func(e scenario.Env) error {
		r, err := e.Agent("sender").Control(e.Ctx(), fakeagent.ControlInput{To: b.Sessions.RuntimeAddr(e, "chat"), Signal: adapterproto.SignalSessionCreate, Preset: "default", Body: body})
		if err == nil {
			s.control = r.MessageID
		}
		return err
	}}
}

func (s *createdSession) resolve(b Bindings, body string, facts *DeliveryEvidence) scenario.Step {
	return scenario.Step{Name: "created-reply", Do: func(e scenario.Env) error {
		r, err := readCreation(e, "sender", b.Sessions.RuntimeAddr(e, "chat"), s.control)
		if err != nil {
			return err
		}
		if r.status.ControlReply == nil || r.status.ControlReply.Create.Outcome != adapterproto.CreateReplyCreated {
			return fmt.Errorf("creation has no real created reply: %+v", r.status)
		}
		s.endpoint, s.address, s.native = r.endpoint, r.address, r.native
		if body != "" {
			facts.Accepted(fakeagent.SendResult{MessageID: s.control, To: []string{s.address}}, body)
		}
		return nil
	}}
}

// drain resolves a returned creation and receives from its actual session. It
// also finishes creation when a crash interrupted the script before resolution.
func (s *createdSession) drain(b Bindings, body string, facts *DeliveryEvidence) func(scenario.Env) error {
	var resolutionFeedback, receivedFeedback feedbackDrain
	return func(e scenario.Env) error {
		if s.control == "" {
			return nil
		}
		if s.endpoint == "" {
			if err := resolutionFeedback.run(e, facts, "sender"); err != nil {
				return err
			}
			if err := s.resolve(b, body, facts).Do(e); err != nil {
				return err
			}
		}
		if err := facts.RecvAndAck(e, b.Sessions.Session(e, s.endpoint)); err != nil {
			return err
		}
		return receivedFeedback.run(e, facts, "sender")
	}
}

func runtimePlan(plan fakeruntime.TurnPlan) scenario.Step {
	return scenario.Step{Name: "first-turn-plan", Do: func(e scenario.Env) error {
		runtime, err := e.Runtime()
		if err != nil {
			return err
		}
		return runtime.SetFirstTurn(plan)
	}}
}

func sessionLifecycleRecipe(b Bindings) ScriptRecipe {
	const initial = "Start by reading this initial body."
	session := &createdSession{}
	facts := newDeliveryEvidence(b)
	var creationFeedback, finalFeedback feedbackDrain
	var interrupt, closeID adapterproto.MessageID
	var closeAttempted bool
	steps := []scenario.Step{
		runtimePlan(fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{{Kind: fakeruntime.ActionRecv}}}),
		session.create(b, initial), scenario.Advance("creation-pull", time.Minute), session.resolve(b, initial, facts),
		{Name: "interrupt", Do: func(e scenario.Env) error {
			r, err := e.Agent("sender").Control(e.Ctx(), fakeagent.ControlInput{To: session.address, Signal: adapterproto.SignalSessionInterrupt})
			if err == nil {
				interrupt = r.MessageID
			}
			return err
		}},
		scenario.Advance("interrupt-pull", time.Minute),
		{Name: "interrupted-reply", Do: func(e scenario.Env) error {
			status, err := e.Agent("sender").Status(e.Ctx(), fakeagent.StatusInput{MessageID: interrupt})
			if err != nil {
				return err
			}
			if status.ControlReply == nil || status.ControlReply.Interrupt == nil || status.ControlReply.Interrupt.Outcome != adapterproto.InterruptReplyStopped {
				return fmt.Errorf("interrupt has no stopped reply: %+v", status)
			}
			return nil
		}},
		{Name: "next-turn-plan", Do: func(e scenario.Env) error {
			runtime, err := e.Runtime()
			if err != nil {
				return err
			}
			return runtime.SetPlan(session.native, fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{{Kind: fakeruntime.ActionRecv}}, EndAfter: true})
		}},
		{Name: "send-for-prompt", Do: func(e scenario.Env) error {
			r, err := e.Agent("sender").Send(e.Ctx(), fakeagent.SendInput{To: []string{session.address}, Body: "A prompt must lead to a real model receive."})
			if err == nil {
				facts.Accepted(r, "A prompt must lead to a real model receive.")
			}
			return err
		}},
		scenario.Advance("prompt-interval", handoff.DefaultMinInterval),
		{Name: "checkpoint", Do: func(e scenario.Env) error {
			return b.Sessions.CheckpointPut(e, "chat", adapterproto.CheckpointPutParams{Scope: adapterproto.ScopeSession, ID: session.endpoint, State: []byte(`{"cursor":7}`)})
		}},
		{Name: "close", Do: func(e scenario.Env) error {
			closeAttempted = true
			r, err := e.Agent("sender").Control(e.Ctx(), fakeagent.ControlInput{To: session.address, Signal: adapterproto.SignalSessionClose})
			if err == nil {
				closeID = r.MessageID
			}
			return err
		}}, scenario.Advance("close-pull", time.Minute),
	}
	return ScriptRecipe{Script: scenario.Script{Name: "session-lifecycle", Setup: scenario.Setup{Direct: []string{"sender"}}, Crash: scenario.CrashPlan{MaxPoints: 324}, Steps: steps}, Drain: func(e scenario.Env) error {
		if session.control != "" && session.endpoint == "" {
			if err := creationFeedback.run(e, facts, "sender"); err != nil {
				return err
			}
			if err := session.resolve(b, initial, facts).Do(e); err != nil {
				return err
			}
		}
		if session.endpoint != "" && !closeAttempted {
			if err := facts.RecvAndAck(e, b.Sessions.Session(e, session.endpoint)); err != nil {
				return err
			}
		}
		return finalFeedback.run(e, facts, "sender")
	}, Check: func(e scenario.Env) error {
		if err := facts.Check(e); err != nil {
			return err
		}
		if closeID == "" {
			return nil
		}
		status, err := e.Agent("sender").Status(e.Ctx(), fakeagent.StatusInput{MessageID: closeID})
		if err != nil {
			return err
		}
		if status.ControlReply == nil || status.ControlReply.Close == nil || status.ControlReply.Close.Outcome != adapterproto.CloseReplyClosed {
			return fmt.Errorf("close has no closed reply: %+v", status)
		}
		return b.Sessions.CheckClosedSession(e, session.endpoint)
	}}
}

func failedCreationRecipe(b Bindings, name, preset string, failure *fakeadapter.Outcome, pointBudget int) ScriptRecipe {
	facts := newDeliveryEvidence(b)
	var feedback feedbackDrain
	var message adapterproto.MessageID
	var steps []scenario.Step
	if failure != nil {
		steps = append(steps, scenario.Step{Name: "refuse-create", Do: func(e scenario.Env) error {
			c, err := e.Adapter("chat")
			if err != nil {
				return err
			}
			return c.SetOutcome(e.Ctx(), fakeadapter.RequestCreate, *failure)
		}})
	}
	steps = append(steps, []scenario.Step{
		{Name: "create", Do: func(e scenario.Env) error {
			r, err := e.Agent("sender").Control(e.Ctx(), fakeagent.ControlInput{To: b.Sessions.RuntimeAddr(e, "chat"), Signal: adapterproto.SignalSessionCreate, Preset: preset, Body: "A failed creation must not deliver this body."})
			if err == nil {
				message = r.MessageID
			}
			return err
		}}, scenario.Advance("reply-pull", time.Minute),
	}...)
	return ScriptRecipe{Script: scenario.Script{Name: name, Setup: scenario.Setup{Direct: []string{"sender"}}, Crash: scenario.CrashPlan{MaxPoints: pointBudget}, Steps: steps}, Drain: func(e scenario.Env) error { return feedback.run(e, facts, "sender") }, Check: func(e scenario.Env) error {
		if err := facts.Check(e); err != nil {
			return err
		}
		if message == "" {
			return nil
		}
		status, err := e.Agent("sender").Status(e.Ctx(), fakeagent.StatusInput{MessageID: message})
		if err != nil {
			return err
		}
		if status.ControlReply == nil || status.ControlReply.Create == nil || status.ControlReply.Create.Outcome != adapterproto.CreateReplyFailed {
			return fmt.Errorf("refused creation has no failed reply: %+v", status)
		}
		log, err := e.Ledger()
		if err != nil {
			return err
		}
		if len(log.Kind(ledger.KindRuntimeSessionCreated)) != 0 {
			return fmt.Errorf("refused creation made a real native session")
		}
		return nil
	}}
}

func sessionExpiryRecipe(b Bindings) ScriptRecipe {
	var message adapterproto.MessageID
	var result fakeagent.SendResult
	var deadlineAdvanced, creationPulled, createdBeforeDeadline bool
	session := &createdSession{}
	facts := newDeliveryEvidence(b)
	var feedback feedbackDrain
	const body = "An expired control must not claim a successful creation."
	deadline := scenario.Step{Name: "control-deadline", Do: func(e scenario.Env) error {
		if !deadlineAdvanced {
			deadlineAdvanced = true
			e.Advance(4 * time.Hour)
		}
		return nil
	}}
	return ScriptRecipe{Script: scenario.Script{Name: "session-create-expiry", Setup: scenario.Setup{Direct: []string{"sender"}}, Crash: scenario.CrashPlan{MaxPoints: 122}, Steps: []scenario.Step{
		{Name: "hold-create", Do: func(e scenario.Env) error {
			c, err := e.Adapter("chat")
			if err != nil {
				return err
			}
			return c.SetOutcome(e.Ctx(), fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeHold})
		}},
		{Name: "create", Do: func(e scenario.Env) error {
			r, err := e.Agent("sender").Control(e.Ctx(), fakeagent.ControlInput{To: b.Sessions.RuntimeAddr(e, "chat"), Signal: adapterproto.SignalSessionCreate, Preset: "default", Body: body})
			if err == nil {
				message, result = r.MessageID, r
			}
			return err
		}}, deadline,
	}}, Drain: func(e scenario.Env) error {
		if !deadlineAdvanced && message != "" {
			if !creationPulled {
				creationPulled = true
				e.Advance(time.Minute)
			}
			if err := e.Quiesce(); err != nil {
				return err
			}
			status, err := e.Agent("sender").Status(e.Ctx(), fakeagent.StatusInput{MessageID: message})
			if err != nil {
				return err
			}
			// Restart may complete a held creation before its deadline phase began.
			// That actual success still owes the original body at its native session.
			if !createdBeforeDeadline && status.ControlReply != nil && status.ControlReply.Create != nil && status.ControlReply.Create.Outcome == adapterproto.CreateReplyCreated {
				createdBeforeDeadline = true
				session.control = message
				facts.AcceptedCreation(result, body, "sender")
			}
		}
		if createdBeforeDeadline {
			if session.endpoint == "" {
				if err := session.resolve(b, "", facts).Do(e); err != nil {
					return err
				}
			}
			return facts.RecvAndAck(e, b.Sessions.Session(e, session.endpoint))
		}
		if message != "" {
			if err := deadline.Do(e); err != nil {
				return err
			}
			if err := e.Quiesce(); err != nil {
				return err
			}
		}
		c, err := e.Adapter("chat")
		if err != nil {
			return err
		}
		if err := c.ClearOutcomes(e.Ctx()); err != nil {
			return err
		}
		if err := c.ReleaseHeld(e.Ctx()); err != nil {
			return err
		}
		return feedback.run(e, facts, "sender")
	}, Check: func(e scenario.Env) error {
		if createdBeforeDeadline {
			return facts.Check(e)
		}
		if message == "" {
			return nil
		}
		status, err := e.Agent("sender").Status(e.Ctx(), fakeagent.StatusInput{MessageID: message})
		if err != nil {
			return err
		}
		if status.ControlReply == nil || status.ControlReply.Create == nil || status.ControlReply.Create.Outcome != adapterproto.CreateReplyUnconfirmed || status.ControlReply.Create.Reason != adapterproto.CreateReasonReadyTimeout {
			return fmt.Errorf("expired held creation has no unconfirmed ready_timeout reply: %+v", status)
		}
		endpoint := string(status.ControlReply.Create.SessionEndpoint)
		runtime, err := ident.ParseAddress(b.Sessions.RuntimeAddr(e, "chat"))
		if err != nil {
			return err
		}
		id, err := ident.ParseEndpointID(endpoint)
		if err != nil {
			return err
		}
		target := ident.Address{Node: runtime.Node, Endpoint: id}.String()
		// The control reply itself was delivered. Its initial body is a separate
		// derived delivery to the allocated session, whose report window expired.
		facts.Accepted(fakeagent.SendResult{MessageID: message, To: []string{target}}, body)
		facts.RequireFeedback(message, target, delivery.KindAcceptedUnconfirmed)
		if err := b.Sessions.CheckClosedSession(e, endpoint); err != nil {
			return err
		}
		return facts.Check(e)
	}}
}

func blockedRecipe(b Bindings) ScriptRecipe {
	var blocking, blocked bool
	var inputs fakeadapter.ExternalInputs
	session := &createdSession{}
	facts := newDeliveryEvidence(b)
	var blockingFeedback, recoveredFeedback feedbackDrain
	var message adapterproto.MessageID
	return ScriptRecipe{Script: scenario.Script{Name: "session-blocked-recovered", Setup: scenario.Setup{Direct: []string{"sender"}}, Crash: scenario.CrashPlan{MaxPoints: 285}, Steps: []scenario.Step{
		runtimePlan(fakeruntime.TurnPlan{EndAfter: true}), session.create(b, ""), scenario.Advance("creation-pull", time.Minute), session.resolve(b, "", facts),
		{Name: "refuse-prompts", Do: func(e scenario.Env) error {
			inputs.PromptRefusal = adapterproto.RefusalRuntimeAuth
			return e.SetAdapterInputs("chat", inputs)
		}},
		{Name: "auth-unavailable", Do: func(e scenario.Env) error {
			log, err := e.Ledger()
			if err != nil {
				return err
			}
			entries := log.Entries()
			if len(entries) == 0 {
				return fmt.Errorf("runtime has no clock evidence")
			}
			inputs.Availability = []adapterproto.Availability{{Condition: adapterproto.ConditionRuntimeAuth, SinceMS: entries[len(entries)-1].AtMS}}
			return e.SetAdapterInputs("chat", inputs)
		}},
		{Name: "send", Do: func(e scenario.Env) error {
			r, err := e.Agent("sender").Send(e.Ctx(), fakeagent.SendInput{To: []string{session.address}, Body: "Read after credentials recover."})
			if err == nil {
				message = r.MessageID
				facts.Accepted(r, "Read after credentials recover.")
			}
			return err
		}}, {Name: "blocked-threshold", Do: func(e scenario.Env) error {
			blocking = true
			e.Advance(handoff.DefaultBlockedThreshold + time.Second)
			return nil
		}}, scenario.Advance("blocked-pull", time.Minute), scenario.Advance("blocked-merge", time.Minute),
		{Name: "read-block-notice", Do: func(e scenario.Env) error {
			if err := facts.RecvAndAck(e, e.Agent("sender")); err != nil {
				return err
			}
			blocking, blocked = false, true
			return nil
		}},
		{Name: "restore-prompt", Do: func(e scenario.Env) error {
			inputs.PromptRefusal = ""
			return e.SetAdapterInputs("chat", inputs)
		}},
		{Name: "restore-credentials", Do: func(e scenario.Env) error {
			inputs.Availability = nil
			return e.SetAdapterInputs("chat", inputs)
		}},
		{Name: "read", Do: func(e scenario.Env) error { return facts.RecvAndAck(e, b.Sessions.Session(e, session.endpoint)) }},
	}}, Drain: func(e scenario.Env) error {
		if blocking {
			e.Advance(handoff.DefaultBlockedThreshold + time.Second)
			if err := e.Quiesce(); err != nil {
				return err
			}
			if err := blockingFeedback.run(e, facts, "sender"); err != nil {
				return err
			}
			blocking, blocked = false, true
		}
		inputs = fakeadapter.ExternalInputs{}
		if err := e.SetAdapterInputs("chat", inputs); err != nil {
			return err
		}
		if session.endpoint != "" {
			if err := facts.RecvAndAck(e, b.Sessions.Session(e, session.endpoint)); err != nil {
				return err
			}
		}
		return recoveredFeedback.run(e, facts, "sender")
	}, Check: func(e scenario.Env) error {
		if blocked {
			facts.RequireFeedback(message, session.address, delivery.KindDeliveryBlocked)
			facts.RequireFeedback(message, session.address, delivery.KindBlockedRecovered)
		}
		return facts.Check(e)
	}}
}
