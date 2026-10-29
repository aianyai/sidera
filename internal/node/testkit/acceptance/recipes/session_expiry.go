package recipes

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func controlWriteExpiryRecipe(b Bindings) ScriptRecipe {
	var message adapterproto.MessageID
	var endpoint string
	facts := &DeliveryEvidence{}
	return ScriptRecipe{Script: scenario.Script{Name: "session-control-write-expiry", Setup: scenario.Setup{Direct: []string{"sender"}}, Crash: scenario.CrashPlan{MaxPoints: 89}, Steps: []scenario.Step{
		{Name: "fail-create-settlement", Do: b.Sessions.FailCreateSettlement},
		{Name: "hold-create", Do: func(e scenario.Env) error {
			c, err := e.Adapter("chat")
			if err != nil {
				return err
			}
			return c.SetOutcome(e.Ctx(), fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeHold})
		}},
		{Name: "create", Do: func(e scenario.Env) error {
			r, err := e.Agent("sender").Control(e.Ctx(), fakeagent.ControlInput{To: b.Sessions.RuntimeAddr(e, "chat"), Signal: adapterproto.SignalSessionCreate, Preset: "default"})
			if err == nil {
				message = r.MessageID
			}
			return err
		}}, scenario.Advance("control-report-deadline", 4*time.Hour),
	}}, Drain: func(e scenario.Env) error {
		log, err := e.Ledger()
		if err != nil {
			return err
		}
		for _, entry := range log.Kind(ledger.KindSessionCreateReceived) {
			endpoint = entry.Attrs.Endpoint
		}
		if endpoint != "" {
			id, err := ident.ParseEndpointID(endpoint)
			if err != nil {
				return err
			}
			runtime, err := ident.ParseAddress(b.Sessions.RuntimeAddr(e, "chat"))
			if err != nil {
				return err
			}
			address := ident.Address{Node: runtime.Node, Endpoint: id}.String()
			// A control closed before its reply can settle cannot settle itself later.
			// The owner closes the alerted endpoint through the real management entry.
			result, err := b.Management.CloseEndpoint(e, ManagementRequest{ID: admin.OperationID{90}}, address)
			if err != nil {
				return err
			}
			if result.Code != "" || result.Value != "closing" {
				return fmt.Errorf("owner could not close the uncontrolled endpoint: %+v", result)
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
		return drainFeedback(e, facts, "sender")
	}, Check: func(e scenario.Env) error {
		if message == "" {
			return b.Recovery.CheckClosedRecords(e)
		}
		status, err := e.Agent("sender").Status(e.Ctx(), fakeagent.StatusInput{MessageID: message})
		if err != nil {
			return err
		}
		if status.ControlReply != nil || len(status.Recipients) != 1 || status.Recipients[0].State != adapterproto.DeliveryClosedUnconfirmed {
			return fmt.Errorf("control whose reply could not commit has an unexpected result: %+v", status)
		}
		facts.RequireFeedback(message, b.Sessions.RuntimeAddr(e, "chat"), delivery.KindAcceptedUnconfirmed)
		if err := facts.Check(e); err != nil {
			return err
		}
		if endpoint == "" {
			return fmt.Errorf("closed control has no real adapter creation request")
		}
		if err := b.Sessions.CheckClosedSession(e, endpoint); err != nil {
			return err
		}
		alerted, err := b.Sessions.HasUnknownCreationAlert(e, endpoint)
		if err != nil {
			return err
		}
		if !alerted {
			return fmt.Errorf("uncontrolled creation %s has no unique owner alert", endpoint)
		}
		log, err := e.Ledger()
		if err != nil {
			return err
		}
		if len(log.Kind(ledger.KindRuntimeSessionCreated)) != 0 {
			return fmt.Errorf("held expired creation unexpectedly made a native session")
		}
		return b.Recovery.CheckClosedRecords(e)
	}}
}
