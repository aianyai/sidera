package fakeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// Serve runs the adapter side of the protocol over r and w until the daemon closes
// the stream, ctx ends, or a knob disconnects it. In a test process r and w are
// the ends of a pair of io.Pipe; in a binary they are standard input and output.
//
// Serve returns nil when the daemon closed the stream, ctx's error when ctx
// ended, ErrDisconnected after the Disconnect knob, and otherwise the reason the
// connection ended. The instance lock, if it was taken, is released.
func Serve(ctx context.Context, r io.Reader, w io.WriteCloser, cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if cfg.Knobs == nil {
		cfg.Knobs = NewKnobs()
	}
	p, err := peer.New(r, w, peer.Config{Params: cfg.Peer, Logger: cfg.Logger})
	if err != nil {
		return err
	}
	a := &adapter{
		cfg: cfg, opts: cfg.Manifest, knobs: cfg.Knobs, led: cfg.Ledger, p: p,
		sessions: map[adapterproto.EndpointRef]*nativeSession{},
		attempts: map[adapterproto.AttemptID]adapterproto.SessionCreateParams{},
		prompts:  map[adapterproto.AttemptID]promptRecord{},
		failed:   make(chan struct{}),
	}
	a.ctx = ctx
	a.register()
	cfg.Knobs.bind(a)
	defer cfg.Knobs.unbind(a)
	defer a.releaseLock()

	if err := a.log(ledger.KindAdapterStarted, ledger.Attrs{Instance: cfg.InstanceID}); err != nil {
		_ = p.Close()
		return err
	}
	p.Start()
	var ended error
	select {
	case <-p.Done():
		ended = p.Err()
	case <-ctx.Done():
	case <-a.failed:
	}
	_ = p.Close()
	a.mu.Lock()
	failErr := a.failErr
	a.mu.Unlock()
	switch {
	case failErr != nil:
		return failErr
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(ended, peer.ErrRemoteClosed):
		return nil
	}
	return ended
}

// nativeSession is a session the adapter drives, or one the daemon knows that the
// adapter could not take back.
type nativeSession struct {
	sess fakeruntime.Session // nil when lost
}

type promptRecord struct {
	endpoint adapterproto.EndpointRef
	text     string
	result   adapterproto.PromptDeliverResult
}

type adapter struct {
	cfg   Config
	opts  ManifestOptions
	knobs *Knobs
	led   *ledger.Writer
	p     *peer.Peer
	ctx   context.Context

	failed chan struct{}

	mu           sync.Mutex
	failErr      error
	initialized  bool
	hangLogged   bool
	shuttingDown bool
	release      func()
	closed       bool // the adapter has been released: nothing may take the lock any more
	sessions     map[adapterproto.EndpointRef]*nativeSession
	attempts     map[adapterproto.AttemptID]adapterproto.SessionCreateParams
	prompts      map[adapterproto.AttemptID]promptRecord
	sent         int
	calls        int
}

func (a *adapter) sentCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sent
}

func (a *adapter) outstanding() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// releaseLock ends the adapter's claim on the instance: the lock is released, and
// no handler that is still running may take it afterwards.
func (a *adapter) releaseLock() {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	a.dropLock()
}

// dropLock releases the instance lock if the adapter holds it.
func (a *adapter) dropLock() {
	a.mu.Lock()
	rel := a.release
	a.release = nil
	a.mu.Unlock()
	if rel != nil {
		rel()
	}
}

// fail ends the connection for a reason of the adapter's own: the first reason
// is the one Serve returns.
func (a *adapter) fail(err error) {
	a.mu.Lock()
	if a.failErr != nil {
		a.mu.Unlock()
		return
	}
	a.failErr = err
	close(a.failed)
	a.mu.Unlock()
}

func (a *adapter) log(kind string, attrs ledger.Attrs) error { return a.led.Append(kind, attrs) }

// hit records that a knob changed what the adapter did.
func (a *adapter) hit(knob, detail string) error {
	return a.log(ledger.KindKnobHit, ledger.Attrs{Knob: knob, Detail: detail})
}

