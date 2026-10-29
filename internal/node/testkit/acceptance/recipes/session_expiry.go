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
	var result fakeagent.SendResult
	var endpoint, native string
	var deadlineAdvanced, deadlineCompleted, creationPulled, createdBeforeDeadline, closeRequested bool
	facts := newDeliveryEvidence(b)
	advanceDeadline := func(e scenario.Env) error {
		if !deadlineAdvanced {
			deadlineAdvanced = true
			e.Advance(4 * time.Hour)
		}
		return e.Quiesce()
	}
	deadline := scenario.Step{Name: "control-report-deadline", Do: func(e scenario.Env) error {
		if err := advanceDeadline(e); err != nil {
			return err
		}
		deadlineCompleted = true
		return nil
	}}
	return ScriptRecipe{Needs: Needs{FSFaults: []FSFault{FaultCreateSettlement}}, Script: scenario.Script{Name: "session-control-write-expiry", Setup: scenario.Setup{Direct: []string{"sender"}}, Crash: scenario.CrashPlan{MaxPoints: 107}, Steps: []scenario.Step{
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
				message, result = r.MessageID, r
			}
			return err
		}}, deadline,
	}}, Drain: func(e scenario.Env) error {
		if message != "" && !deadlineAdvanced && !createdBeforeDeadline {
			if !creationPulled {
				creationPulled = true
				e.Advance(time.Minute)
			}
			if err := e.Quiesce(); err != nil {
				return err
			}
			creation, err := readCreation(e, "sender", b.Sessions.RuntimeAddr(e, "chat"), message)
			if err != nil {
				return err
			}
			// A restart can finish creation before the held request's deadline.
			// Retain its original control result and prove that actual success.
			if creation.status.ControlReply != nil && creation.status.ControlReply.Create.Outcome == adapterproto.CreateReplyCreated {
				createdBeforeDeadline = true
				endpoint, native = creation.endpoint, creation.native
				facts.AcceptedCreation(result, "", "sender")
			}
		}
		if message != "" && !createdBeforeDeadline {
			if err := advanceDeadline(e); err != nil {
				return err
			}
		}
		if createdBeforeDeadline && !closeRequested {
			if err := facts.RecvAndAck(e, b.Sessions.Session(e, endpoint)); err != nil {
				return err
			}
		}
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
			// Finish cleanup through management even if recovery already closed it
			// or a previous drain committed the close before recording its result.
			closeRequested = true
			result, err := b.Management.CloseEndpoint(e, ManagementRequest{ID: admin.OperationID{90}}, address)
			if err != nil {
				return err
			}
			if result.Code != "" || (result.Value != "closing" && result.Value != "already_closed") {
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
		if createdBeforeDeadline {
			if err := facts.Check(e); err != nil {
				return err
			}
			log, err := e.Ledger()
			if err != nil {
				return err
			}
			released := false
			for _, closed := range log.Kind(ledger.KindRuntimeClosed) {
				if closed.Writer == "runtime/"+native {
					released = true
				}
			}
			if !released {
				return fmt.Errorf("created native session %s was not really closed", native)
			}
			if err := b.Sessions.CheckClosedSession(e, endpoint); err != nil {
				return err
			}
			return b.Recovery.CheckClosedRecords(e)
		}
		if message == "" {
			return b.Recovery.CheckClosedRecords(e)
		}
		creation, err := readCreation(e, "sender", b.Sessions.RuntimeAddr(e, "chat"), message)
		if err != nil {
			return err
		}
		status := creation.status
		if len(status.Recipients) != 1 || string(status.Recipients[0].To) != b.Sessions.RuntimeAddr(e, "chat") {
			return fmt.Errorf("control whose reply could not commit has an unexpected result: %+v", status)
		}
		if status.ControlReply != nil {
			// Before the closing tick commits, restart may retry the failed result
			// write and deliver the precise timeout reply. A completed deadline step
			// still owes the original no-reply result and its actual runtime notice.
			reply := status.ControlReply.Create
			recoveredTimeout := !deadlineCompleted && deadlineAdvanced &&
				status.Recipients[0].State == adapterproto.DeliveryDelivered &&
				reply.Outcome == adapterproto.CreateReplyUnconfirmed &&
				reply.Reason == adapterproto.CreateReasonReadyTimeout && creation.endpoint == endpoint
			if !recoveredTimeout {
				return fmt.Errorf("control whose reply could not commit has an unexpected result: %+v", status)
			}
		} else {
			if status.Recipients[0].State != adapterproto.DeliveryClosedUnconfirmed {
				return fmt.Errorf("control whose reply could not commit has an unexpected result: %+v", status)
			}
			facts.RequireFeedback(message, b.Sessions.RuntimeAddr(e, "chat"), delivery.KindAcceptedUnconfirmed)
		}
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
