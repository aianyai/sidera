package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/publication"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// What the adapter host asks of the engine for the life of one connection: the
// admission of an instance and what adapter.start carries, the probe tokens, the
// state an adapter reports, and the checkpoints it asks the node to keep. The
// state of an instance that is not on disk is held here, in memory, by the loop.

var _ adapterhost.Engine = (*Engine)(nil)

// The names the probe tokens use. The Agent interface refers to them: a probe
// token is presented by the front end it was issued for, which finds the node
// through the data directory and names itself with the token.
const (
	// ProbeTokenEnv is the environment variable that carries a probe token to the
	// tool whose reach of the node is being probed.
	ProbeTokenEnv = "SIDERA_PROBE_TOKEN"
	// TokenEnv is the environment variable that carries the token of a session
	// endpoint to its tools, which are the Agent interface's front ends.
	TokenEnv = "SIDERA_TOKEN"
	// HomeEnv is the environment variable that carries the data directory.
	HomeEnv = "SIDERA_HOME"
	// ProbeTokenLifetime is how long a probe token is valid, on the injected clock,
	// from the moment it is issued.
	ProbeTokenLifetime = 5 * time.Minute
)

// adapterState is what the loop holds of one adapter instance besides its
// endpoints, which are records. It is made by the first thing that is told about
// the instance and is never dropped: an instance whose adapter is gone keeps its
// admission, so that its endpoints go on being judged by what it declared. Only
// the owner's disabling of the instance ends the admission.
type adapterState struct {
	admitted bool
	adm      adapterreg.Admission
	info     adapterproto.InitializeResult

	// online is true from adapter.start to the end of the connection; offlineSince
	// is when the engine was told that the connection ended, zero for an instance
	// that has not been online since the engine started.
	online       bool
	offlineSince time.Time

	// snapshot is the last availability the adapter reported, and received the
	// engine's own time of receiving it.
	snapshot []adapterproto.Availability
	received time.Time

	// controlUnknown: the adapter told the node that it cannot tell who controls a
	// native session it was creating, which may remain. It is memory, as the other
	// conditions are, and the alert that told the owner is on disk.
	controlUnknown bool

	probed          bool
	probe           adapterproto.ProbeResult
	probedAt        time.Time
	versionUntested bool
	// declared: the probe has passed once since the instance was enabled, so the
	// runtime endpoint is part of what the node declares. It is not withdrawn when
	// the instance later becomes unusable, so that the declaration does not flap,
	// and it ends with the disabling of the instance.
	declared bool

	// busy holds the endpoints with a running turn and rejected those whose
	// runtime had a request refused, both by reference: an adapter may report a
	// turn before the creation of its session has returned, so the state does not
	// wait for an endpoint to be known.
	busy     map[adapterproto.EndpointRef]bool
	rejected map[adapterproto.EndpointRef]bool

	tokens []probeToken
}

// probeToken is a token issued for a probe of one front end of one preset. The
// Agent interface presents it to the engine (PresentProbeToken) to prove the front
// end reaches the node; once presented it is used, and stays in the list so that
// the state of the probe can be told from which tokens were seen.
type probeToken struct {
	preset   string
	frontend adapterproto.Frontend
	token    []byte
	expires  time.Time
	used     bool
}

func (e *Engine) adapter(instance string) *adapterState {
	st := e.adapters[instance]
	if st == nil {
		st = &adapterState{}
		e.adapters[instance] = st
	}
	return st
}

// admission is what the instance was admitted with, and whether it was.
func (e *Engine) admission(instance string) (adapterreg.Admission, bool) {
	if st := e.adapters[instance]; st != nil && st.admitted {
		return st.adm, true
	}
	return adapterreg.Admission{}, false
}

// admitted is the state of an instance that has been admitted, and otherwise the
// refusal every command that needs one gives.
func (e *Engine) admitted(instance string) (*adapterState, error) {
	if st := e.adapters[instance]; st != nil && st.admitted {
		return st, nil
	}
	return nil, refuse(ReasonInstanceUnknown)
}

// enabledInstance is the configuration's instance of that name when the owner
// has enabled it.
func (e *Engine) enabledInstance(instance string) (config.Instance, bool) {
	in, ok := e.d.Config.Instances().Items[instance]
	return in, ok && in.Enabled
}

