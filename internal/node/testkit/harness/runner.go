package harness

import (
	"context"
	"fmt"
	"strconv"

	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent/engineagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// engineRunner carries out the actions a turn of a fake runtime takes, as the model of
// a real runtime reaches the channel through its tools: it calls the engine as the
// session endpoint the turn belongs to, with the token the session was created with.
// What the runtime reports to its adapter as the tool result is the page this returns,
// so the adapter acknowledges on the agent's behalf for the page the model was shown.
//
// The agent it uses is not recorded as a fixture agent is: the runtime writes what the
// model saw to the ledger itself, and two writers would count each message twice.
//
// The arguments of an action are strings: a receive reads "frontend" (cli by default),
// "wait_s" and "ack_lease"; a receive of one part reads "msg" and "part"; an
// acknowledgement reads "lease".
type engineRunner struct{ h *H }

var _ fakeruntime.ActionRunner = engineRunner{}

func (r engineRunner) RunAction(ctx context.Context, session fakeruntime.SessionInfo, a fakeruntime.TurnAction) (fakeruntime.ActionResult, error) {
	token := session.Env[engine.TokenEnv]
	if token == "" {
		return fakeruntime.ActionResult{}, fmt.Errorf("harness: the session %s has no token in its tool access", session.Endpoint)
	}
	agent := engineagent.New(currentEngine{r.h}, engine.ByToken(adapterproto.Token(token)))
	switch a.Kind {
	case fakeruntime.ActionRecv, fakeruntime.ActionRecvPart:
		in, err := recvInput(a)
		if err != nil {
			return fakeruntime.ActionResult{}, err
		}
		res, err := agent.Recv(ctx, in)
		return fakeruntime.ActionResult{Output: res.Raw}, err
	case fakeruntime.ActionAck:
		_, err := agent.Ack(ctx, fakeagent.AckInput{Lease: adapterproto.LeaseRef(a.Args["lease"])})
		return fakeruntime.ActionResult{}, err
	}
	return fakeruntime.ActionResult{}, fmt.Errorf("harness: the engine runner does not carry out %q actions", a.Kind)
}

func recvInput(a fakeruntime.TurnAction) (fakeagent.RecvInput, error) {
	in := fakeagent.RecvInput{
		Frontend: adapterproto.FrontendCLI, Msg: adapterproto.MessageID(a.Args["msg"]), AckLease: adapterproto.LeaseRef(a.Args["ack_lease"]),
	}
	if f := a.Args["frontend"]; f != "" {
		in.Frontend = adapterproto.Frontend(f)
	}
	for name, dst := range map[string]*int{"wait_s": &in.WaitS, "part": &in.Part} {
		if v := a.Args[name]; v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return in, fmt.Errorf("harness: the %s of a receive is not a number: %w", name, err)
			}
			*dst = n
		}
	}
	return in, nil
}