// call makes a request of the daemon. Both messages are validated: the adapter
// never writes a message the protocol would refuse.
func (a *adapter) call(ctx context.Context, method string, params, result adapterproto.Message) error {
	if err := params.Validate(); err != nil {
		return fmt.Errorf("fakeadapter: %s params: %w", method, err)
	}
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.calls--
		a.mu.Unlock()
	}()
	var raw json.RawMessage
	if err := a.p.Call(ctx, method, params, &raw); err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	if err := adapterproto.Decode(raw, result); err != nil {
		return fmt.Errorf("fakeadapter: %s result: %w", method, err)
	}
	return nil
}

// notify sends a notification of the adapter.
func (a *adapter) notify(ctx context.Context, method string, params adapterproto.Message) error {
	if err := params.Validate(); err != nil {
		return fmt.Errorf("fakeadapter: %s params: %w", method, err)
	}
	if err := a.p.Notify(ctx, method, params); err != nil {
		return err
	}
	a.mu.Lock()
	a.sent++
	a.mu.Unlock()
	return nil
}

// --- the daemon's requests ---

func (a *adapter) register() {
	a.handle(adapterproto.MethodInitialize, a.initialize)
	a.handle(adapterproto.MethodPing, func(context.Context, json.RawMessage) (any, error) { return adapterproto.Empty{}, nil })
	a.handle(adapterproto.MethodShutdown, a.shutdown)
	a.handle(adapterproto.MethodAdapterStart, a.start)
	a.handle(adapterproto.MethodAdapterProbe, a.probe)
	a.handle(adapterproto.MethodSessionCreate, a.sessionCreate)
	a.handle(adapterproto.MethodPromptDeliver, a.promptDeliver)
	a.handle(adapterproto.MethodPromptReconcile, a.promptReconcile)
	a.handle(adapterproto.MethodSessionInterrupt, a.sessionInterrupt)
	a.handle(adapterproto.MethodSessionClose, a.sessionClose)
	a.p.HandleNotification(adapterproto.MethodEndpointPending, a.endpointPending)
}

