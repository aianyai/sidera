package inproc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
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
