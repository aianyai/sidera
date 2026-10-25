package scenario

import (
	"fmt"

	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// RecvAndAckAll is a step body for the agent of an endpoint that takes pages
// until one is empty and acknowledges each page it took, whole. It is what a
// well-behaved reader does, and what a scenario uses to end a delivery.
func RecvAndAckAll(name string) func(Env) error {
	return func(e Env) error {
		a := e.Agent(name)
		for {
			res, err := a.Recv(e.Ctx(), fakeagent.RecvInput{Frontend: adapterproto.FrontendCLI})
			if err != nil {
				return fmt.Errorf("endpoint %q: recv: %w", name, err)
			}
			if len(res.Page.Messages) == 0 {
				return nil
			}
			// No items means every part the lease handed out.
			if _, err := a.Ack(e.Ctx(), fakeagent.AckInput{Lease: res.Page.Lease}); err != nil {
				return fmt.Errorf("endpoint %q: ack of lease %s: %w", name, res.Page.Lease, err)
			}
		}
	}
}
