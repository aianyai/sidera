package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The owner's operations on adapters: installing a package, enabling an instance
// of an installed adapter, disabling an instance. Each comes through the one door
// of manage.go, and its result is kept as that door says: the installation under
// state/admin/ with its effect, the other two in instances.json in the replacement
// that changes it.

// AdapterInstalled is what an installation did, for the result to say.
type AdapterInstalled struct {
	ID      string
	Version string
	// AlreadyCurrent: the package was the installed one (the same version with the
	// same contents), and nothing was done but storing the result.
	AlreadyCurrent bool
	// Replaced: the package replaced one that was installed, another version or the
	// same version with other contents. Neither this nor AlreadyCurrent: the adapter
	// was not installed before.
	Replaced bool
}

// InstallAdapterOp is the operation that installs the adapter package in the
// directory the owner names, or replaces the installed one with it: another version
// (an upgrade), or the same version with other contents (a rebuilt package that kept
// its version). The package is copied outside the loop into a directory next to the
// versions of its adapter, and
// the copy is what is parsed and checked (the manifest, and that the package runs
// on this platform), so that what is installed is what was checked and not what the
// source may have become; the loop only renames the copy into place and makes it
// current. A request that was done before is answered without reading the package.
// A package that replaces one that enabled instances run is installed under a
// directory of its own and the instances are restarted after the commit, as a change
// of the presets restarts them, to run the new files; their session endpoints stay
// open, and the new adapter takes the sessions back as any restarted adapter does.
// The files they ran from, and those the process of a disabled instance that is still
// ending runs from, are held and not discarded until the repair at the next start, so
// nothing is refused for an instance. Contents that are the same (by the digest staging
// computed) are installed already and only the result is stored. encode makes the
// result; it runs in the loop and must not wait.
func InstallAdapterOp(source string, encode func(AdapterInstalled) ([]byte, error)) Operation {
	return &installAdapterOp{source: source, encode: encode}
}

type installAdapterOp struct {
	source string
	encode func(AdapterInstalled) ([]byte, error)

	// What prepare leaves for run and settle. They are written by the install
	// worker before the loop is handed the command and read by the loop, and read
	// by the worker after the loop answered, so each handover orders them.
	staged    config.Staged
	stageErr  error
	committed bool // the staged directory was renamed into the versions
}

func (o *installAdapterOp) Admin() admin.Operation {
	return admin.Operation{Kind: admin.OpInstallAdapter}
}
func (o *installAdapterOp) ends() Ending { return EndingNone }
func (o *installAdapterOp) alone() bool  { return true }

func (o *installAdapterOp) prepare(_ context.Context, e *Engine, rnd io.Reader) {
	o.staged, o.stageErr = e.stagePackage(o.source, rnd)
}

// stagePackage copies the package into the staging directory of its adapter and
// judges the copy. A package that is refused is dropped again, so that a refusal
// leaves nothing.
func (e *Engine) stagePackage(source string, rnd io.Reader) (config.Staged, error) {
	if e.d.OpenPackage == nil {
		return config.Staged{}, errors.New("engine: Deps.OpenPackage is not set, and the engine cannot read a package")
	}
	src, err := e.d.OpenPackage(source)
	if err != nil {
		return config.Staged{}, &Refusal{Reason: ReasonInvalidArgument, Field: "source", Detail: DetailSourceUnreadable, Err: err}
	}
	staged, err := e.d.Config.StageAdapter(src, rnd)
	var unreadable *config.SourceError
	switch {
	case errors.As(err, &unreadable):
		return config.Staged{}, &Refusal{Reason: ReasonInvalidArgument, Field: "source", Detail: DetailSourceUnreadable, Path: unreadable.Path, Err: err}
	case err != nil:
		return config.Staged{}, err
	}
	if !adapterreg.RunsOn(staged.Manifest, runtime.GOOS, runtime.GOARCH) {
		e.unstage(staged)
		return config.Staged{}, refuse(ReasonPlatformUnsupported)
	}
	return staged, nil
}

func (e *Engine) unstage(s config.Staged) {
	if err := e.d.Config.Unstage(s); err != nil {
		// What is left, the next start removes.
		e.log.Warn("the staging of an adapter package could not be dropped", "adapter", s.ID, "version", s.Version, "error", err.Error())
	}
}

// instanceStopping says whether the adapter host still has a process of the
// instance that was stopped, which is what PlanInstall asks before it lets a
// version be discarded.
func (e *Engine) instanceStopping(instance string) bool {
	return e.d.Adapters != nil && e.d.Adapters.Stopping(instance)
}