// handle registers a request handler behind the checks every method has: a hung
// adapter answers nothing, and before initialize and after shutdown only ping is
// answered.
func (a *adapter) handle(method string, h func(context.Context, json.RawMessage) (any, error)) {
	a.p.Handle(method, func(ctx context.Context, raw json.RawMessage) (any, error) {
		if a.knobs.hanging() {
			a.mu.Lock()
			first := !a.hangLogged
			a.hangLogged = true
			a.mu.Unlock()
			if first {
				if err := a.hit("hang", method); err != nil {
					return nil, err
				}
			}
			// A request a hung adapter keeps is held like any other, so that a fixture
			// that asks whether the system is quiet finds the request the daemon has in
			// flight among the ones expected.
			held := &heldRequest{method: method}
			a.knobs.addHeld(held)
			defer a.knobs.removeHeld(held)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if method != adapterproto.MethodInitialize && method != adapterproto.MethodPing {
			a.mu.Lock()
			initialized, shutting := a.initialized, a.shuttingDown
			a.mu.Unlock()
			if !initialized {
				return nil, peer.NewError(peer.CodeNotInitialized, "initialize first")
			}
			if shutting {
				return nil, peer.NewError(peer.CodeShuttingDown, "shutting down")
			}
		}
		return h(ctx, raw)
	})
}

// decodeParams strictly decodes the params of a request; a request without params
// is read as an empty object.
func decodeParams(raw json.RawMessage, into adapterproto.Message) error {
	if raw == nil {
		raw = json.RawMessage(`{}`)
	}
	if err := adapterproto.Decode(raw, into); err != nil {
		return peer.NewError(peer.CodeInvalidParams, err.Error())
	}
	return nil
}

// answer validates a result before it is sent.
func answer(res adapterproto.Message) (any, error) {
	if err := res.Validate(); err != nil {
		return nil, fmt.Errorf("fakeadapter built an invalid result: %w", err)
	}
	return res, nil
}

func (a *adapter) initialize(_ context.Context, raw json.RawMessage) (any, error) {
	var p adapterproto.InitializeParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	a.mu.Lock()
	already := a.initialized
	a.mu.Unlock()
	if already {
		return nil, peer.NewError(peer.CodeInvalidRequest, "already initialized")
	}
	version, err := adapterproto.Negotiate(p.Protocol, adapterproto.SupportedRange())
	if err != nil {
		var nc *adapterproto.NoCommonVersionError
		if errors.As(err, &nc) {
			e := adapterproto.NewUnsupportedVersionError(nc)
			return nil, (*peer.Error)(&e)
		}
		return nil, peer.NewError(peer.CodeInvalidParams, err.Error())
	}
	// The lock comes before the answer: an adapter that has answered controls
	// sessions, and two of them must never at once. Taking it and handing it to the
	// adapter, which releases it when Serve returns, is one step under the adapter's
	// lock: the handler of a connection that has just ended may still be running,
	// and a lock taken after the release would be held until the process ends.
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, peer.NewError(peer.CodeShuttingDown, "the connection has ended")
	}
	release, err := lockInstance(p.Instance.LockPath)
	if err == nil {
		a.release = release
	}
	a.mu.Unlock()
	switch {
	case errors.Is(err, errLocked):
		if lerr := a.log(ledger.KindAdapterLockRefused, ledger.Attrs{Instance: a.cfg.InstanceID, LockPath: p.Instance.LockPath}); lerr != nil {
			return nil, lerr
		}
		return nil, peer.NewError(adapterproto.CodeInstanceLocked, "another process holds the instance lock")
	case err != nil:
		return nil, fmt.Errorf("fakeadapter: lock: %w", err)
	}
	if err := a.log(ledger.KindAdapterLockAcquired, ledger.Attrs{Instance: a.cfg.InstanceID, LockPath: p.Instance.LockPath}); err != nil {
		a.dropLock()
		return nil, err
	}
	a.mu.Lock()
	a.initialized = true
	a.mu.Unlock()
	return answer(adapterproto.InitializeResult{
		ProtocolVersion: version,
		Adapter:         adapterproto.AdapterInfo{ID: a.opts.ID, Version: a.opts.Version},
		Capabilities:    a.opts.Capabilities(),
	})
}

func (a *adapter) shutdown(_ context.Context, _ json.RawMessage) (any, error) {
	a.mu.Lock()
	a.shuttingDown = true
	a.mu.Unlock()
	return adapterproto.Empty{}, nil
}

func (a *adapter) endpointPending(_ context.Context, raw json.RawMessage) {
	var p adapterproto.PendingParams
	if err := adapterproto.Decode(raw, &p); err != nil {
		return
	}
	// A notification has no answer to carry a failure, and a ledger that cannot be
	// written fails the test through the next request that writes to it.
	_ = a.log(ledger.KindEndpointPendingReceived, ledger.Attrs{Endpoint: string(p.Endpoint), Count: p.Count})
}

// outcome returns the knob outcome for the next request of a kind, if any, after
// writing its hit. A held request waits here and then goes its normal way; an
// error that has no effect is returned for the handler to answer with.
func (a *adapter) outcome(ctx context.Context, request, method string) (Outcome, bool, error) {
	o, ok := a.knobs.next(request)
	if !ok {
		return Outcome{}, false, nil
	}
	if err := a.hit(request+"."+outcomeName(o.Kind), o.Reason); err != nil {
		return Outcome{}, false, err
	}
	switch {
	case o.Kind == OutcomeHold:
		if err := a.knobs.hold(ctx, method); err != nil {
			return Outcome{}, false, err
		}
		return Outcome{}, false, nil
	case o.Kind == OutcomeError && !o.AfterEffect:
		return o, true, peer.NewError(o.Code, injectedMessage)
	}
	return o, true, nil
}

