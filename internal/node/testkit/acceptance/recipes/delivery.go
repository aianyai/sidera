package recipes

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func deliveryRecipes(_ Bindings) []ScriptRecipe {
	return []ScriptRecipe{sendRecipe(), replyRecipe(), registrationRecipe(), secondCheckRecipe(), expiryRecipe(), redeliveryRecipe()}
}

func sendRecipe() ScriptRecipe {
	const body = "A real body must survive acceptance."
	facts := &DeliveryEvidence{}
	return ScriptRecipe{
		Script: scenario.Script{Name: "delivery-send", Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: 42}, Steps: []scenario.Step{
			{Name: "send", Do: sendBody(facts, "sender", "receiver", body)},
		}},
		Drain: func(e scenario.Env) error { return facts.RecvAndAck(e, e.Agent("receiver")) },
		Check: facts.Check,
	}
}

func sendBody(facts *DeliveryEvidence, from, to, body string) func(scenario.Env) error {
	return func(e scenario.Env) error {
		result, err := e.Agent(from).Send(e.Ctx(), fakeagent.SendInput{To: []string{e.Addr(to)}, Body: body})
		if err == nil {
			facts.Accepted(result, body)
		}
		return err
	}
}

func registrationRecipe() ScriptRecipe {
	return ScriptRecipe{Script: scenario.Script{Name: "delivery-register-declare", Crash: scenario.CrashPlan{MaxPoints: 17}, Steps: []scenario.Step{
		{Name: "register", Do: func(e scenario.Env) error { _, err := e.Direct("declared"); return err }},
		{Name: "declare", Do: func(e scenario.Env) error {
			hidden := true
			return e.Agent("declared").Declare(e.Ctx(), fakeagent.DeclareInput{HiddenFromDirectory: &hidden})
		}},
	}}, Check: func(e scenario.Env) error {
		me, err := e.Agent("declared").Me(e.Ctx())
		if err != nil {
			return err
		}
		if !me.Declaration.HiddenFromDirectory {
			return fmt.Errorf("declaration was not applied")
		}
		return nil
	}}
}

func replyRecipe() ScriptRecipe {
	facts := &DeliveryEvidence{}
	var original adapterproto.MessageID
	return ScriptRecipe{Script: scenario.Script{Name: "delivery-reply", Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: 95}, Steps: []scenario.Step{
		{Name: "send", Do: func(e scenario.Env) error {
			r, err := e.Agent("sender").Send(e.Ctx(), fakeagent.SendInput{To: []string{e.Addr("receiver")}, Body: "Read this before replying."})
			if err == nil {
				original = r.MessageID
				facts.Accepted(r, "Read this before replying.")
			}
			return err
		}},
		{Name: "read-before-reply", Do: func(e scenario.Env) error {
			r, err := e.Agent("receiver").Recv(e.Ctx(), fakeagent.RecvInput{Frontend: adapterproto.FrontendCLI})
			if err != nil {
				return err
			}
			if len(r.Page.Messages) != 1 || r.Page.Messages[0].ID != original || r.Page.Messages[0].Body != "Read this before replying." {
				return fmt.Errorf("reply has no complete visible original: %+v", r.Page)
			}
			return nil
		}},
		{Name: "reply", Do: func(e scenario.Env) error {
			r, err := e.Agent("receiver").Reply(e.Ctx(), fakeagent.ReplyInput{MessageID: original, Body: "The reply carries real evidence."})
			if err == nil {
				facts.Accepted(r, "The reply carries real evidence.")
			}
			return err
		}},
	}}, Drain: func(e scenario.Env) error {
		if err := facts.RecvAndAck(e, e.Agent("sender")); err != nil {
			return err
		}
		return facts.RecvAndAck(e, e.Agent("receiver"))
	}, Check: facts.Check}
}

func secondCheckRecipe() ScriptRecipe {
	facts := &DeliveryEvidence{}
	return ScriptRecipe{Script: scenario.Script{Name: "delivery-second-check", Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: 78}, Steps: []scenario.Step{
		{Name: "send", Do: sendBody(facts, "sender", "receiver", "Queued before the declaration narrows.")},
		{Name: "narrow", Do: func(e scenario.Env) error {
			restrict := true
			allowed := []string{e.Addr("receiver")}
			return e.Agent("receiver").Declare(e.Ctx(), fakeagent.DeclareInput{RestrictSenders: &restrict, AcceptFrom: &allowed})
		}},
		{Name: "second-check", Do: func(e scenario.Env) error { return facts.RecvAndAck(e, e.Agent("receiver")) }},
	}}, Drain: func(e scenario.Env) error {
		if err := facts.RecvAndAck(e, e.Agent("receiver")); err != nil {
			return err
		}
		return drainFeedback(e, facts, "sender")
	}, Check: facts.Check}
}

func expiryRecipe() ScriptRecipe {
	facts := &DeliveryEvidence{}
	return ScriptRecipe{Script: scenario.Script{Name: "delivery-expiry", Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: 72}, Steps: []scenario.Step{
		{Name: "send", Do: sendBody(facts, "sender", "receiver", "The queue deadline must not silently discard this body.")},
		scenario.Advance("queue-deadline", delivery.DefaultEndpointMaxQueue+time.Minute),
	}}, Drain: func(e scenario.Env) error {
		if err := facts.RecvAndAck(e, e.Agent("receiver")); err != nil {
			return err
		}
		return drainFeedback(e, facts, "sender")
	}, Check: facts.Check}
}

func redeliveryRecipe() ScriptRecipe {
	facts := &DeliveryEvidence{}
	steps := []scenario.Step{{Name: "send", Do: sendBody(facts, "sender", "receiver", "A lease expires without acknowledgement.")}}
	for i := 0; i < delivery.MaxLeases; i++ {
		steps = append(steps, scenario.Step{Name: fmt.Sprintf("handout-%d", i+1), Do: func(e scenario.Env) error {
			_, err := e.Agent("receiver").Recv(e.Ctx(), fakeagent.RecvInput{Frontend: adapterproto.FrontendCLI})
			return err
		}}, scenario.Advance(fmt.Sprintf("lease-expiry-%d", i+1), delivery.LeaseDuration))
	}
	return ScriptRecipe{Script: scenario.Script{Name: "delivery-redelivery", Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: 144}, Steps: steps}, Drain: func(e scenario.Env) error {
		if err := facts.RecvAndAck(e, e.Agent("receiver")); err != nil {
			return err
		}
		return drainFeedback(e, facts, "sender")
	}, Check: facts.Check}
}

// Pulls run once a minute and feedback merges for a minute. Two bounded advances
// let the source learn a final result, then let its notice become readable.
func drainFeedback(e scenario.Env, facts *DeliveryEvidence, sender string) error {
	for range 2 {
		e.Advance(time.Minute)
		if err := e.Quiesce(); err != nil {
			return err
		}
	}
	return facts.RecvAndAck(e, e.Agent(sender))
}