func (o *installAdapterOp) run(e *Engine, x *opRun) ([]byte, error) {
	if o.stageErr != nil {
		return nil, o.stageErr
	}
	plan, err := e.d.Config.PlanInstall(o.staged, e.instanceStopping)
	if err != nil {
		return nil, err
	}
	// Only now is it known whether the installation restarts enabled instances on
	// new code, which the interface that authorized the request could not tell. The
	// list is empty because what the catalogue asks for then is the local owner,
	// whom no list level stands in for.
	if plan.Replaced && len(e.enabledInstancesOf(o.staged.ID)) > 0 {
		replacing := admin.Operation{Kind: admin.OpInstallAdapter, ReplacesEnabled: true}
		if !admin.Authorize(admin.List{}, x.actor, replacing, x.now).Allowed {
			return nil, refuse(ReasonNotPermitted)
		}
	}
	result, err := o.encode(AdapterInstalled{ID: o.staged.ID, Version: o.staged.Version, AlreadyCurrent: plan.AlreadyCurrent, Replaced: plan.Replaced})
	if err != nil {
		return nil, fmt.Errorf("engine: the result of %s: %w", x.name, err)
	}
	pr, err := store.PrepareAdminResult(x.record(result))
	if err != nil {
		return nil, err
	}
	if plan.AlreadyCurrent {
		tx := e.d.Writer.Begin(&declAdapterInstallCurrent)
		defer tx.Close()
		if err := store.PutAdminResult(tx, store.RoleResult, pr); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return result, nil
	}
	tx := e.d.Writer.Begin(&declAdapterInstall)
	defer tx.Close()
	if err := plan.Promote(tx); err != nil {
		return nil, err
	}
	after, err := plan.Point(tx)
	if err != nil {
		return nil, err
	}
	if err := store.PutAdminResult(tx, store.RoleResult, pr); err != nil {
		return nil, err
	}
	if err := plan.DiscardOld(tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	after()
	o.committed = true
	if plan.Replaced {
		e.restartInstancesOf(x.now, o.staged.ID)
	}
	return result, nil
}

// restartInstancesOf restarts the enabled instances of the adapter, which run the
// package that was installed before, to run the one that is installed now.
func (e *Engine) restartInstancesOf(now time.Time, adapter string) {
	if e.d.Adapters == nil {
		return
	}
	for _, instance := range e.enabledInstancesOf(adapter) {
		e.restartInstance(now, instance)
	}
}

// enabledInstancesOf lists, in order, the enabled instances of the adapter.
func (e *Engine) enabledInstancesOf(adapter string) []string {
	var out []string
	for _, instance := range sortedKeys(e.d.Config.Instances().Items) {
		if in, ok := e.enabledInstance(instance); ok && in.Adapter == adapter {
			out = append(out, instance)
		}
	}
	return out
}

// settle drops the staging when the commit did not take it over (refused, found
// current, a resend that was answered, a failure that a retry may cure). It is not
// called after a fatal failure of the disk, when nothing more is written and the
// next start removes what is left.
func (o *installAdapterOp) settle(e *Engine) {
	if o.staged.Dir == "" || o.committed {
		return
	}
	e.unstage(o.staged)
}

// InstanceChange is what the owner's enabling or disabling of an instance did,
// for the result to say.
type InstanceChange struct {
	Instance string
	// Enabled is whether the instance is enabled after the change.
	Enabled bool
	// Removed is whether the instance is gone after the change.
	Removed bool
	// Version is the version of the instances after the change.
	Version admin.Version
}

func instanceResult(encode func(InstanceChange) ([]byte, error), instance string, x *opRun) config.ResultFunc[config.Instances] {
	return func(after config.Instances) (json.RawMessage, error) {
		in, there := after.Items[instance]
		b, err := encode(InstanceChange{Instance: instance, Enabled: in.Enabled, Removed: !there, Version: after.Version})
		if err != nil {
			return nil, fmt.Errorf("the result of %s: %w", x.name, err)
		}
		return b, nil
	}
}

// endpointOpen says whether the endpoint is open or being created now, which is
// what config needs to know to keep the settings of an endpoint that is.
func (e *Engine) endpointOpen(id ident.EndpointID) bool {
	s := e.view.endpoints[id]
	return s != nil && s.rec.Status != store.EndpointClosed
}

// EnableAdapterOp is the operation that enables the instance of an installed
// adapter, making it if it is new (with the configuration values the owner gives,
// which an instance that exists does not take) and turning it on if it exists. The
// version is that of the instances, checked inside the change with the operation
// and its result written in the same replacement. After the commit the adapter
// host is asked to run the instance, which it does in the background: the owner
// reads whether it came up from the list of the adapters. An instance whose
// endpoints are still being closed from an earlier disabling is not enabled until
// that is done. encode makes the result; it runs in the loop and must not wait.
func EnableAdapterOp(adapter, instance string, instanceConfig []byte, encode func(InstanceChange) ([]byte, error)) Operation {
	return &enableAdapterOp{adapter: adapter, instance: instance, config: instanceConfig, encode: encode}
}

type enableAdapterOp struct {
	adapter, instance string
	config            []byte
	encode            func(InstanceChange) ([]byte, error)
}

func (o *enableAdapterOp) Admin() admin.Operation {
	return admin.Operation{Kind: admin.OpEnableAdapter}
}
func (o *enableAdapterOp) ends() Ending { return EndingNone }
func (o *enableAdapterOp) run(e *Engine, x *opRun) ([]byte, error) {
	if in, ok := e.d.Config.Instances().Items[o.instance]; ok && !in.Enabled {
		// What the owner disabled is still being closed: the endpoints of the
		// instance that were there are closed before the instance is the owner's
		// again, so that the closing never reaches an endpoint of the instance once it
		// is enabled.
		if id, busy := e.instanceClosing(o.instance); busy {
			return nil, &waitForDrain{endpoint: id}
		}
	}
	after, err := e.d.Config.EnableAdapter(x.configRequest(), o.instance, o.adapter, o.config, e.endpointOpen, instanceResult(o.encode, o.instance, x))
	if err != nil {
		return nil, err
	}
	if e.d.Adapters != nil {
		e.d.Adapters.EnsureRunning(o.instance)
	}
	return after.LastOperation.Result, nil
}

// instanceClosing says whether an endpoint of the instance is open or has records
// not closed yet, and names the first by identifier.
func (e *Engine) instanceClosing(instance string) (ident.EndpointID, bool) {
	var ids []string
	for id, s := range e.view.endpoints {
		if s.rec.Instance == instance && (s.rec.Status != store.EndpointClosed || e.view.Draining(id)) {
			ids = append(ids, id.String())
		}
	}
	if len(ids) == 0 {
		return ident.EndpointID{}, false
	}
	sort.Strings(ids)
	id, err := ident.ParseEndpointID(ids[0])
	if err != nil {
		panic("engine: an endpoint identifier of the view does not parse: " + err.Error())
	}
	return id, true
}

// restartInstance stops the running instance of the adapter and starts it again, so
// that the new process is taken on with the configuration, presets and package as
// they are now. The engine marks the instance offline as it does for a connection
// that ended, since the host reports nothing for a stop; the sessions the adapter
// holds are attached again by the new adapter.start. The instance must be enabled.
func (e *Engine) restartInstance(now time.Time, instance string) {
	if err := e.gone(now, instance, adapterhost.GoneConnectionEnded); err != nil {
		e.log.Warn("an instance could not be marked offline for its restart", "instance", instance, "error", err.Error())
	}
	e.d.Adapters.Stop(instance)
	e.d.Adapters.EnsureRunning(instance)
}

// ConfigureAdapterOp is the operation that replaces the configuration values of an
// instance with the ones the owner gives. It is judged as enabling an instance, the
// operation that decides whether this machine runs an adapter: the values can name
// the program the adapter runs, so replacing them is no weaker than enabling. The
// instance may be enabled or not; a running one is stopped and started again after
// the commit, since an adapter reads its configuration only in adapter.start, and
// one that is not enabled reads the new values when it is. The version is that of
// the instances, checked inside the change, the operation and its result kept in the
// same replacement. encode makes the result; it runs in the loop and must not wait.
func ConfigureAdapterOp(instance string, instanceConfig []byte, encode func(InstanceChange) ([]byte, error)) Operation {
	return &configureAdapterOp{instance: instance, config: instanceConfig, encode: encode}
}

type configureAdapterOp struct {
	instance string
	config   []byte
	encode   func(InstanceChange) ([]byte, error)
}

func (o *configureAdapterOp) Admin() admin.Operation {
	return admin.Operation{Kind: admin.OpEnableAdapter}
}
func (o *configureAdapterOp) ends() Ending { return EndingNone }
func (o *configureAdapterOp) run(e *Engine, x *opRun) ([]byte, error) {
	after, err := e.d.Config.SetInstanceConfig(x.configRequest(), o.instance, o.config, e.endpointOpen, instanceResult(o.encode, o.instance, x))
	if err != nil {
		return nil, err
	}
	if e.d.Adapters != nil && after.Items[o.instance].Enabled {
		e.restartInstance(x.now, o.instance)
	}
	return after.LastOperation.Result, nil
}

// DisableAdapterOp is the operation that disables an instance: the instance stays
// in the configuration with its settings and its stable endpoint ids, so that
// enabling it again finds the same endpoints. The version is that of the instances,
// checked inside the change, the operation and its result kept in the same
// replacement. After the commit the adapter host is asked to stop the instance and
// the instance is marked offline here, because the host reports nothing when it
// stops one; the instance's endpoints are then closed, a chunk at a time, and the
// records queued for them rejected. encode makes the result; it runs in the loop
// and must not wait.
func DisableAdapterOp(instance string, encode func(InstanceChange) ([]byte, error)) Operation {
	return &disableAdapterOp{instance: instance, encode: encode}
}

type disableAdapterOp struct {
	instance string
	encode   func(InstanceChange) ([]byte, error)
}

func (o *disableAdapterOp) Admin() admin.Operation {
	return admin.Operation{Kind: admin.OpDisableAdapter}
}
func (o *disableAdapterOp) ends() Ending { return EndingNone }
func (o *disableAdapterOp) run(e *Engine, x *opRun) ([]byte, error) {
	after, err := e.d.Config.DisableAdapter(x.configRequest(), o.instance, e.endpointOpen, instanceResult(o.encode, o.instance, x))
	if err != nil {
		return nil, err
	}
	e.instanceDisabled(x.now, o.instance)
	return after.LastOperation.Result, nil
}

// RemoveAdapterInstanceOp is the operation that removes an instance from the
// configuration, with its settings. The owner disables it first, and closes the
// session endpoints made through it (disabling closes them as well, so one still open
// is one the closing has not reached): an instance that is enabled, or that has such
// an endpoint open, is refused, the second with how many there are. The records of an
// endpoint still being closed are waited for, as enabling waits for them, so that the
// closing never meets an instance that is gone. The version is that of the
// instances, checked inside the change, the operation and its result kept in the same
// replacement. What the adapter keeps in its state directory, and the stable
// endpoint ids of the instance, are not touched: the adapter may have a process that
// outlives it and still uses that directory, and only the adapter knows. encode makes
// the result; it runs in the loop and must not wait.
func RemoveAdapterInstanceOp(instance string, encode func(InstanceChange) ([]byte, error)) Operation {
	return &removeAdapterInstanceOp{instance: instance, encode: encode}
}

type removeAdapterInstanceOp struct {
	instance string
	encode   func(InstanceChange) ([]byte, error)
}

// Admin judges the removal as disabling the instance: it only withdraws.
func (o *removeAdapterInstanceOp) Admin() admin.Operation {
	return admin.Operation{Kind: admin.OpDisableAdapter}
}
func (o *removeAdapterInstanceOp) ends() Ending { return EndingNone }
func (o *removeAdapterInstanceOp) run(e *Engine, x *opRun) ([]byte, error) {
	if in, ok := e.d.Config.Instances().Items[o.instance]; ok && !in.Enabled && e.openSessions(o.instance) == 0 {
		if id, busy := e.instanceClosing(o.instance); busy {
			return nil, &waitForDrain{endpoint: id}
		}
	}
	after, err := e.d.Config.RemoveInstance(x.configRequest(), o.instance, e.openSessions, e.endpointOpen, instanceResult(o.encode, o.instance, x))
	if err != nil {
		return nil, err
	}
	return after.LastOperation.Result, nil
}

// openSessions counts the session endpoints made through the instance that are not
// closed.
func (e *Engine) openSessions(instance string) int {
	n := 0
	for _, s := range e.view.endpoints {
		if s.rec.Instance == instance && s.rec.Kind == adapterreg.KindSession && s.rec.Status != store.EndpointClosed {
			n++
		}
	}
	return n
}

// instanceDisabled is what follows the commit of a disabling, driven by the state
// that was committed: the host stops the instance, the engine marks it offline and
// forgets what it learned of the adapter that ran (its admission, its probe and its
// tokens, so that the next adapter that runs is admitted and probed anew), and the
// closing of its endpoints is queued. The admission goes first among them: a
// request of the adapter that was past the host's check when the host stopped it
// must not open an endpoint that the closing, already queued, would not reach. A start that finds the instance disabled does the closing from the disk.
func (e *Engine) instanceDisabled(now time.Time, instance string) {
	if st := e.adapters[instance]; st != nil {
		st.goOffline(now)
		st.admitted = false
		st.tokens, st.probed, st.probe, st.probedAt, st.declared = nil, false, adapterproto.ProbeResult{}, time.Time{}, false
	}
	if e.d.Adapters != nil {
		e.d.Adapters.Stop(instance)
	}
	e.later(&cmdInstanceDrain{instance: instance})
}
