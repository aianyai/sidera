package acceptance

import (
	"context"
	"fmt"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/disk"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func newSessionOps() recipes.SessionOps {
	return recipes.SessionOps{
		RuntimeAddr:             runtimeAddress,
		Session:                 sessionAgentFor,
		CheckpointPut:           checkpointPut,
		FailCreateSettlement:    failCreateSettlement,
		HasUnknownCreationAlert: hasUnknownCreationAlert,
		CheckClosedSession:      checkClosedSession,
	}
}

func runtimeAddress(e scenario.Env, instance string) string {
	address := envFrom(e).fixture.runtimeAddresses[instance]
	if address == "" {
		panic(fmt.Sprintf("acceptance: no runtime address bound for %s", instance))
	}
	return address
}

// bindRuntimeAddresses retains publicly observed identities across daemon
// generations and instance disablement. An address does not prove openness.
func (f *Fixture) bindRuntimeAddresses(ctx context.Context) error {
	raw, err := f.Manage(ctx, "endpoints.list", manageapi.Request{})
	if err != nil {
		return err
	}
	var listed struct {
		Endpoints []struct{ Address, Kind, Instance string } `json:"endpoints"`
	}
	if err := resultBody(raw, &listed); err != nil {
		return err
	}
	observed := map[string]string{}
	for _, ep := range listed.Endpoints {
		if ep.Kind != "runtime" {
			continue
		}
		if ep.Instance == "" || ep.Address == "" {
			return fmt.Errorf("acceptance: runtime endpoint has no instance or address")
		}
		if _, exists := observed[ep.Instance]; exists {
			return fmt.Errorf("acceptance: multiple runtime endpoints for %s", ep.Instance)
		}
		if known, exists := f.runtimeAddresses[ep.Instance]; exists && known != ep.Address {
			return fmt.Errorf("acceptance: runtime address changed for %s: %s to %s", ep.Instance, known, ep.Address)
		}
		observed[ep.Instance] = ep.Address
	}
	if f.runtimeAddresses == nil {
		f.runtimeAddresses = map[string]string{}
	}
	for instance, address := range observed {
		f.runtimeAddresses[instance] = address
	}
	return nil
}

func checkpointPut(e scenario.Env, instance string, p adapterproto.CheckpointPutParams) error {
	ctl, err := e.Adapter(instance)
	if err != nil {
		return err
	}
	return ctl.CheckpointPut(e.Ctx(), p)
}

func failCreateSettlement(e scenario.Env) error {
	return unavailableFilesystemFault(e, recipes.FaultCreateSettlement)
}

func hasUnknownCreationAlert(e scenario.Env, endpoint string) (bool, error) {
	env := envFrom(e)
	if err := env.check(); err != nil {
		return false, err
	}
	raw, err := env.fixture.Manage(e.Ctx(), "alerts.list", manageapi.Request{Params: map[string]any{"include_acknowledged": true}})
	if err != nil {
		return false, err
	}
	var listed struct {
		Alerts []struct{ Kind, Subject string } `json:"alerts"`
	}
	if err := resultBody(raw, &listed); err != nil {
		return false, err
	}
	count := 0
	for _, alert := range listed.Alerts {
		if alert.Kind == "control_unknown" && alert.Subject == endpoint {
			count++
		}
	}
	return count == 1, nil
}

func checkClosedSession(e scenario.Env, endpoint string) error {
	scan, _, err := readSessionRecords(e)
	if err != nil {
		return err
	}
	return disk.CheckClosedSession(scan, endpoint)
}

// readSessionRecords uses the store's decoder on the real disk after the caller
// has quiesced the node. The temporary store is never recovered or written.
func readSessionRecords(e scenario.Env) (*store.Scan, fsx.Reader, error) {
	env := envFrom(e)
	if err := env.check(); err != nil {
		return nil, nil, err
	}
	f := env.fixture
	if f.current == nil || f.current.p.exited() {
		return nil, nil, scenario.NotAvailable("session inspection requires a running daemon")
	}
	fs, err := diskFilesystem(e)
	if err != nil {
		return nil, nil, err
	}
	w := txn.New(fs, f.table, nil)
	scan, err := store.New(w).Scan()
	return scan, w.Reader(), err
}
