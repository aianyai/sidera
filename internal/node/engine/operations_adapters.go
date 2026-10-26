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
	// AlreadyCurrent: the version was the installed one, and nothing was done but
	// storing the result.
	AlreadyCurrent bool
}

// InstallAdapterOp is the operation that installs the adapter package in the
// directory the owner names, or upgrades the adapter to its version. The package is
// copied outside the loop into a directory next to the versions of its adapter, and
// the copy is what is parsed and checked (the manifest, and that the package runs
// on this platform), so that what is installed is what was checked and not what the
// source may have become; the loop only renames the copy into place and makes it
// current. A request that was done before is answered without reading the package.
// An adapter that has an enabled instance is not upgraded: the instance runs the
// version that is current. encode makes the result; it runs in the loop and must
// not wait.
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
func (o *installAdapterOp) stops() bool { return false }

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

func (o *installAdapterOp) run(e *Engine, x *opRun) ([]byte, error) {
	if o.stageErr != nil {
		return nil, o.stageErr
	}
	plan, err := e.d.Config.PlanInstall(o.staged)
	if err != nil {
		return nil, err
	}
	result, err := o.encode(AdapterInstalled{ID: o.staged.ID, Version: o.staged.Version, AlreadyCurrent: plan.AlreadyCurrent})
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
	if err := plan.DiscardStale(tx); err != nil {
		return nil, err
	}
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
	return result, nil
}

// settle drops the staging when the commit did not take it over (refused, found
// current, a resend that was answered, a failure that a retry may cure). After a
// fatal failure of the disk nothing more is written, and the next start removes
// what is left.
func (o *installAdapterOp) settle(e *Engine, err error) {
	if o.staged.Dir == "" || o.committed {
		return
	}
	if r, ok := ReasonOf(err); ok && r == ReasonStorageFatal {
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
	// Version is the version of the instances after the change.
	Version admin.Version
}

func instanceResult(encode func(InstanceChange) ([]byte, error), instance string, x *opRun) config.ResultFunc[config.Instances] {
	return func(after config.Instances) (json.RawMessage, error) {
		b, err := encode(InstanceChange{Instance: instance, Enabled: after.Items[instance].Enabled, Version: after.Version})
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
func (o *enableAdapterOp) stops() bool { return false }
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
func (o *disableAdapterOp) stops() bool { return false }
func (o *disableAdapterOp) run(e *Engine, x *opRun) ([]byte, error) {
	after, err := e.d.Config.DisableAdapter(x.configRequest(), o.instance, e.endpointOpen, instanceResult(o.encode, o.instance, x))
	if err != nil {
		return nil, err
	}
	e.instanceDisabled(x.now, o.instance)
	return after.LastOperation.Result, nil
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
		st.tokens, st.probed, st.probe, st.probedAt, st.versionUntested, st.declared = nil, false, adapterproto.ProbeResult{}, time.Time{}, false, false
	}
	if e.d.Adapters != nil {
		e.d.Adapters.Stop(instance)
	}
	e.later(&cmdInstanceDrain{instance: instance})
}
