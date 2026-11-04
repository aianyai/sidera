package engine

import (
	"context"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// InstalledAdapter is an adapter package that is installed, at the version that is
// current.
type InstalledAdapter struct {
	ID      string
	Version string
	// Fault is why the package cannot run, as the configuration found it when it was
	// loaded: empty when the package is good. It is a fact of the package, so it is
	// there whether or not an instance is enabled; the name is one of
	// adapterhost.GoneCause.
	Fault string
}

// InstanceView is one adapter instance of the configuration as the owner sees it.
type InstanceView struct {
	Name    string
	Adapter string
	Enabled bool
	// Probe is how far the probe of an enabled instance has come; it is the zero
	// value for an instance that is not enabled, which runs nothing to probe.
	Probe ProbeView
	// Fault is why the package of an enabled instance cannot run, as the adapter
	// host last reported it: empty when the instance is online, has not been
	// reported, or was last lost for the sake of one connection. The names are those
	// of adapterhost.GoneCause.
	Fault string
	// Availability is what the adapter of an enabled, online instance last said keeps
	// something from working (adapter.availability), empty when it said nothing or
	// has said everything is well. It is the adapter's word and describes a
	// connection, so it is empty for an instance that is offline: that is a fact of
	// its own, which the snapshot of the connection that ended cannot show.
	Availability []adapterproto.Availability
}

// AdapterList is what the owner is shown of the adapters.
type AdapterList struct {
	// Version is the version of the instances: the one a change of them names.
	Version admin.Version
	// Adapters are the installed packages by identifier, and Instances the
	// configured instances by name.
	Adapters  []InstalledAdapter
	Instances []InstanceView
}

type cmdAdapterList struct{ call[AdapterList] }

func (c *cmdAdapterList) gate() gate                   { return gateRead }
func (c *cmdAdapterList) run(e *Engine, now time.Time) { c.done(e.adapterList(now), nil) }

func (e *Engine) adapterList(now time.Time) AdapterList {
	instances := e.d.Config.Instances()
	list := AdapterList{Version: instances.Version}
	for _, a := range e.d.Config.Adapters() {
		ia := InstalledAdapter{ID: a.ID, Version: a.Version}
		if a.Fault != nil {
			ia.Fault = adapterhost.GonePackageInvalid.String()
		}
		list.Adapters = append(list.Adapters, ia)
	}
	sort.Slice(list.Adapters, func(i, j int) bool { return list.Adapters[i].ID < list.Adapters[j].ID })
	for _, name := range sortedKeys(instances.Items) {
		in := instances.Items[name]
		v := InstanceView{Name: name, Adapter: in.Adapter, Enabled: in.Enabled}
		if in.Enabled {
			v.Probe = ProbeView{State: ProbePending}
			if st := e.adapters[name]; st != nil {
				v.Probe = st.probeView(now)
				if st.fault != 0 {
					v.Fault = st.fault.String()
				}
				if st.online {
					v.Availability = cloneAvailability(st.snapshot)
				}
			}
		}
		list.Instances = append(list.Instances, v)
	}
	return list
}

// AdapterList is the installed adapters, the instances and the state of the probe
// of each enabled one, from the loop's state at one command.
func (e *Engine) AdapterList(ctx context.Context) (AdapterList, error) {
	c := &cmdAdapterList{call: newCall[AdapterList]()}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}

type cmdPresets struct{ call[config.Presets] }

func (c *cmdPresets) gate() gate                 { return gateRead }
func (c *cmdPresets) run(e *Engine, _ time.Time) { c.done(e.d.Config.Presets(), nil) }

// Presets is presets.json as the loop sees it: the presets, and the version a
// change of them has to name.
func (e *Engine) Presets(ctx context.Context) (config.Presets, error) {
	c := &cmdPresets{call: newCall[config.Presets]()}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}