// ---------------------------------------------------------------------------
// Admission

type cmdAdmit struct {
	call[adapterproto.StartParams]
	instance string
	adm      adapterreg.Admission
	info     adapterproto.InitializeResult
}

func (c *cmdAdmit) gate() gate { return gateAlways }
func (c *cmdAdmit) run(e *Engine, _ time.Time) {
	start, err := e.admit(c.instance, c.adm, c.info)
	c.done(start, e.classify(err))
}

// AdapterAdmitted tells the engine that an adapter instance answered initialize
// with info and was admitted on a connection as a, with what it is allowed to do.
// The instance must be enabled in the configuration. An adapter that declared a
// runtime gets its runtime endpoint made (or opened again) in the same command;
// what adapter.start carries is returned. A later admission of the instance
// replaces the earlier one, and only once everything has succeeded: an admission
// that is refused leaves the instance as it was. An admission that succeeds
// forgets the busy states and the availability snapshot of the earlier connection.
//
// It is the entry the adapter host calls, and the one a test calls to give an
// instance, as the host does.
func (e *Engine) AdapterAdmitted(ctx context.Context, instance string, a adapterreg.Admission, info adapterproto.InitializeResult) (adapterproto.StartParams, error) {
	c := &cmdAdmit{call: newCall[adapterproto.StartParams](), instance: instance, adm: a, info: info}
	return roundTrip(ctx, e, submitInternal, c, c.call)
}

func (e *Engine) admit(instance string, a adapterreg.Admission, info adapterproto.InitializeResult) (adapterproto.StartParams, error) {
	var none adapterproto.StartParams
	in, ok := e.enabledInstance(instance)
	if !ok {
		return none, refuse(ReasonInstanceUnknown)
	}
	var runtime adapterproto.EndpointRef
	if a.Declares(adapterreg.KindRuntime) {
		id, err := e.admitRuntime(instance)
		if err != nil {
			return none, err
		}
		runtime = adapterproto.EndpointRef(id.String())
	}
	start, err := e.startParams(instance, in, a, runtime)
	if err != nil {
		return none, err
	}
	if err := start.Validate(); err != nil {
		return none, fmt.Errorf("engine: what adapter.start carries for instance %q is not valid: %w", instance, err)
	}
	st := e.adapter(instance)
	st.admitted, st.adm, st.info = true, a, info
	// What was reported on an earlier connection is not what this one will say:
	// the host reports nothing when it stops an instance, so no AdapterGone need
	// have cleared it.
	st.busy, st.snapshot, st.received = nil, nil, time.Time{}
	return start, nil
}

// admitRuntime makes the runtime endpoint of the instance exist and be open, and
// returns its id. The id is minted once and kept in config/, so it is the same
// for ever. An endpoint that is open is not written; one that does not exist is
// made, and one that was closed (the instance was disabled and is enabled again)
// is opened again with a new token. Both take a place in the cap of the node. A
// runtime endpoint is no caller: its token is never shown to anyone.
func (e *Engine) admitRuntime(instance string) (ident.EndpointID, error) {
	ch, err := e.d.Config.RuntimeID(instance, e.randomReader())
	var ce *config.ChangeError
	switch {
	case errors.As(err, &ce) && ce.Reason == config.ChangeInstanceUnknown:
		return ident.EndpointID{}, refuse(ReasonInstanceUnknown)
	case err != nil:
		return ident.EndpointID{}, err
	}
	st := e.view.endpoints[ch.ID]
	switch {
	case st != nil && st.rec.Kind != adapterreg.KindRuntime:
		return ident.EndpointID{}, errors.New("engine: the stable id of a runtime endpoint is the id of another kind of endpoint")
	case st != nil && st.rec.Status != store.EndpointClosed:
		return ch.ID, nil
	case st != nil && e.view.Draining(ch.ID):
		// Records queued for the endpoint while it was closed would land on the
		// endpoint that is open again. The error ends the connection and the host
		// starts the instance again after its backoff, by when the drain is done.
		return ident.EndpointID{}, fmt.Errorf("engine: the records queued for the runtime endpoint of instance %q are still being closed", instance)
	case e.view.OpenEndpoints() >= publication.MaxEndpointsPerNode:
		return ident.EndpointID{}, &Refusal{Reason: ReasonEndpointLimit, Limit: LimitNode}
	}
	token, err := mintToken(e.randomReader())
	if err != nil {
		return ident.EndpointID{}, err
	}
	rec := store.EndpointRecord{ID: ch.ID, Kind: adapterreg.KindRuntime, Instance: instance, Status: store.EndpointOpen, Token: token}
	if rec.Declaration, err = encodeDeclaration(authz.Declaration{}); err != nil {
		return ident.EndpointID{}, err
	}
	if err := e.commitEndpointAndID(&declRuntimeAdmit, ch, rec); err != nil {
		return ident.EndpointID{}, err
	}
	e.putEndpoint(rec, authz.Declaration{})
	return ch.ID, nil
}

