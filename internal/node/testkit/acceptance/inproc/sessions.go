package inproc

import (
	"fmt"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func newSessionOps() recipes.SessionOps {
	return recipes.SessionOps{
		RuntimeAddr: func(e scenario.Env, instance string) string { return e.(*harness.H).RuntimeAddrOf(instance) },
		Session:     func(e scenario.Env, endpoint string) fakeagent.Agent { return e.(*harness.H).Session(endpoint) },
		CheckpointPut: func(e scenario.Env, instance string, p adapterproto.CheckpointPutParams) error {
			adapter, err := e.Adapter(instance)
			if err != nil {
				return err
			}
			return adapter.CheckpointPut(e.Ctx(), p)
		},
		FailCreateSettlement: func(e scenario.Env) error {
			e.(*harness.H).Faults().Fail(fsxtest.Match{Tx: "control-result", Role: "ctl", Step: fsx.StepCreate, Hit: 1}, fsxtest.DiskFull)
			return nil
		},
		HasUnknownCreationAlert: func(e scenario.Env, endpoint string) (bool, error) {
			scan, err := e.(*harness.H).Records()
			if err != nil {
				return false, err
			}
			count := 0
			for _, alert := range scan.Alerts {
				if alert.Kind == "control_unknown" && alert.Subject == endpoint {
					count++
				}
			}
			return count == 1, nil
		},
		CheckClosedSession: func(e scenario.Env, endpoint string) error { return checkClosedSession(e.(*harness.H), endpoint) },
	}
}

func checkClosedSession(h *harness.H, endpoint string) error {
	scan, err := h.Records()
	if err != nil {
		return err
	}
	found := false
	for _, ep := range scan.Endpoints {
		if ep.ID.String() == endpoint {
			found = true
			if ep.Status != store.EndpointClosed || len(ep.Token) != 0 {
				return fmt.Errorf("session %s is not closed with an empty token", endpoint)
			}
		}
	}
	if !found {
		return fmt.Errorf("closed session %s is missing before retention", endpoint)
	}
	for _, cp := range scan.Checkpoints {
		if cp.Key.Scope == store.CheckpointScopeSession && cp.Key.ID == endpoint {
			return fmt.Errorf("closed session %s retains a checkpoint", endpoint)
		}
	}
	return nil
}
