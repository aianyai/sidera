package fakeruntime

import (
	"context"
	"errors"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
)

// RemoteController is the script's hold on runtime processes: it finds the process
// of a session among the connections of the fixture's control server and turns its
// knobs. It is the Controller of the binary layer, as a Runtime is of the engine
// layer.
type RemoteController struct {
	Server *fakectl.Server
}

var _ Controller = RemoteController{}

// reach bounds how long the controller waits for a runtime process to be there
// and to answer.
const reach = 10 * time.Second

func (c RemoteController) conn(ctx context.Context, ref string) (*fakectl.Conn, error) {
	conn, err := c.Server.Wait(ctx, ref)
	if err != nil {
		return nil, errors.Join(ErrUnknownSession, err)
	}
	return conn, nil
}

func (c RemoteController) knob(ref, name string, args, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), reach)
	defer cancel()
	conn, err := c.conn(ctx, ref)
	if err != nil {
		return err
	}
	return remoteError(conn.Knob(ctx, name, args, result))
}

// SetPlan sets the plan of the session's next turn.
func (c RemoteController) SetPlan(ref string, plan TurnPlan) error {
	if err := plan.validate(); err != nil {
		return err
	}
	return c.knob(ref, KnobPlan, PlanKnob{ManualStart: plan.ManualStart, Actions: plan.Actions, EndAfter: plan.EndAfter}, nil)
}

// StartTurn starts a turn.
func (c RemoteController) StartTurn(ref string) error { return c.knob(ref, KnobTurnStart, nil, nil) }

// EndTurn ends the running turn.
func (c RemoteController) EndTurn(ref, final string) error {
	return c.knob(ref, KnobTurnEnd, TurnEndKnob{Final: final}, nil)
}

// KillNative ends the runtime process of the session. The process ends its
// connection rather than answer, so a connection that ended after the request
// counts as the answer.
func (c RemoteController) KillNative(ref string) error {
	ctx, cancel := context.WithTimeout(context.Background(), reach)
	defer cancel()
	conn, err := c.conn(ctx, ref)
	if err != nil {
		return err
	}
	err = remoteError(conn.Knob(ctx, KnobKill, nil, nil))
	select {
	case <-conn.Done():
		return nil
	default:
	}
	return err
}

// Idle is true when every runtime process that is connected has no turn action
// running or waiting. A process that cannot be asked counts as not idle.
func (c RemoteController) Idle() bool {
	for _, conn := range c.Server.Conns() {
		if conn.Hello().Role != fakectl.RoleRuntime {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), reach)
		var r IdleReply
		err := conn.Knob(ctx, KnobIdle, nil, &r)
		cancel()
		if err != nil || !r.Idle {
			return false
		}
	}
	return true
}
