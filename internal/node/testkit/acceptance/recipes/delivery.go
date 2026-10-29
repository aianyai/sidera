package recipes

import (
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func deliveryRecipes(b Bindings) []ScriptRecipe {
	return []ScriptRecipe{sendRecipe(), replyRecipe(), registrationRecipe(), secondCheckRecipe(), expiryRecipe(), redeliveryRecipe(), replyAfterFailureRecipe(b), unreachableRecipe()}
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
	var registered, declared bool
	return ScriptRecipe{Script: scenario.Script{Name: "delivery-register-declare", Crash: scenario.CrashPlan{MaxPoints: 17}, Steps: []scenario.Step{
		{Name: "register", Do: func(e scenario.Env) error {
			_, err := e.Direct("declared")
			registered = err == nil
			return err
		}},
		{Name: "declare", Do: func(e scenario.Env) error {
			hidden := true
			err := e.Agent("declared").Declare(e.Ctx(), fakeagent.DeclareInput{HiddenFromDirectory: &hidden})
			declared = err == nil
			return err
		}},
	}}, Check: func(e scenario.Env) error {
		if !registered {
			return nil
		}
		me, err := e.Agent("declared").Me(e.Ctx())
		if err != nil {
			return err
		}
		if declared && !me.Declaration.HiddenFromDirectory {
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

// DeliveryOps names recoverable source-write preparation faults.
type DeliveryOps struct {
	FailSourceUpdate     func(scenario.Env) error
	RetainLostSourceBody func(scenario.Env) error
}

var ErrSourceAlreadyConfirmed = errors.New("acceptance: source receipt already committed")

func replyAfterFailureRecipe(b Bindings) ScriptRecipe {
	facts := &DeliveryEvidence{}
	var original adapterproto.MessageID
	return ScriptRecipe{Script: scenario.Script{Name: "delivery-reply-after-write-failure", Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: 90}, Steps: []scenario.Step{
		{Name: "fail-receipt-write", Do: b.Delivery.FailSourceUpdate},
		{Name: "send", Do: func(e scenario.Env) error {
			r, err := e.Agent("sender").Send(e.Ctx(), fakeagent.SendInput{To: []string{e.Addr("receiver")}, Body: "The reply must prove this body arrived."})
			if err == nil {
				original = r.MessageID
				facts.Accepted(r, "The reply must prove this body arrived.")
			}
			return err
		}},
		{Name: "read", Do: func(e scenario.Env) error { return facts.RecvAndAck(e, e.Agent("receiver")) }},
		{Name: "pending-source", Do: func(e scenario.Env) error {
			status, err := e.Agent("sender").Status(e.Ctx(), fakeagent.StatusInput{MessageID: original})
			if err != nil {
				return err
			}
			if len(status.Recipients) != 1 || status.Recipients[0].State != adapterproto.DeliveryQueued {
				return fmt.Errorf("%w: %+v", ErrSourceAlreadyConfirmed, status)
			}
			return nil
		}},
		{Name: "reply", Do: func(e scenario.Env) error {
			r, err := e.Agent("receiver").Reply(e.Ctx(), fakeagent.ReplyInput{MessageID: original, Body: "This is evidence from a real received message."})
			if err == nil {
				facts.Accepted(r, "This is evidence from a real received message.")
			}
			return err
		}},
	}}, Drain: func(e scenario.Env) error {
		if err := facts.RecvAndAck(e, e.Agent("receiver")); err != nil {
			return err
		}
		if err := facts.RecvAndAck(e, e.Agent("sender")); err != nil {
			return err
		}
		return drainFeedback(e, facts, "sender")
	}, Check: func(e scenario.Env) error {
		if err := facts.Check(e); err != nil {
			return err
		}
		return b.Recovery.CheckClosedRecords(e)
	}}
}

func unreachableRecipe() ScriptRecipe {
	facts := &DeliveryEvidence{}
	return ScriptRecipe{Script: scenario.Script{Name: "delivery-unreachable", Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: 48}, Steps: []scenario.Step{
		{Name: "send-to-an-absent-node", Do: func(e scenario.Env) error {
			target, err := absentNodeAddress(e.Addr("receiver"))
			if err != nil {
				return err
			}
			r, err := e.Agent("sender").Send(e.Ctx(), fakeagent.SendInput{To: []string{target}, Body: "An unreachable node must produce real deadline feedback."})
			if err == nil {
				facts.Accepted(r, "An unreachable node must produce real deadline feedback.")
				facts.RequireFeedback(r.MessageID, target, delivery.KindExpiredUnconfirmed)
			}
			return err
		}}, scenario.Advance("report-deadline", 4*time.Hour),
	}}, Drain: func(e scenario.Env) error { return drainFeedback(e, facts, "sender") }, Check: facts.Check}
}

func absentNodeAddress(address string) (string, error) {
	target, err := ident.ParseAddress(address)
	if err != nil {
		return "", err
	}
	bits := target.Node.Bytes()
	bits[0] ^= 0x80
	target.Node, err = ident.NodeIDFromBytes(bits)
	if err != nil {
		return "", err
	}
	return target.String(), nil
}