// startParams is what adapter.start carries, built from the configuration and the
// engine's view. Every list is present, even when empty.
func (e *Engine) startParams(instance string, in config.Instance, a adapterreg.Admission, runtime adapterproto.EndpointRef) (adapterproto.StartParams, error) {
	p := adapterproto.StartParams{
		Node:            e.d.Node.String(),
		RuntimeEndpoint: runtime,
		Config:          configValues(in.Config),
		Presets:         []adapterproto.PresetConfig{},
		Sessions:        []adapterproto.SessionAttach{},
		DirectEndpoints: []adapterproto.DirectAttach{},
	}
	if a.Declares(adapterreg.KindRuntime) {
		presets := e.d.Config.Presets().Items
		for _, name := range sortedKeys(presets) {
			p.Presets = append(p.Presets, adapterproto.PresetConfig{Name: name, Config: configValues(presets[name].Config[in.Adapter])})
		}
	}
	direct := e.d.Config.EndpointIDs(instance).Direct
	for _, key := range sortedKeys(direct) {
		st := e.view.endpoints[direct[key]]
		if st != nil && st.rec.Kind == adapterreg.KindDirect && st.rec.Instance == instance && st.rec.Status != store.EndpointClosed {
			p.DirectEndpoints = append(p.DirectEndpoints, adapterproto.DirectAttach{Endpoint: adapterproto.EndpointRef(direct[key].String()), Key: key})
		}
	}
	sessions, err := e.sessionAttaches(instance)
	if err != nil {
		return adapterproto.StartParams{}, err
	}
	p.Sessions = sessions
	return p, nil
}

