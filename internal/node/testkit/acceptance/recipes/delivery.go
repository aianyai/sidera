package recipes

import (
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func deliveryRecipes(_ Bindings) []ScriptRecipe {
	const body = "A real body must survive acceptance."
	var message adapterproto.MessageID
	return []ScriptRecipe{{
		Script: scenario.Script{Name: "delivery-send", Setup: scenario.Setup{Direct: []string{"sender", "receiver"}}, Crash: scenario.CrashPlan{MaxPoints: 42}, Steps: []scenario.Step{
			{Name: "send", Do: func(e scenario.Env) error {
				result, err := e.Agent("sender").Send(e.Ctx(), fakeagent.SendInput{To: []string{e.Addr("receiver")}, Body: body})
				if err == nil {
					message = result.MessageID
				}
				return err
			}},
		}},
		Drain: scenario.RecvAndAckAll("receiver"),
		Check: func(e scenario.Env) error { return checkAcceptedBody(e, message, body) },
	}}
}
