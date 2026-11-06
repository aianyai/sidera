package engine

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/config"
)

// AdapterSetupInfo is what the core CLI needs to run the client setup of one adapter
// instance.
type AdapterSetupInfo struct {
	Instance string
	Adapter  string
	// StateDir is the absolute path of the directory the instance's adapter keeps its
	// state in.
	StateDir string
	// Config is the values the owner set for the instance, the object adapter.start
	// passes as config: never nil. The manifest's field defaults are not applied; the
	// setup program applies them as the adapter does.
	Config map[string]json.RawMessage
	// SetupArgv is the program the installed package declares as its setup, as an
	// absolute path inside the package of the installed version and revision the
	// adapter's pointer names (the one the instance runs, or will run once restarted),
	// followed by the manifest's arguments; nil when the package declares none.
	SetupArgv []string
}

type cmdAdapterSetupInfo struct {
	call[AdapterSetupInfo]
	instance string
}

func (c *cmdAdapterSetupInfo) gate() gate { return gateRead }
func (c *cmdAdapterSetupInfo) run(e *Engine, _ time.Time) {
	c.done(e.adapterSetupInfo(c.instance))
}

func (e *Engine) adapterSetupInfo(instance string) (AdapterSetupInfo, error) {
	in, ok := e.d.Config.Instances().Items[instance]
	if !ok {
		return AdapterSetupInfo{}, refuse(ReasonInstanceUnknown)
	}
	for _, a := range e.d.Config.Adapters() {
		if a.ID != in.Adapter {
			continue
		}
		// A package that could not be loaded has no manifest: what it declares is not
		// known, which is not the same as declaring no setup.
		if a.Fault != nil {
			return AdapterSetupInfo{}, refuse(ReasonPackageInvalid)
		}
		info := AdapterSetupInfo{
			Instance: instance,
			Adapter:  in.Adapter,
			StateDir: adapterhost.StateDir(e.d.Home, instance),
			Config:   in.Config,
		}
		if info.Config == nil {
			info.Config = map[string]json.RawMessage{}
		}
		if setup := a.Manifest.Setup; setup != nil {
			program, args := adapterhost.Command(adapterhost.PackageDir(e.d.Home, a), *setup)
			info.SetupArgv = append([]string{program}, args...)
		}
		return info, nil
	}
	// A ChangeError because apiwire maps this reason to not_found/adapter_unknown, the
	// code the other adapters.* methods answer for an adapter that is not installed.
	return AdapterSetupInfo{}, &config.ChangeError{Reason: config.ChangeAdapterNotInstalled, Field: in.Adapter}
}

// AdapterSetupInfo is what runs the client setup of an adapter instance, from the
// loop's state at one command: the instance is refused when it is not there, when
// its adapter is not installed and when the installed package cannot be loaded.
func (e *Engine) AdapterSetupInfo(ctx context.Context, instance string) (AdapterSetupInfo, error) {
	c := &cmdAdapterSetupInfo{call: newCall[AdapterSetupInfo](), instance: instance}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}