func outcomeName(k OutcomeKind) string {
	switch k {
	case OutcomeFail:
		return "fail"
	case OutcomeIndeterminate:
		return "indeterminate"
	case OutcomeRefuse:
		return "refuse"
	case OutcomeAccepted:
		return "accepted"
	case OutcomeRequested:
		return "requested"
	case OutcomeNotRunning:
		return "not-running"
	case OutcomeUnconfirmed:
		return "unconfirmed"
	case OutcomeUnreachable:
		return "unreachable"
	case OutcomeError:
		return "error"
	case OutcomeHold:
		return "hold"
	}
	return "unknown"
}

// runtimeMethod refuses a method of the runtime kind when the adapter declared no
// runtime.
func (a *adapter) runtimeMethod() error {
	if !a.opts.Runtime {
		return peer.NewError(adapterproto.CodeCapabilityNotDeclared, "this adapter declares no runtime")
	}
	return nil
}

func (a *adapter) identity(s fakeruntime.Session) *adapterproto.ProcessIdentity {
	return &adapterproto.ProcessIdentity{PID: s.PID(), StartKey: s.StartKey()}
}

func (a *adapter) session(ep adapterproto.EndpointRef) (*nativeSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[ep]
	if !ok {
		return nil, peer.NewError(adapterproto.CodeUnknownEndpoint, "no such session endpoint")
	}
	return s, nil
}

func (a *adapter) adopt(ep adapterproto.EndpointRef, s fakeruntime.Session) {
	a.mu.Lock()
	a.sessions[ep] = &nativeSession{sess: s}
	a.mu.Unlock()
	if s != nil {
		s.Watch(&sessionWatcher{a: a, endpoint: ep, sess: s})
	}
}

func (a *adapter) start(ctx context.Context, raw json.RawMessage) (any, error) {
	var p adapterproto.StartParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	o, has, err := a.outcome(ctx, forStart, adapterproto.MethodAdapterStart)
	if err != nil {
		return nil, err
	}
	if err := a.knobs.pause(ctx, a, PauseStartBeforeReply); err != nil {
		return nil, err
	}
	states := make([]adapterproto.SessionState, 0, len(p.Sessions))
	for _, at := range p.Sessions {
		st := adapterproto.SessionState{Endpoint: at.Endpoint, State: adapterproto.AttachLost}
		sess, found := a.cfg.Runtime.Find(string(at.NativeRef))
		switch {
		case has && o.Kind == OutcomeUnreachable:
			st.State = adapterproto.AttachUnreachable
			if found {
				a.adopt(at.Endpoint, sess)
			}
		case found && sess.Alive():
			st.State = adapterproto.AttachAttached
			_, st.TurnActive = sess.TurnActive()
			st.Process = a.identity(sess)
			a.adopt(at.Endpoint, sess)
		default:
			a.adopt(at.Endpoint, nil)
		}
		states = append(states, st)
	}
	return answer(adapterproto.StartResult{Sessions: states})
}

func (a *adapter) probe(_ context.Context, raw json.RawMessage) (any, error) {
	var p adapterproto.ProbeParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	res := adapterproto.ProbeResult{
		Auth:         adapterproto.AuthValid,
		Dependencies: []adapterproto.Dependency{},
		Presets:      []adapterproto.PresetStatus{},
		ToolProbes:   []adapterproto.ToolProbeResult{},
	}
	if a.opts.Runtime {
		res.Runtime = &adapterproto.RuntimeInfo{Name: a.opts.RuntimeName, Version: a.opts.RuntimeVersion}
		res.Dependencies = append(res.Dependencies, adapterproto.Dependency{Name: a.opts.RuntimeName, State: adapterproto.DependencyOK})
	}
	seen := map[string]bool{}
	for _, tp := range p.ToolProbes {
		if !seen[tp.Preset] {
			seen[tp.Preset] = true
			res.Presets = append(res.Presets, adapterproto.PresetStatus{Name: tp.Preset, Usable: true})
		}
		res.ToolProbes = append(res.ToolProbes, adapterproto.ToolProbeResult{
			Preset: tp.Preset, Frontend: tp.Frontend, Outcome: adapterproto.ProbeRan, Method: adapterproto.ProbeUnsandboxed,
		})
	}
	return answer(res)
}

