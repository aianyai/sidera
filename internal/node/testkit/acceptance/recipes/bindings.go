package recipes

import (
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

// Bindings supplies the actual node identity and domain operations missing from scenario.Env.
// Each domain adds named, typed callbacks when its recipes need them; callbacks
// must resolve the current process on every invocation, including after restart.
// An Env operation is used directly rather than wrapped in a duplicate callback.
type Bindings struct {
	// LocalNode returns the binding's actual node identity without retaining process handles.
	LocalNode    func(scenario.Env) (ident.NodeID, error)
	Delivery     DeliveryOps
	Sessions     SessionOps
	Installation InstallOps
	Recovery     RecoveryOps
	Management   ManagementOps
	Maintenance  MaintenanceOps
}
