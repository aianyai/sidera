package recipes

import (
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func deliveryRecipes(b Bindings) []ScriptRecipe {
	return []ScriptRecipe{sendRecipe(b), replyRecipe(b), directRegistrationRecipe(), registrationRecipe(b), secondCheckRecipe(b), expiryRecipe(b), redeliveryRecipe(b), replyAfterFailureRecipe(b), unreachableRecipe(b)}
}

func sendRecipe(b Bindings) ScriptRecipe {
	const body = "A real body must survive acceptance."
	facts := newDeliveryEvidence(b)
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

func directRegistrationRecipe() ScriptRecipe {
	var address string
	return ScriptRecipe{Script: scenario.Script{Name: "delivery-register-direct", Crash: scenario.CrashPlan{MaxPoints: 11}, Steps: []scenario.Step{
		{Name: "register", Do: func(e scenario.Env) error {
			var err error
			address, err = e.Direct("registered")
			return err
		}},
		{Name: "register-same-key", Do: func(e scenario.Env) error {
			again, err := e.Direct("registered")
			if err != nil {
				return err
			}
			if again != address {
				return fmt.Errorf("repeated direct registration changed address from %s to %s", address, again)
			}
			return nil
		}},
	}}}
}

func registrationRecipe(b Bindings) ScriptRecipe {
	facts := newDeliveryEvidence(b)
	session := &createdSession{}
	var declared bool
	return ScriptRecipe{Needs: Needs{RuntimeAdapter: true}, Script: scenario.Script{Name: "delivery-register-declare", Setup: scenario.Setup{Direct: []string{"sender"}}, Crash: scenario.CrashPlan{MaxPoints: 78}, Steps: []scenario.Step{
		session.create(b, ""), scenario.Advance("creation-pull", time.Minute), session.resolve(b, "", facts),
		{Name: "declare", Do: func(e scenario.Env) error {
			hidden := true
			err := b.Sessions.Session(e, session.endpoint).Declare(e.Ctx(), fakeagent.DeclareInput{HiddenFromDirectory: &hidden})
			declared = err == nil
			return err
		}},
	}}, Drain: session.drain(b, "", facts), Check: func(e scenario.Env) error {
		if err := facts.Check(e); err != nil {
			return err
		}
		if !declared {
			return nil
		}
		me, err := b.Sessions.Session(e, session.endpoint).Me(e.Ctx())
		if err != nil {
			return err
		}
		if me.Address != session.address || !me.Declaration.HiddenFromDirectory {
			return fmt.Errorf("session declaration was not applied: %+v", me)
		}
		return nil
	}}
}

func replyRecipe(b Bindings) ScriptRecipe {
	facts := newDeliveryEvidence(b)
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

func secondCheckRecipe(b Bindings) ScriptRecipe {
	facts := newDeliveryEvidence(b)
	session := &createdSession{}
	var message adapterproto.MessageID
	return ScriptRecipe{Needs: Needs{RuntimeAdapter: true}, Script: scenario.Script{Name: "delivery-second-check", Setup: scenario.Setup{Direct: []string{"sender"}}, Crash: scenario.CrashPlan{MaxPoints: 150}, Steps: []scenario.Step{
		runtimePlan(fakeruntime.TurnPlan{}), session.create(b, ""), scenario.Advance("creation-pull", time.Minute), session.resolve(b, "", facts),
		{Name: "send", Do: func(e scenario.Env) error {
			const body = "Queued before the declaration narrows."
			r, err := e.Agent("sender").Send(e.Ctx(), fakeagent.SendInput{To: []string{session.address}, Body: body})
			if err == nil {
				message = r.MessageID
				facts.Accepted(r, body)
			}
			return err
		}},
		{Name: "narrow", Do: func(e scenario.Env) error {
			restrict := true
			allowed := []string{session.address}
			err := b.Sessions.Session(e, session.endpoint).Declare(e.Ctx(), fakeagent.DeclareInput{RestrictSenders: &restrict, AcceptFrom: &allowed})
			if err == nil {
				facts.RequireFeedback(message, session.address, delivery.KindRejected)
			}
			return err
		}},
		{Name: "second-check", Do: func(e scenario.Env) error { return facts.RecvAndAck(e, b.Sessions.Session(e, session.endpoint)) }},
	}}, Drain: session.drain(b, "", facts), Check: facts.Check}
}

func expiryRecipe(b Bindings) ScriptRecipe {
	facts := newDeliveryEvidence(b)
	var feedback feedbackDrain
	return ScriptRecipe{Script: scenario.Script{Name: "delivery-expiry", Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: 90}, Steps: []scenario.Step{
		{Name: "send", Do: sendBody(facts, "sender", "receiver", "The queue deadline must not silently discard this body.")},
		scenario.Advance("queue-deadline", delivery.DefaultEndpointMaxQueue+time.Minute),
	}}, Drain: func(e scenario.Env) error {
		if err := facts.RecvAndAck(e, e.Agent("receiver")); err != nil {
			return err
		}
		return feedback.run(e, facts, "sender")
	}, Check: facts.Check}
}

func redeliveryRecipe(b Bindings) ScriptRecipe {
	facts := newDeliveryEvidence(b)
	var feedback feedbackDrain
	steps := []scenario.Step{{Name: "send", Do: sendBody(facts, "sender", "receiver", "A lease expires without acknowledgement.")}}
	for i := 0; i < delivery.MaxLeases; i++ {
		steps = append(steps, scenario.Step{Name: fmt.Sprintf("handout-%d", i+1), Do: func(e scenario.Env) error {
			_, err := e.Agent("receiver").Recv(e.Ctx(), fakeagent.RecvInput{Frontend: adapterproto.FrontendCLI})
			return err
		}}, scenario.Advance(fmt.Sprintf("lease-expiry-%d", i+1), delivery.LeaseDuration))
	}
	return ScriptRecipe{Script: scenario.Script{Name: "delivery-redelivery", Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: 180}, Steps: steps}, Drain: func(e scenario.Env) error {
		if err := facts.RecvAndAck(e, e.Agent("receiver")); err != nil {
			return err
		}
		return feedback.run(e, facts, "sender")
	}, Check: facts.Check}
}

// DeliveryOps names recoverable source-write preparation faults.
type DeliveryOps struct {
	FailSourceUpdate     func(scenario.Env) error
	RetainLostSourceBody func(scenario.Env) error
}

var ErrSourceAlreadyConfirmed = errors.New("acceptance: source receipt already committed")

func replyAfterFailureRecipe(b Bindings) ScriptRecipe {
	facts := newDeliveryEvidence(b)
	var feedback feedbackDrain
	var original adapterproto.MessageID
	return ScriptRecipe{Needs: Needs{FSFaults: []FSFault{FaultSourceUpdate}}, Script: scenario.Script{Name: "delivery-reply-after-write-failure", Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: 102}, Steps: []scenario.Step{
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
		return feedback.run(e, facts, "sender")
	}, Check: func(e scenario.Env) error {
		if err := facts.Check(e); err != nil {
			return err
		}
		return b.Recovery.CheckClosedRecords(e)
	}}
}

func unreachableRecipe(b Bindings) ScriptRecipe {
	facts := newDeliveryEvidence(b)
	var feedback feedbackDrain
	var sent, advanced bool
	deadline := scenario.Step{Name: "report-deadline", Do: func(e scenario.Env) error {
		if !advanced {
			advanced = true
			e.Advance(4 * time.Hour)
		}
		return nil
	}}
	return ScriptRecipe{Script: scenario.Script{Name: "delivery-unreachable", Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: 66}, Steps: []scenario.Step{
		{Name: "send-to-an-absent-node", Do: func(e scenario.Env) error {
			target, err := absentNodeAddress(e.Addr("receiver"))
			if err != nil {
				return err
			}
			r, err := e.Agent("sender").Send(e.Ctx(), fakeagent.SendInput{To: []string{target}, Body: "An unreachable node must produce real deadline feedback."})
			if err == nil {
				facts.Accepted(r, "An unreachable node must produce real deadline feedback.")
				facts.RequireFeedback(r.MessageID, target, delivery.KindExpiredUnconfirmed)
				sent = true
			}
			return err
		}}, deadline,
	}}, Drain: func(e scenario.Env) error {
		if sent {
			if err := deadline.Do(e); err != nil {
				return err
			}
			if err := e.Quiesce(); err != nil {
				return err
			}
		}
		return feedback.run(e, facts, "sender")
	}, Check: facts.Check}
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
