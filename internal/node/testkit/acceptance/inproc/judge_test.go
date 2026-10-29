package inproc

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// alteredRecv changes only the evidence supplied to the acceptance judgement;
// the underlying rejection, notice formation and receive are all real calls.
type alteredRecv struct {
	fakeagent.Agent
	change func(*adapterproto.PageMessage)
}

func (a alteredRecv) Recv(ctx context.Context, in fakeagent.RecvInput) (fakeagent.RecvResult, error) {
	r, err := a.Agent.Recv(ctx, in)
	for i := range r.Page.Messages {
		a.change(&r.Page.Messages[i])
	}
	return r, err
}

func TestFeedbackJudgementRequiresTheRealMessageRecipientAndSystemSource(t *testing.T) {
	for _, name := range []string{"unchanged", "wrong-message", "wrong-recipient", "non-system-source", "bad-header"} {
		t.Run(name, func(t *testing.T) {
			h := harness.New(t, harness.Options{})
			h.MustDirect("sender")
			h.MustDirect("receiver")
			paused := true
			if err := h.Agent("receiver").Declare(h.Ctx(), fakeagent.DeclareInput{Paused: &paused}); err != nil {
				t.Fatal(err)
			}
			const body = "The receiver refuses this actual body."
			r, err := h.Agent("sender").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("receiver")}, Body: body})
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				h.Advance(time.Minute)
				if err := h.Quiesce(); err != nil {
					t.Fatal(err)
				}
			}
			facts := &recipes.DeliveryEvidence{}
			facts.Accepted(r, body)
			a := alteredRecv{Agent: h.Agent("sender"), change: func(m *adapterproto.PageMessage) {
				switch name {
				case "wrong-message":
					m.Body = strings.ReplaceAll(m.Body, string(r.MessageID), "another-message")
				case "wrong-recipient":
					m.Body = strings.ReplaceAll(m.Body, h.Addr("receiver"), h.Addr("sender"))
				case "non-system-source":
					m.From = adapterproto.Address(h.Addr("receiver"))
				case "bad-header":
					m.Body = "A notice without a channel header."
				}
			}}
			err = facts.RecvAndAck(h, a)
			if err == nil {
				err = facts.Check(h)
			}
			if name == "unchanged" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("altered evidence falsely discharged the accepted-body obligation")
			}
		})
	}
}

func TestCreationJudgementRequiresTheRequestedInitialBody(t *testing.T) {
	const actualBody = "The native session really reads this initial body."
	for _, tc := range []struct {
		name, expectedBody string
		wantRejected       bool
	}{
		{"matching-body", actualBody, false},
		{"different-body", "The requested initial body was different.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := &recipes.DeliveryEvidence{}
			var message adapterproto.MessageID
			s := &harness.WalkScenario{
				Options: harness.Options{Adapter: &harness.AdapterOptions{Runtime: true}},
				Script: scenario.Script{Name: "creation-body-evidence", Setup: scenario.Setup{Direct: []string{"sender"}}, Steps: []scenario.Step{
					{Name: "create", Do: func(e scenario.Env) error {
						r, err := e.Agent("sender").Control(e.Ctx(), fakeagent.ControlInput{To: newSessionOps().RuntimeAddr(e, "chat"), Signal: adapterproto.SignalSessionCreate, Preset: "default", Body: actualBody})
						if err == nil {
							message = r.MessageID
							facts.AcceptedCreation(r, tc.expectedBody, "sender")
						}
						return err
					}},
					scenario.Advance("creation-pull", time.Minute),
				}},
				Drain: func(h *harness.H) error {
					status, err := h.Agent("sender").Status(h.Ctx(), fakeagent.StatusInput{MessageID: message})
					if err != nil {
						return err
					}
					if status.ControlReply == nil || status.ControlReply.Create == nil || status.ControlReply.Create.Outcome != adapterproto.CreateReplyCreated {
						return fmt.Errorf("creation has no actual created reply: %+v", status)
					}
					endpoint := string(status.ControlReply.Create.SessionEndpoint)
					if err := facts.RecvAndAck(h, h.Session(endpoint)); err != nil {
						return err
					}
					log, err := h.Ledger()
					if err != nil {
						return err
					}
					for _, seen := range log.Kind(ledger.KindModelSaw) {
						if seen.Attrs.Endpoint == endpoint && seen.Attrs.MessageID == string(message) && seen.Attrs.BodyDigest == ledger.Digest([]byte(actualBody)) {
							return nil
						}
					}
					return fmt.Errorf("created session did not read its actual initial body")
				},
				Check: func(h *harness.H) error { return facts.Check(h) },
			}
			r, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Record: true})
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantRejected {
				if len(r.Violations) != 1 || !strings.Contains(r.Violations[0], "has no visible body") {
					t.Fatalf("different initial body: violations=%v", r.Violations)
				}
			} else if len(r.Violations) != 0 {
				t.Fatalf("actual initial body: violations=%v", r.Violations)
			}
		})
	}
}