// sessionAttaches are the open sessions of the instance, which it is asked to take
// back control of: the endpoint, the preset and the native session it maps to, the
// access its tools need (the token of the endpoint, which the session was created
// with and holds in its environment) and the state the adapter kept for it. A session
// whose creation has not ended has no native session to take back: its creation is
// called again, as a creation is, and a closed one is not listed. They come sorted
// by endpoint.
func (e *Engine) sessionAttaches(instance string) ([]adapterproto.SessionAttach, error) {
	out := []adapterproto.SessionAttach{}
	var ids []ident.EndpointID
	for id, st := range e.view.endpoints {
		if st.rec.Kind == adapterreg.KindSession && st.rec.Instance == instance && st.rec.Status == store.EndpointOpen && st.rec.Session != nil && st.rec.Session.NativeRef != "" {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	for _, id := range ids {
		rec := e.view.endpoints[id].rec
		access, err := e.toolAccess(rec.Token)
		if err != nil {
			return nil, err
		}
		a := adapterproto.SessionAttach{
			Endpoint: adapterproto.EndpointRef(id.String()), Preset: rec.Session.Preset, NativeRef: adapterproto.NativeRef(rec.Session.NativeRef),
			Process: rec.Session.Process, ToolAccess: access,
		}
		if cp, ok := e.view.checkpoints[store.CheckpointKey{Scope: string(adapterproto.ScopeSession), ID: id.String()}]; ok {
			a.Checkpoint = append([]byte(nil), cp...)
		}
		out = append(out, a)
	}
	return out, nil
}

// configValues is a copy of configuration values that is never nil.
func configValues(in map[string]json.RawMessage) adapterproto.ConfigValues {
	out := make(adapterproto.ConfigValues, len(in))
	for k, v := range in {
		out[k] = slices.Clone(v)
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// The life of a connection

type cmdStarted struct {
	call[struct{}]
	instance string
	result   adapterproto.StartResult
}

func (c *cmdStarted) gate() gate { return gateAlways }
func (c *cmdStarted) run(e *Engine, now time.Time) {
	c.done(struct{}{}, e.markStarted(now, c.instance, c.result))
}

// AdapterStarted tells the engine what the adapter answered to adapter.start:
// the instance is online, and its busy state is the sessions that reported
// turn_active, replacing what was known before. A session the engine does not
// know is logged and ignored.
func (e *Engine) AdapterStarted(ctx context.Context, instance string, r adapterproto.StartResult) error {
	c := &cmdStarted{call: newCall[struct{}](), instance: instance, result: r}
	_, err := roundTrip(ctx, e, submitInternal, c, c.call)
	return err
}

func (e *Engine) markStarted(now time.Time, instance string, r adapterproto.StartResult) error {
	st, err := e.admitted(instance)
	if err != nil {
		return err
	}
	st.online, st.offlineSince = true, time.Time{}
	// The busy state is what the result lists, not that and what was known before.
	st.busy = nil
	for _, s := range r.Sessions {
		if _, err := e.view.resolveSession(instance, s.Endpoint); err != nil {
			e.log.Warn("the adapter reported a session the engine does not know; it is ignored", "instance", instance)
			continue
		}
		setFlag(&st.busy, s.Endpoint, s.TurnActive)
	}
	// What the instance was asked to create before its last connection ended may
	// or may not have been done: it is asked again, with the same attempt.
	e.redriveCreates(instance, now)
	e.redriveExecutions(instance, now)
	// The probe answering is what judges the sessions of the instance (probeResult). One
	// that fails is only logged by the host, so the judgment is also made after a wait.
	e.sched.set(schedKey{schedProbeFallback, instance}, now.Add(e.p.RetryBackoff))
	return nil
}

// setFlag sets or clears the entry of a set that is made when first needed.
func setFlag(set *map[adapterproto.EndpointRef]bool, ref adapterproto.EndpointRef, on bool) {
	if !on {
		delete(*set, ref)
		return
	}
	if *set == nil {
		*set = map[adapterproto.EndpointRef]bool{}
	}
	(*set)[ref] = true
}

type cmdProbeTokens struct {
	call[[]adapterproto.ToolProbeRequest]
	instance string
}

func (c *cmdProbeTokens) gate() gate { return gateAlways }
func (c *cmdProbeTokens) run(e *Engine, now time.Time) {
	reqs, err := e.probeTokens(now, c.instance)
	c.done(reqs, e.classify(err))
}

// ProbeTokens issues one single-use token for each front end of each preset of an
// adapter that declared a runtime, and returns the adapter.probe parameters:
// presets by name, front ends in the order the adapter declared them. Each token
// is valid for ProbeTokenLifetime and is kept in memory only, replacing the ones
// issued before for the instance. An adapter that declared direct endpoints only
// gets an empty list. The data directory the tokens carry is Deps.Home, which is
// asked for only when there is a token to make.
func (e *Engine) ProbeTokens(ctx context.Context, instance string) ([]adapterproto.ToolProbeRequest, error) {
	c := &cmdProbeTokens{call: newCall[[]adapterproto.ToolProbeRequest](), instance: instance}
	return roundTrip(ctx, e, submitInternal, c, c.call)
}

func (e *Engine) probeTokens(now time.Time, instance string) ([]adapterproto.ToolProbeRequest, error) {
	st, err := e.admitted(instance)
	if err != nil {
		return nil, err
	}
	reqs := []adapterproto.ToolProbeRequest{}
	var issued []probeToken
	if rt := st.adm.Capabilities().Runtime; rt != nil {
		for _, preset := range sortedKeys(e.d.Config.Presets().Items) {
			for _, front := range rt.Frontends {
				if e.d.Home == "" {
					return nil, errors.New("engine: Deps.Home is not set, and the probe tokens carry the data directory")
				}
				raw, err := mintToken(e.randomReader())
				if err != nil {
					return nil, err
				}
				text := base64.RawURLEncoding.EncodeToString(raw)
				issued = append(issued, probeToken{preset: preset, frontend: front, token: raw, expires: now.Add(ProbeTokenLifetime)})
				reqs = append(reqs, adapterproto.ToolProbeRequest{
					Preset: preset, Frontend: front, Token: adapterproto.Token(text),
					Env: map[string]string{ProbeTokenEnv: text, HomeEnv: e.d.Home},
				})
			}
		}
	}
	if err := (adapterproto.ProbeParams{ToolProbes: reqs}).Validate(); err != nil {
		return nil, fmt.Errorf("engine: the probe requests of instance %q: %w", instance, err)
	}
	st.tokens = issued
	return reqs, nil
}

type cmdProbed struct {
	call[struct{}]
	instance string
	result   adapterproto.ProbeResult
}

func (c *cmdProbed) gate() gate { return gateAlways }
func (c *cmdProbed) run(e *Engine, now time.Time) {
	c.done(struct{}{}, e.classify(e.probeResult(now, c.instance, c.result)))
}

// versionUntestedKind is the kind of the alert that says an adapter's runtime is
// outside the versions its manifest was tested against.
const versionUntestedKind = "version_untested"

// AdapterProbed tells the engine what adapter.probe found. The result is kept for
// the instance. When the runtime it names is outside every tested range of the
// installed manifest, the owner is told once with an alert of kind
// version_untested: the same words about the same instance are never raised twice,
// and alerts are on disk, so a restart does not raise them again. It blocks
// nothing and the adapter keeps running.
func (e *Engine) AdapterProbed(ctx context.Context, instance string, r adapterproto.ProbeResult) error {
	c := &cmdProbed{call: newCall[struct{}](), instance: instance, result: r}
	_, err := roundTrip(ctx, e, submitInternal, c, c.call)
	return err
}

func (e *Engine) probeResult(now time.Time, instance string, r adapterproto.ProbeResult) error {
	st, err := e.admitted(instance)
	if err != nil {
		return err
	}
	man, err := e.installedManifest(instance)
	if err != nil {
		return err
	}
	untested := adapterreg.RuntimeVersionUntested(man, r.Runtime)
	if untested {
		detail := fmt.Sprintf("runtime %s %s is outside the tested ranges of adapter %s %s", r.Runtime.Name, r.Runtime.Version, man.ID, man.Version)
		if !e.alerted(versionUntestedKind, instance, detail) {
			if _, err := e.raiseAlert(now, versionUntestedKind, instance, detail); err != nil {
				return err
			}
		}
	}
	st.probed, st.probe, st.probedAt, st.versionUntested = true, r, now, untested
	e.declareIfPassed(instance, st, now)
	// The probe is a call the host made to the adapter, so the connection is in
	// service: what the node asked the instance to create before this connection is
	// asked again now as well, in case the call made when the adapter started found
	// the connection not in service yet.
	e.redriveCreates(instance, now)
	e.redriveExecutions(instance, now)
	e.sched.remove(schedKey{schedProbeFallback, instance})
	e.noteInstanceProbed(instance, now)
	return nil
}

// alerted reports whether an alert with these words exists, acknowledged or not.
func (e *Engine) alerted(kind, subject, detail string) bool {
	return slices.ContainsFunc(e.view.alerts, func(a store.AlertRecord) bool {
		return a.Kind == kind && a.Subject == subject && a.Detail == detail
	})
}

// installedManifest is the manifest of the package the instance runs.
func (e *Engine) installedManifest(instance string) (manifest.Manifest, error) {
	in, ok := e.d.Config.Instances().Items[instance]
	if !ok {
		return manifest.Manifest{}, refuse(ReasonInstanceUnknown)
	}
	for _, a := range e.d.Config.Adapters() {
		if a.ID == in.Adapter {
			return a.Manifest, nil
		}
	}
	return manifest.Manifest{}, fmt.Errorf("engine: adapter %q of instance %q is not installed", in.Adapter, instance)
}

type cmdGone struct {
	call[struct{}]
	instance string
	cause    adapterhost.GoneCause
}

func (c *cmdGone) gate() gate { return gateAlways }
func (c *cmdGone) run(e *Engine, now time.Time) {
	c.done(struct{}{}, e.gone(now, c.instance, c.cause))
}

// AdapterGone tells the engine that the connection of the instance ended or never
// came to be. The instance is offline since the time of the command, unless it
// was not online (it never was, or it is already gone), in which case the time
// it had stays. Its busy states are unknown from then on. Its endpoints, records
// and admission stay as they are: it comes back. Any enabled instance may be
// reported, admitted or not, because a start can fail before the admission.
func (e *Engine) AdapterGone(ctx context.Context, instance string, cause adapterhost.GoneCause) error {
	c := &cmdGone{call: newCall[struct{}](), instance: instance, cause: cause}
	_, err := roundTrip(ctx, e, submitInternal, c, c.call)
	return err
}

// goOffline makes the instance offline since now, unless it was not online, in
// which case the time it had stays, and its busy states unknown.
func (st *adapterState) goOffline(now time.Time) {
	if st.online {
		st.online, st.offlineSince = false, now
	}
	st.busy = nil
}

func (e *Engine) gone(now time.Time, instance string, cause adapterhost.GoneCause) error {
	if _, ok := e.enabledInstance(instance); !ok {
		return refuse(ReasonInstanceUnknown)
	}
	e.adapter(instance).goOffline(now)
	e.sched.remove(schedKey{schedProbeFallback, instance})
	e.noteBlockedInstance(instance) // the adapter being away is a cause that has just begun
	e.promptsDirty = true           // the turns of its sessions no longer count against the cap
	e.log.Info("adapter gone", "instance", instance, "cause", cause.String())
	return nil
}

// ---------------------------------------------------------------------------
// Notifications

type cmdNotified struct {
	call[struct{}]
	instance string
	n        adapterhost.Notification
}

func (c *cmdNotified) gate() gate { return gateAlways }
func (c *cmdNotified) run(e *Engine, now time.Time) {
	c.done(struct{}{}, e.notified(now, c.instance, c.n))
}

// AdapterNotified tells the engine a notification of the adapter, in the order the
// adapter sent it:
//
//   - an availability snapshot replaces the instance's, an empty one clears it;
//   - a turn that started or ended sets or clears the busy state of its endpoint, and a
//     turn that started for the prompt in flight settles that prompt as delivered;
//   - a request the runtime made that the preset or the method refused is
//     remembered for the endpoint;
//   - a prompt that was persisted is accepted.
//
// About an endpoint the engine does not know it is not an error and writes
// nothing: a session may report a turn before its creation has returned, and the
// state kept by reference is what the creation finds. Parameters of any other type
// are an error, which means a caller that is wrong.
func (e *Engine) AdapterNotified(ctx context.Context, instance string, n adapterhost.Notification) error {
	c := &cmdNotified{call: newCall[struct{}](), instance: instance, n: n}
	_, err := roundTrip(ctx, e, submitInternal, c, c.call)
	return err
}

func (e *Engine) notified(now time.Time, instance string, n adapterhost.Notification) error {
	st, err := e.admitted(instance)
	if err != nil {
		return err
	}
	switch p := notificationParams(n.Params).(type) {
	case adapterproto.AvailabilityParams:
		st.snapshot, st.received = cloneAvailability(p.Conditions), now
		e.noteBlockedInstance(instance)
	case adapterproto.NativeRequestHandledParams:
		if p.Disposition == adapterproto.DispositionRejectedByPreset || p.Disposition == adapterproto.DispositionRejectedUnknown {
			setFlag(&st.rejected, p.Endpoint, true)
		}
	case adapterproto.TurnStartedParams:
		setFlag(&st.busy, p.Endpoint, true)
		e.noteTurnStarted(now, p.Endpoint, p.Attempt)
	case adapterproto.TurnEndedParams:
		setFlag(&st.busy, p.Endpoint, false)
		e.interruptTurnEnded(now, p.Endpoint)
		if id, err := ident.ParseEndpointID(string(p.Endpoint)); err == nil {
			e.noteTurnEnded(id)
		}
	case adapterproto.PromptPersistedParams:
	default:
		return fmt.Errorf("engine: notification %q has parameters of type %T, which is not one an adapter sends", n.Method, n.Params)
	}
	return nil
}

// notificationParams is the parameters as the value of their type: the host decodes
// into a pointer, and a caller that builds a notification by hand may pass either.
func notificationParams(m adapterproto.Message) adapterproto.Message {
	switch p := m.(type) {
	case *adapterproto.AvailabilityParams:
		return *p
	case *adapterproto.NativeRequestHandledParams:
		return *p
	case *adapterproto.TurnStartedParams:
		return *p
	case *adapterproto.TurnEndedParams:
		return *p
	case *adapterproto.PromptPersistedParams:
		return *p
	}
	return m
}

func cloneAvailability(in []adapterproto.Availability) []adapterproto.Availability {
	out := slices.Clone(in)
	for i := range out {
		out[i].Endpoints = slices.Clone(out[i].Endpoints)
	}
	return out
}

// ---------------------------------------------------------------------------
// Reads

type cmdSignals struct {
	call[handoff.Signals]
	instance string
	endpoint adapterproto.EndpointRef
}

func (c *cmdSignals) gate() gate { return gateRead }
func (c *cmdSignals) run(e *Engine, _ time.Time) {
	if _, ok := e.enabledInstance(c.instance); !ok {
		c.done(handoff.Signals{}, refuse(ReasonInstanceUnknown))
		return
	}
	c.done(e.adapterSignals(c.instance, c.endpoint), nil)
}

// adapterSignals is what keeps the adapter of an instance from working for one
// endpoint: its last availability snapshot and the engine's own observation of
// whether it is connected. An instance that never connected is offline, since when
// unknown.
func (e *Engine) adapterSignals(instance string, endpoint adapterproto.EndpointRef) handoff.Signals {
	var st adapterState
	if known := e.adapters[instance]; known != nil {
		st = *known
	}
	return adapterreg.Signals(st.snapshot, endpoint, st.received, !st.online, st.offlineSince)
}

// AdapterSignals is what keeps the adapter from working for one endpoint, as
// handoff judges blocked delivery by it: the adapter's last availability snapshot
// and the engine's own observation of whether it is connected. An enabled
// instance that has not connected is offline since an unknown time; an instance
// that is not enabled is unknown.
func (e *Engine) AdapterSignals(ctx context.Context, instance string, endpoint adapterproto.EndpointRef) (handoff.Signals, error) {
	c := &cmdSignals{call: newCall[handoff.Signals](), instance: instance, endpoint: endpoint}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}

// ProbeTokenView says for which front end of which preset a probe token is held
// and until when. The token itself is shown to no one but the adapter it was
// issued to.
type ProbeTokenView struct {
	Preset    string
	Frontend  adapterproto.Frontend
	ExpiresAt time.Time
}

// AdapterView is what the engine knows of one adapter instance.
type AdapterView struct {
	// Admitted is whether the instance has been admitted since the engine started.
	Admitted bool
	// Online is whether a connection past adapter.start exists. An instance that
	// is not online is offline, since OfflineSince, which is zero when it has not
	// been online since the engine started.
	Online       bool
	OfflineSince time.Time
	// Info is what the adapter answered to initialize on its last admission.
	Info adapterproto.InitializeResult
	// RuntimeEndpoint is the runtime endpoint of an adapter that declared a runtime.
	RuntimeEndpoint adapterproto.EndpointRef
	// Probed says whether adapter.probe has answered, and Runtime is the runtime it
	// found, if any. VersionUntested is the verdict on that version against the
	// manifest's tested ranges.
	Probed          bool
	Runtime         *adapterproto.RuntimeInfo
	VersionUntested bool
	// Conditions is the last availability snapshot and ConditionsReceived when the
	// engine got it.
	Conditions         []adapterproto.Availability
	ConditionsReceived time.Time
	// Busy lists the endpoints with a running turn, and ServerRequestRejected those
	// whose runtime had a request refused, both sorted.
	Busy                  []adapterproto.EndpointRef
	ServerRequestRejected []adapterproto.EndpointRef
	// ProbeTokens are the probe tokens held, in the order they were issued.
	ProbeTokens []ProbeTokenView
	// Probe is how far the probe of the instance has come.
	Probe ProbeView
	// RuntimeDeclared is whether the runtime endpoint is part of what the node
	// declares: the instance is enabled and its probe has passed.
	RuntimeDeclared bool
}

type cmdAdapter struct {
	call[AdapterView]
	instance string
}

func (c *cmdAdapter) gate() gate { return gateRead }
func (c *cmdAdapter) run(e *Engine, now time.Time) {
	if _, ok := e.enabledInstance(c.instance); !ok {
		c.done(AdapterView{}, refuse(ReasonInstanceUnknown))
		return
	}
	var st adapterState
	if known := e.adapters[c.instance]; known != nil {
		st = *known
	}
	v := AdapterView{
		Admitted: st.admitted, Online: st.online, OfflineSince: st.offlineSince,
		Info: st.info, Probed: st.probed, VersionUntested: st.versionUntested,
		Conditions: cloneAvailability(st.snapshot), ConditionsReceived: st.received,
		Busy: sortedRefs(st.busy), ServerRequestRejected: sortedRefs(st.rejected),
	}
	if st.probed && st.probe.Runtime != nil {
		rt := *st.probe.Runtime
		v.Runtime = &rt
	}
	if id := e.d.Config.EndpointIDs(c.instance).Runtime; st.admitted && st.adm.Declares(adapterreg.KindRuntime) && !id.IsZero() {
		v.RuntimeEndpoint = adapterproto.EndpointRef(id.String())
	}
	for _, t := range st.tokens {
		v.ProbeTokens = append(v.ProbeTokens, ProbeTokenView{Preset: t.preset, Frontend: t.frontend, ExpiresAt: t.expires})
	}
	v.Probe, v.RuntimeDeclared = st.probeView(now), st.declared
	c.done(v, nil)
}

func sortedRefs(set map[adapterproto.EndpointRef]bool) []adapterproto.EndpointRef {
	var out []adapterproto.EndpointRef
	for r := range set {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

// Adapter is what the engine knows of an adapter instance the owner has enabled,
// admitted or not; an instance that is not enabled is unknown.
func (e *Engine) Adapter(ctx context.Context, instance string) (AdapterView, error) {
	c := &cmdAdapter{call: newCall[AdapterView](), instance: instance}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}

// ---------------------------------------------------------------------------
// Checkpoints

type cmdCheckpoint struct {
	call[struct{}]
	instance string
	params   adapterproto.CheckpointPutParams
}

// A checkpoint is taken in every phase the loop runs in: the adapters are started
// before the node serves, and a session being taken back may put its state while
// the node recovers.
func (c *cmdCheckpoint) gate() gate { return gateAlways }
func (c *cmdCheckpoint) run(e *Engine, _ time.Time) {
	c.done(struct{}{}, e.classify(e.checkpointPut(c.instance, c.params)))
}

// CheckpointPut keeps the state an adapter asked the node to keep, one value for
// each (scope, id): a later put replaces the earlier. The scope session needs a
// session endpoint of the instance that is open or being created, for the adapter
// keeps the state of a session while it creates it; the scope attempt needs an
// attempt of such an endpoint, so an adapter cannot write the state another
// instance's creation will read back. The state is stored compact and is on disk when this returns.
func (e *Engine) CheckpointPut(ctx context.Context, instance string, p adapterproto.CheckpointPutParams) error {
	c := &cmdCheckpoint{call: newCall[struct{}](), instance: instance, params: p}
	_, err := roundTrip(ctx, e, submitInternal, c, c.call)
	return err
}

func (e *Engine) checkpointPut(instance string, p adapterproto.CheckpointPutParams) error {
	if _, err := e.admitted(instance); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return invalidParams(err)
	}
	key := store.CheckpointKey{Scope: string(p.Scope), ID: p.ID}
	switch p.Scope {
	case adapterproto.ScopeSession:
		st, err := e.view.resolveSessionBeingMade(instance, adapterproto.EndpointRef(p.ID))
		if err != nil {
			return err
		}
		key.ID = st.rec.ID.String()
	case adapterproto.ScopeAttempt:
		// An attempt is keyed by its own identifier, but it belongs to a session
		// endpoint, and an adapter may keep state only for what its instance owns.
		if _, err := e.view.resolveAttemptBeingMade(instance, adapterproto.AttemptID(p.ID)); err != nil {
			return err
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, p.State); err != nil {
		return invalidArgument("state", err)
	}
	rec := store.CheckpointRecord{Key: key, State: compact.Bytes()}
	if err := e.d.Store.CheckpointPut(rec); err != nil {
		return err
	}
	e.view.checkpoints[key] = rec.State
	return nil
}