func sameCreate(a, b adapterproto.SessionCreateParams) bool {
	// The checkpoint is the daemon's stored state of the attempt and may differ
	// between tries of one attempt.
	a.Checkpoint, b.Checkpoint = nil, nil
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

func (a *adapter) sessionCreate(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := a.runtimeMethod(); err != nil {
		return nil, err
	}
	var p adapterproto.SessionCreateParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	if err := a.log(ledger.KindSessionCreateReceived, ledger.Attrs{Attempt: string(p.Attempt), Preset: p.Preset, Endpoint: string(p.SessionEndpoint)}); err != nil {
		return nil, err
	}
	a.mu.Lock()
	earlier, retried := a.attempts[p.Attempt]
	if !retried {
		a.attempts[p.Attempt] = p
	}
	a.mu.Unlock()
	if retried && !sameCreate(earlier, p) {
		return nil, peer.NewError(adapterproto.CodeAttemptConflict, "the attempt was made with other parameters")
	}

	o, has, err := a.outcome(ctx, forCreate, adapterproto.MethodSessionCreate)
	if err != nil {
		return nil, err
	}
	failedBeforeEffect := has && o.Kind == OutcomeFail && o.Reason != string(adapterproto.CreateFailControlUnknown)
	if failedBeforeEffect {
		if err := a.log(ledger.KindNativeCreateReturned, ledger.Attrs{Attempt: string(p.Attempt), Outcome: string(adapterproto.CreateFailed)}); err != nil {
			return nil, err
		}
		return answer(adapterproto.SessionCreateResult{Outcome: adapterproto.CreateFailed, Reason: adapterproto.CreateFailure(o.Reason)})
	}

	if err := a.call(ctx, adapterproto.MethodCheckpointPut, adapterproto.CheckpointPutParams{
		Scope: adapterproto.ScopeAttempt, ID: string(p.Attempt), State: json.RawMessage(`{"phase":"creating"}`),
	}, &adapterproto.Empty{}); err != nil {
		return nil, err
	}
	sess, err := a.cfg.Runtime.Create(ctx, fakeruntime.CreateRequest{
		Attempt: string(p.Attempt), Endpoint: string(p.SessionEndpoint), Preset: p.Preset,
		FirstAttempt: string(p.FirstPrompt.Attempt), FirstPrompt: p.FirstPrompt.Text, Env: p.ToolAccess.Env,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if lerr := a.log(ledger.KindNativeCreateReturned, ledger.Attrs{Attempt: string(p.Attempt), Outcome: string(adapterproto.CreateFailed)}); lerr != nil {
			return nil, lerr
		}
		return answer(adapterproto.SessionCreateResult{Outcome: adapterproto.CreateFailed, Reason: adapterproto.CreateFailNativeError})
	}
	a.adopt(p.SessionEndpoint, sess)
	outcome := adapterproto.CreateCreated
	switch {
	case has && o.Kind == OutcomeFail:
		outcome = adapterproto.CreateFailed
	case has && o.Kind == OutcomeIndeterminate:
		outcome = adapterproto.CreateIndeterminate
	}
	// The entry names the runtime's own entry for the session, which the runtime
	// wrote before it answered: that is how the ledger shows the effect was recorded
	// before it was confirmed.
	created := sess.Created()
	if err := a.log(ledger.KindNativeCreateReturned, ledger.Attrs{
		Attempt: string(p.Attempt), NativeRef: sess.Ref(), RuntimeWriter: created.Writer, RuntimeSeq: created.Seq, Outcome: string(outcome),
	}); err != nil {
		return nil, err
	}
	if err := a.knobs.pause(ctx, a, PauseCreateAfterNative); err != nil {
		return nil, err
	}
	switch {
	case has && o.Kind == OutcomeError:
		return nil, peer.NewError(o.Code, injectedMessage)
	case outcome == adapterproto.CreateFailed:
		return answer(adapterproto.SessionCreateResult{Outcome: outcome, Reason: adapterproto.CreateFailControlUnknown})
	case outcome == adapterproto.CreateIndeterminate:
		code := adapterproto.GroundsCode(o.Reason)
		if o.Reason == "" {
			code = adapterproto.GroundsNativeUnreachable
		}
		return answer(adapterproto.SessionCreateResult{
			Outcome: outcome, NativeRef: adapterproto.NativeRef(sess.Ref()), Grounds: &adapterproto.Grounds{Code: code},
		})
	}
	if err := a.call(ctx, adapterproto.MethodCheckpointPut, adapterproto.CheckpointPutParams{
		Scope: adapterproto.ScopeSession, ID: string(p.SessionEndpoint), State: json.RawMessage(`{"native_ref":"` + sess.Ref() + `"}`),
	}, &adapterproto.Empty{}); err != nil {
		return nil, err
	}
	return answer(adapterproto.SessionCreateResult{
		Outcome:   adapterproto.CreateCreated,
		NativeRef: adapterproto.NativeRef(sess.Ref()),
		Process:   a.identity(sess),
		Frontends: append([]adapterproto.Frontend(nil), a.opts.Frontends...),
	})
}

func (a *adapter) promptDeliver(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := a.runtimeMethod(); err != nil {
		return nil, err
	}
	var p adapterproto.PromptDeliverParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	ns, err := a.session(p.Endpoint)
	if err != nil {
		return nil, err
	}
	digest := ledger.Digest([]byte(p.Text))
	attrs := ledger.Attrs{Endpoint: string(p.Endpoint), Attempt: string(p.Attempt), TextDigest: digest}
	if err := a.log(ledger.KindPromptDeliverReceived, attrs); err != nil {
		return nil, err
	}
	a.mu.Lock()
	earlier, seen := a.prompts[p.Attempt]
	a.mu.Unlock()
	if seen {
		if earlier.endpoint != p.Endpoint || earlier.text != p.Text {
			return nil, peer.NewError(adapterproto.CodeAttemptConflict, "the attempt was made with other parameters")
		}
		return answer(earlier.result)
	}
	o, has, err := a.outcome(ctx, forPrompt, adapterproto.MethodPromptDeliver)
	if err != nil {
		return nil, err
	}
	remember := func(res adapterproto.PromptDeliverResult) (any, error) {
		a.mu.Lock()
		a.prompts[p.Attempt] = promptRecord{endpoint: p.Endpoint, text: p.Text, result: res}
		a.mu.Unlock()
		return answer(res)
	}
	refuse := func(reason adapterproto.PromptRefusal) (any, error) {
		return answer(adapterproto.PromptDeliverResult{Stage: adapterproto.PromptRefused, Reason: reason})
	}
	if has && o.Kind == OutcomeRefuse {
		return refuse(adapterproto.PromptRefusal(o.Reason))
	}
	if ns.sess == nil {
		return refuse(adapterproto.RefusalSessionLost)
	}
	switch err := ns.sess.Deliver(ctx, string(p.Attempt), p.Text); {
	case errors.Is(err, fakeruntime.ErrBusy):
		return refuse(adapterproto.RefusalBusy)
	case errors.Is(err, fakeruntime.ErrNotAlive):
		return refuse(adapterproto.RefusalSessionLost)
	case err != nil:
		return nil, err
	}
	if err := a.log(ledger.KindPromptForwarded, attrs); err != nil {
		return nil, err
	}
	if err := a.knobs.pause(ctx, a, PausePromptAfterForward); err != nil {
		return nil, err
	}
	if has && o.Kind == OutcomeError {
		return nil, peer.NewError(o.Code, injectedMessage)
	}
	stage := adapterproto.PromptPersisted
	if has && o.Kind == OutcomeAccepted {
		stage = adapterproto.PromptAccepted
	}
	return remember(adapterproto.PromptDeliverResult{Stage: stage})
}

func (a *adapter) promptReconcile(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := a.runtimeMethod(); err != nil {
		return nil, err
	}
	var p adapterproto.ReconcileParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	ns, err := a.session(p.Endpoint)
	if err != nil {
		return nil, err
	}
	o, has, err := a.outcome(ctx, forReconcile, adapterproto.MethodPromptReconcile)
	if err != nil {
		return nil, err
	}
	if has && o.Kind == OutcomeIndeterminate {
		code := adapterproto.GroundsCode(o.Reason)
		if o.Reason == "" {
			code = adapterproto.GroundsNativeUnreachable
		}
		return answer(adapterproto.ReconcileResult{Verdict: adapterproto.VerdictIndeterminate, Grounds: adapterproto.Grounds{Code: code}})
	}
	if ns.sess == nil {
		return answer(adapterproto.ReconcileResult{Verdict: adapterproto.VerdictIndeterminate, Grounds: adapterproto.Grounds{Code: adapterproto.GroundsNativeUnreachable}})
	}
	rec, err := ns.sess.Reconcile(string(p.Attempt))
	if err != nil {
		return answer(adapterproto.ReconcileResult{Verdict: adapterproto.VerdictIndeterminate, Grounds: adapterproto.Grounds{Code: adapterproto.GroundsRecordUnreadable}})
	}
	switch {
	case rec.Found:
		turn := adapterproto.TurnReport{Ref: "queued", State: adapterproto.TurnStateRunning}
		if rec.Turn != "" {
			turn = adapterproto.TurnReport{Ref: adapterproto.TurnRef(rec.Turn), State: adapterproto.TurnState(rec.TurnState)}
		}
		return answer(adapterproto.ReconcileResult{Verdict: adapterproto.VerdictFound, Turn: &turn, Grounds: adapterproto.Grounds{Code: adapterproto.GroundsAttemptInRecord}})
	case rec.TurnRunning:
		return answer(adapterproto.ReconcileResult{Verdict: adapterproto.VerdictIndeterminate, Grounds: adapterproto.Grounds{Code: adapterproto.GroundsTurnRunning}})
	}
	return answer(adapterproto.ReconcileResult{Verdict: adapterproto.VerdictProvablyAbsent, Grounds: adapterproto.Grounds{Code: adapterproto.GroundsRecordCompleteWithoutAttempt}})
}

func (a *adapter) sessionInterrupt(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := a.runtimeMethod(); err != nil {
		return nil, err
	}
	if !a.opts.Interrupt {
		return nil, peer.NewError(adapterproto.CodeCapabilityNotDeclared, "this adapter does not interrupt")
	}
	var p adapterproto.InterruptParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	ns, err := a.session(p.Endpoint)
	if err != nil {
		return nil, err
	}
	if err := a.log(ledger.KindInterruptReceived, ledger.Attrs{Endpoint: string(p.Endpoint)}); err != nil {
		return nil, err
	}
	o, has, err := a.outcome(ctx, forInterrupt, adapterproto.MethodSessionInterrupt)
	if err != nil {
		return nil, err
	}
	refused := func(r adapterproto.InterruptRefusal) (any, error) {
		return answer(adapterproto.InterruptResult{Stage: adapterproto.InterruptRefused, Reason: r})
	}
	switch {
	case has && o.Kind == OutcomeRefuse:
		return refused(adapterproto.InterruptRefusal(o.Reason))
	case has && o.Kind == OutcomeNotRunning:
		return answer(adapterproto.InterruptResult{Stage: adapterproto.InterruptNotRunning})
	case ns.sess == nil:
		return refused(adapterproto.InterruptNativeUnreachable)
	}
	stage := adapterproto.InterruptRequested
	if !(has && o.Kind == OutcomeRequested) {
		res, err := ns.sess.Interrupt(ctx)
		switch {
		case errors.Is(err, fakeruntime.ErrNotAlive):
			return refused(adapterproto.InterruptNativeUnreachable)
		case err != nil:
			return nil, err
		case res == fakeruntime.InterruptNotRunning:
			stage = adapterproto.InterruptNotRunning
		default:
			stage = adapterproto.InterruptStopped
		}
	}
	if err := a.knobs.pause(ctx, a, PauseInterruptAfterForward); err != nil {
		return nil, err
	}
	if has && o.Kind == OutcomeError {
		return nil, peer.NewError(o.Code, injectedMessage)
	}
	return answer(adapterproto.InterruptResult{Stage: stage})
}

func (a *adapter) sessionClose(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := a.runtimeMethod(); err != nil {
		return nil, err
	}
	var p adapterproto.CloseParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	ns, err := a.session(p.Endpoint)
	if err != nil {
		return nil, err
	}
	if err := a.log(ledger.KindCloseReceived, ledger.Attrs{Endpoint: string(p.Endpoint)}); err != nil {
		return nil, err
	}
	o, has, err := a.outcome(ctx, forClose, adapterproto.MethodSessionClose)
	if err != nil {
		return nil, err
	}
	if ns.sess != nil {
		if err := ns.sess.Close(ctx); err != nil {
			return nil, err
		}
	}
	if err := a.knobs.pause(ctx, a, PauseCloseAfterForward); err != nil {
		return nil, err
	}
	switch {
	case has && o.Kind == OutcomeError:
		return nil, peer.NewError(o.Code, injectedMessage)
	case has && o.Kind == OutcomeUnconfirmed:
		reason := adapterproto.CloseReason(o.Reason)
		if o.Reason == "" {
			reason = adapterproto.CloseNativeUnreachable
		}
		return answer(adapterproto.CloseResult{Outcome: adapterproto.CloseUnconfirmed, Reason: reason})
	}
	return answer(adapterproto.CloseResult{Outcome: adapterproto.CloseClosed})
}

// --- what a session tells the adapter ---

// sessionWatcher turns the events of a native session into the protocol's
// notifications and requests.
type sessionWatcher struct {
	a        *adapter
	endpoint adapterproto.EndpointRef
	sess     fakeruntime.Session
}

func (w *sessionWatcher) TurnStarted(e fakeruntime.TurnStartedEvent) {
	p := adapterproto.TurnStartedParams{
		Endpoint: w.endpoint, Turn: adapterproto.TurnRef(e.Turn), Attempt: adapterproto.AttemptID(e.Attempt),
		Process: w.a.identity(w.sess), AtMS: e.AtMS,
	}
	if err := w.a.notify(w.a.ctx, adapterproto.MethodTurnStarted, p); err != nil {
		w.a.fail(err)
	}
}

func (w *sessionWatcher) TurnEnded(e fakeruntime.TurnEndedEvent) {
	p := adapterproto.TurnEndedParams{Endpoint: w.endpoint, Turn: adapterproto.TurnRef(e.Turn), Final: adapterproto.TurnFinal(e.Final), AtMS: e.AtMS}
	if err := w.a.notify(w.a.ctx, adapterproto.MethodTurnEnded, p); err != nil {
		w.a.fail(err)
	}
}

// ToolResult reports a recv result to the daemon, as an adapter that acknowledges
// on behalf of the agent does when it reads the result in the runtime's record.
func (w *sessionWatcher) ToolResult(e fakeruntime.ToolResultEvent) {
	a := w.a
	if !a.opts.Runtime || !a.opts.AckOnBehalf {
		return
	}
	mode := a.knobs.observedMode()
	text := e.Text
	switch mode {
	case ObservedTruncated:
		text = text[:len(text)/2] + "\n" + truncatedMarkerSay
	case ObservedReplaced:
		text = replacedMarker
	}
	if mode != ObservedFaithful {
		if err := a.hit("observed."+string(mode), e.Lease); err != nil {
			a.fail(err)
			return
		}
	}
	var res adapterproto.RecvObservedResult
	err := a.call(a.ctx, adapterproto.MethodRecvObserved, adapterproto.RecvObservedParams{
		Endpoint: w.endpoint, Lease: adapterproto.LeaseRef(e.Lease), Frontend: adapterproto.Frontend(e.Frontend), Observed: text,
	}, &res)
	verdict := "error"
	if err == nil {
		verdict = string(res.Verdict)
	} else if a.ctx.Err() != nil {
		return
	}
	if err := a.log(ledger.KindObservedSent, ledger.Attrs{Lease: e.Lease, Mode: string(mode), Verdict: verdict}); err != nil {
		a.fail(err)
	}
}
