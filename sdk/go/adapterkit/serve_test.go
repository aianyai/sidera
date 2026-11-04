package adapterkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

const (
	endpoint = "22222222222222222222222222222222"
	attempt  = "6f1c2a4e-8b3d-4c5a-9e7f-0a1b2c3d4e5f"
	token    = "k3JpQ9vL2mXwR7tYbN5cZ8dHfG1sAeUo0iWxM4yTqBA"
)

// stub is an application that records what it was asked and answers like a
// runtime adapter that always succeeds.
type stub struct {
	adapterkit.Base

	mu          sync.Mutex
	conn        *adapterkit.Conn
	instance    adapterproto.InstanceInfo
	initErr     error
	shutdowns   int
	intents     []adapterproto.ShutdownIntent
	shutdownErr error
	created     []adapterproto.SessionCreateParams
	attached    []adapterproto.SessionAttachParams
	started     []adapterproto.StartParams
	badResult   bool
}

func (s *stub) Initialize(_ context.Context, conn *adapterkit.Conn, in adapterproto.InstanceInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn, s.instance = conn, in
	return s.initErr
}

func (s *stub) Shutdown(_ context.Context, p adapterproto.ShutdownParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdowns++
	s.intents = append(s.intents, p.Intent)
	return s.shutdownErr
}

func (s *stub) CreateSession(_ context.Context, p adapterproto.SessionCreateParams) (adapterproto.SessionCreateResult, error) {
	s.mu.Lock()
	s.created = append(s.created, p)
	bad := s.badResult
	s.mu.Unlock()
	if bad {
		return adapterproto.SessionCreateResult{Outcome: adapterproto.CreateCreated}, nil // no native ref
	}
	return adapterproto.SessionCreateResult{
		Outcome: adapterproto.CreateCreated, NativeRef: "native-1", Frontends: []adapterproto.Frontend{adapterproto.FrontendCLI},
	}, nil
}

func (s *stub) Start(ctx context.Context, p adapterproto.StartParams) (adapterproto.StartResult, error) {
	s.mu.Lock()
	s.started = append(s.started, p)
	s.mu.Unlock()
	return s.Base.Start(ctx, p)
}

func (s *stub) AttachSession(_ context.Context, p adapterproto.SessionAttachParams) (adapterproto.SessionAttachResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attached = append(s.attached, p)
	return adapterproto.SessionAttachResult{Outcome: adapterproto.SessionAttachAttached}, nil
}

func runtimeCaps() adapterproto.Capabilities {
	return adapterproto.Capabilities{Runtime: &adapterproto.RuntimeCapabilities{Frontends: []adapterproto.Frontend{adapterproto.FrontendCLI}}}
}

// side is the daemon's end of a connection to a served adapter.
type side struct {
	t      *testing.T
	daemon *peer.Peer
	cancel context.CancelFunc
	done   chan error // the result of Serve
	once   sync.Once
	result error

	mu    sync.Mutex
	notes []string // the methods of the notifications the adapter sent
}

// A test creates the directory of its lock before it calls serveOn: cleanups run
// in reverse order, so the directory is then removed after Serve has returned and
// released the lock, which Windows does not let be removed while it is held.
func serveOn(t *testing.T, cfg adapterkit.Config) *side {
	t.Helper()
	aR, dW := io.Pipe() // the daemon writes, the adapter reads
	dR, aW := io.Pipe() // the adapter writes, the daemon reads
	ctx, cancel := context.WithCancel(context.Background())
	s := &side{t: t, cancel: cancel, done: make(chan error, 1)}
	d, err := peer.New(dR, dW, peer.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{adapterproto.MethodTurnEnded, adapterproto.MethodAdapterAvailability} {
		d.HandleNotification(m, func(context.Context, json.RawMessage) {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.notes = append(s.notes, m)
		})
	}
	d.Start()
	s.daemon = d
	go func() { s.done <- adapterkit.Serve(ctx, aR, aW, cfg) }()
	t.Cleanup(func() { s.end() })
	return s
}

// end closes the daemon's side, as the daemon does, and waits for Serve to return.
func (s *side) end() error {
	s.once.Do(func() {
		_ = s.daemon.Close()
		select {
		case s.result = <-s.done:
			s.cancel()
		case <-time.After(10 * time.Second):
			s.t.Error("Serve did not return after the daemon closed the stream")
		}
	})
	return s.result
}

func (s *side) call(method string, params any, result any) error {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.daemon.Call(ctx, method, params, result)
}

func codeOf(t *testing.T, err error) int {
	t.Helper()
	var pe *peer.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not an error object", err)
	}
	return pe.Code
}

func config(app adapterkit.Application, caps adapterproto.Capabilities) adapterkit.Config {
	return adapterkit.Config{Adapter: adapterproto.AdapterInfo{ID: "stub", Version: "0.1.0"}, Capabilities: caps, App: app}
}

func initParams(lock string) adapterproto.InitializeParams {
	return adapterproto.InitializeParams{
		Protocol: adapterproto.SupportedRange(), Daemon: adapterproto.DaemonInfo{Version: "test"},
		Instance: adapterproto.InstanceInfo{ID: "work", LockPath: lock, StateDir: filepath.Dir(lock)},
	}
}

func createParams() adapterproto.SessionCreateParams {
	return adapterproto.SessionCreateParams{
		Attempt: attempt, RuntimeEndpoint: endpoint, SessionEndpoint: endpoint, Preset: "default",
		FirstPrompt: adapterproto.FirstPrompt{Attempt: attempt, Text: "hello"},
		ToolAccess:  adapterproto.ToolAccess{Env: map[string]string{"SIDERA_TOKEN": token}},
	}
}

func (s *side) initialize(lock string) error {
	s.t.Helper()
	var res adapterproto.InitializeResult
	return s.call(adapterproto.MethodInitialize, initParams(lock), &res)
}

// The kit declares shutdown_intent itself, and hands the application the intent the
// daemon sent, or restart when it sent none (a daemon that does not know the
// member, or an adapter that did not declare it). An error of the application is
// the answer to shutdown.
func TestShutdownHandsTheApplicationItsIntent(t *testing.T) {
	cases := []struct {
		name   string
		params any
		want   adapterproto.ShutdownIntent
	}{
		{"no members", adapterproto.Empty{}, adapterproto.ShutdownRestart},
		{"restart", adapterproto.ShutdownParams{Intent: adapterproto.ShutdownRestart}, adapterproto.ShutdownRestart},
		{"stop", adapterproto.ShutdownParams{Intent: adapterproto.ShutdownStop}, adapterproto.ShutdownStop},
		{"retire", adapterproto.ShutdownParams{Intent: adapterproto.ShutdownRetire}, adapterproto.ShutdownRetire},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lock := filepath.Join(t.TempDir(), "lock")
			app := &stub{}
			s := serveOn(t, config(app, runtimeCaps()))
			var init adapterproto.InitializeResult
			if err := s.call(adapterproto.MethodInitialize, initParams(lock), &init); err != nil {
				t.Fatal(err)
			}
			if !init.Capabilities.ShutdownIntent {
				t.Error("the kit did not declare shutdown_intent")
			}
			if err := s.call(adapterproto.MethodShutdown, tc.params, &adapterproto.Empty{}); err != nil {
				t.Fatal(err)
			}
			if len(app.intents) != 1 || app.intents[0] != tc.want {
				t.Errorf("the application was handed %v, want [%s]", app.intents, tc.want)
			}
		})
	}
}

// An intent the protocol does not define is invalid params, and the adapter keeps
// serving; an error of the application is the answer, and the adapter ends anyway.
func TestShutdownRefusesAnUnknownIntentAndAnswersTheApplicationsError(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "lock")
	app := &stub{shutdownErr: peer.NewError(peer.CodeInternalError, "a process was left")}
	s := serveOn(t, config(app, runtimeCaps()))
	if err := s.initialize(lock); err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	if got := codeOf(t, s.call(adapterproto.MethodShutdown, map[string]string{"intent": "pause"}, &raw)); got != peer.CodeInvalidParams {
		t.Errorf("an unknown intent: code %d, want %d", got, peer.CodeInvalidParams)
	}
	if app.shutdowns != 0 {
		t.Error("the application was told to shut down by a request that was refused")
	}
	if err := s.call(adapterproto.MethodShutdown, adapterproto.ShutdownParams{Intent: adapterproto.ShutdownRetire}, &raw); err == nil || !strings.Contains(err.Error(), "a process was left") {
		t.Errorf("shutdown answered %v, want the error of the application", err)
	}
	if got := codeOf(t, s.call(adapterproto.MethodSessionCreate, createParams(), &raw)); got != peer.CodeShuttingDown {
		t.Errorf("after a shutdown the application failed: code %d, want %d", got, peer.CodeShuttingDown)
	}
}

// Before initialize only initialize, ping and the cancel notification are served:
// every other request, a known method or not, is not_initialized, and the
// application is not called.
func TestOnlyInitializePingAndCancelAreServedBeforeInitialize(t *testing.T) {
	app := &stub{}
	s := serveOn(t, config(app, runtimeCaps()))
	for _, method := range []string{adapterproto.MethodSessionCreate, adapterproto.MethodAdapterStart, adapterproto.MethodShutdown, "no.such_method"} {
		var raw json.RawMessage
		if got := codeOf(t, s.call(method, createParams(), &raw)); got != peer.CodeNotInitialized {
			t.Errorf("%s before initialize: code %d, want %d", method, got, peer.CodeNotInitialized)
		}
	}
	if err := s.call(adapterproto.MethodPing, adapterproto.Empty{}, &adapterproto.Empty{}); err != nil {
		t.Errorf("ping before initialize: %v", err)
	}
	if err := s.daemon.Notify(context.Background(), adapterproto.MethodCancelRequest, adapterproto.CancelRequestParams{RequestID: peer.NumberID(999)}); err != nil {
		t.Errorf("the cancel notification before initialize: %v", err)
	}
	if len(app.created) != 0 {
		t.Error("the application was called before initialize")
	}
}

// A version range with nothing in common is -32022 with both ranges, and it takes
// no lock and initializes nothing: a valid range after it still works.
func TestInitializeWithoutACommonVersionIsRefusedAndTakesNoLock(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "instance.lock")
	app := &stub{}
	s := serveOn(t, config(app, runtimeCaps()))
	p := initParams(lock)
	p.Protocol = adapterproto.Range{Min: 2, Max: 3}
	err := s.call(adapterproto.MethodInitialize, p, &adapterproto.InitializeResult{})
	var pe *peer.Error
	if !errors.As(err, &pe) || pe.Code != peer.CodeUnsupportedProtocolVersion {
		t.Fatalf("initialize with range 2..3: %v, want code %d", err, peer.CodeUnsupportedProtocolVersion)
	}
	var data adapterproto.UnsupportedVersionData
	if err := json.Unmarshal(pe.Data, &data); err != nil || data.Requested != p.Protocol || data.Supported != adapterproto.SupportedRange() {
		t.Errorf("error data %s (%v), want both ranges", pe.Data, err)
	}
	if app.instance.ID != "" {
		t.Error("the application was initialized for a version that is not supported")
	}
	if err := s.initialize(lock); err != nil {
		t.Fatalf("initialize with a range that fits: %v", err)
	}
}

// The lock is taken before the answer: a second adapter on the same lock file
// answers instance_locked and stays uninitialized, and once the first is gone the
// lock is free again.
func TestInitializeAnswersInstanceLockedWhileAnotherAdapterHoldsTheLock(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "instance.lock")
	first := serveOn(t, config(&stub{}, runtimeCaps()))
	if err := first.initialize(lock); err != nil {
		t.Fatal(err)
	}
	second := serveOn(t, config(&stub{}, runtimeCaps()))
	if got := codeOf(t, second.initialize(lock)); got != adapterproto.CodeInstanceLocked {
		t.Fatalf("second initialize: code %d, want %d", got, adapterproto.CodeInstanceLocked)
	}
	var raw json.RawMessage
	if got := codeOf(t, second.call(adapterproto.MethodAdapterStart, adapterproto.StartParams{}, &raw)); got != peer.CodeNotInitialized {
		t.Errorf("a refused adapter serves %d, want not_initialized %d", got, peer.CodeNotInitialized)
	}
	if err := first.end(); err != nil {
		t.Fatalf("Serve of the first adapter: %v", err)
	}
	if err := second.initialize(lock); err != nil {
		t.Fatalf("initialize after the lock was released: %v", err)
	}
}

// An application that refuses to initialize leaves no lock held, and its error is the answer.
func TestAnApplicationThatRefusesInitializeReleasesTheLock(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "instance.lock")
	bad := &stub{initErr: peer.NewError(adapterproto.CodeInstanceLocked, "no")}
	s := serveOn(t, config(bad, runtimeCaps()))
	if got := codeOf(t, s.initialize(lock)); got != adapterproto.CodeInstanceLocked {
		t.Fatalf("code %d, want the application's", got)
	}
	other := serveOn(t, config(&stub{}, runtimeCaps()))
	if err := other.initialize(lock); err != nil {
		t.Fatalf("the lock was left held: %v", err)
	}
}

// After shutdown only ping is served; everything else, initialize included, is
// shutting_down. The application is told once.
func TestAfterShutdownOnlyPingIsServed(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "l")
	app := &stub{}
	s := serveOn(t, config(app, runtimeCaps()))
	if err := s.initialize(lock); err != nil {
		t.Fatal(err)
	}
	if err := s.call(adapterproto.MethodShutdown, adapterproto.Empty{}, &adapterproto.Empty{}); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{adapterproto.MethodSessionCreate, adapterproto.MethodAdapterStart, adapterproto.MethodShutdown, adapterproto.MethodInitialize, "no.such_method"} {
		var raw json.RawMessage
		if got := codeOf(t, s.call(method, createParams(), &raw)); got != peer.CodeShuttingDown {
			t.Errorf("%s after shutdown: code %d, want %d", method, got, peer.CodeShuttingDown)
		}
	}
	if err := s.call(adapterproto.MethodPing, adapterproto.Empty{}, &adapterproto.Empty{}); err != nil {
		t.Errorf("ping after shutdown: %v", err)
	}
	if app.shutdowns != 1 {
		t.Errorf("the application was told of shutdown %d times, want once", app.shutdowns)
	}
}

// What a request carries and what an application answers are held to the protocol:
// parameters that break the schema are -32602 and never reach the application, a
// result that breaks it is an internal error and never reaches the daemon, a method
// the daemon may call but the adapter has no capability for is
// capability_not_declared, and a second initialize is invalid.
func TestRequestsAndAnswersAreHeldToTheProtocol(t *testing.T) {
	dir := t.TempDir()
	app := &stub{}
	s := serveOn(t, config(app, runtimeCaps()))
	if err := s.initialize(filepath.Join(dir, "runtime.lock")); err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	if got := codeOf(t, s.call(adapterproto.MethodSessionCreate, json.RawMessage(`{"attempt":"x","extra":1}`), &raw)); got != peer.CodeInvalidParams {
		t.Errorf("params with an unknown field: code %d, want %d", got, peer.CodeInvalidParams)
	}
	if len(app.created) != 0 {
		t.Error("invalid params reached the application")
	}
	app.badResult = true
	if got := codeOf(t, s.call(adapterproto.MethodSessionCreate, createParams(), &raw)); got != peer.CodeInternalError {
		t.Errorf("an invalid result: code %d, want %d", got, peer.CodeInternalError)
	}
	if got := codeOf(t, s.initialize(filepath.Join(dir, "other.lock"))); got != peer.CodeInvalidRequest {
		t.Errorf("a second initialize: code %d, want %d", got, peer.CodeInvalidRequest)
	}

	direct := serveOn(t, config(&stub{}, adapterproto.Capabilities{Direct: &adapterproto.DirectCapabilities{MaxEndpoints: 1}}))
	if err := direct.initialize(filepath.Join(dir, "direct.lock")); err != nil {
		t.Fatal(err)
	}
	if got := codeOf(t, direct.call(adapterproto.MethodSessionCreate, createParams(), &raw)); got != adapterproto.CodeCapabilityNotDeclared {
		t.Errorf("session.create without a runtime: code %d, want %d", got, adapterproto.CodeCapabilityNotDeclared)
	}
}

// The kit declares that it decodes the feedback member of a page, so that the
// daemon sends it to every adapter built on the kit, and does not change the
// declaration it was given. An adapter without direct endpoints declares none.
func TestTheKitDeclaresFeedbackForDirectEndpoints(t *testing.T) {
	given := adapterproto.Capabilities{Direct: &adapterproto.DirectCapabilities{MaxEndpoints: 1}}
	var init adapterproto.InitializeResult
	s := serveOn(t, config(&stub{}, given))
	if err := s.call(adapterproto.MethodInitialize, initParams(filepath.Join(t.TempDir(), "lock")), &init); err != nil {
		t.Fatal(err)
	}
	if init.Capabilities.Direct == nil || !init.Capabilities.Direct.Feedback {
		t.Errorf("the kit did not declare direct.feedback: %+v", init.Capabilities.Direct)
	}
	if given.Direct.Feedback {
		t.Error("the kit changed the declaration it was given")
	}

	var runtimeOnly adapterproto.InitializeResult
	s = serveOn(t, config(&stub{}, runtimeCaps()))
	if err := s.call(adapterproto.MethodInitialize, initParams(filepath.Join(t.TempDir(), "lock")), &runtimeOnly); err != nil {
		t.Fatal(err)
	}
	if runtimeOnly.Capabilities.Direct != nil {
		t.Errorf("the kit declared direct endpoints for a runtime adapter: %+v", runtimeOnly.Capabilities.Direct)
	}
}

// The methods of attached sessions are answered only for an adapter that declared
// attach, even when it declared a runtime.
func TestAttachMethodsNeedTheAttachCapability(t *testing.T) {
	dir := t.TempDir()
	params := adapterproto.SessionAttachParams{Endpoint: endpoint, NativeRef: "native-1"}
	for _, tc := range []struct {
		name     string
		attach   bool
		wantCode int
	}{
		{"declared", true, 0},
		{"runtime without attach", false, adapterproto.CodeCapabilityNotDeclared},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caps := runtimeCaps()
			caps.Runtime.Attach = tc.attach
			app := &stub{}
			s := serveOn(t, config(app, caps))
			if err := s.initialize(filepath.Join(dir, tc.name+".lock")); err != nil {
				t.Fatal(err)
			}
			var res adapterproto.SessionAttachResult
			err := s.call(adapterproto.MethodSessionAttach, params, &res)
			if tc.wantCode == 0 {
				if err != nil || res.Outcome != adapterproto.SessionAttachAttached || len(app.attached) != 1 {
					t.Fatalf("session.attach = %+v, %v; the application saw %d calls", res, err, len(app.attached))
				}
				return
			}
			if got := codeOf(t, err); got != tc.wantCode {
				t.Errorf("session.attach: code %d, want %d", got, tc.wantCode)
			}
			if len(app.attached) != 0 {
				t.Error("an undeclared session.attach reached the application")
			}
		})
	}
}

// An adapter.start that lists an attached session is refused for an adapter that
// did not declare attach, before the application sees it; one without an attached
// entry is served as before.
func TestStartWithAnAttachedSessionNeedsTheAttachCapability(t *testing.T) {
	dir := t.TempDir()
	start := func(attached bool) adapterproto.StartParams {
		entry := adapterproto.SessionAttach{Endpoint: endpoint, NativeRef: "native-1"}
		if attached {
			entry.Attached = true
		} else {
			entry.Preset = "default"
			entry.ToolAccess = &adapterproto.ToolAccess{Env: map[string]string{"SIDERA_TOKEN": token}}
		}
		return adapterproto.StartParams{
			Node: "node", Config: adapterproto.ConfigValues{}, Presets: []adapterproto.PresetConfig{},
			Sessions: []adapterproto.SessionAttach{entry}, DirectEndpoints: []adapterproto.DirectAttach{},
		}
	}
	for _, tc := range []struct {
		name     string
		attach   bool
		attached bool
		wantCode int
	}{
		{"attached entry, declared", true, true, 0},
		{"attached entry, runtime without attach", false, true, adapterproto.CodeCapabilityNotDeclared},
		{"created entry, runtime without attach", false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caps := runtimeCaps()
			caps.Runtime.Attach = tc.attach
			app := &stub{}
			s := serveOn(t, config(app, caps))
			if err := s.initialize(filepath.Join(dir, tc.name+".lock")); err != nil {
				t.Fatal(err)
			}
			var res adapterproto.StartResult
			err := s.call(adapterproto.MethodAdapterStart, start(tc.attached), &res)
			if tc.wantCode == 0 {
				if err != nil || len(app.started) != 1 {
					t.Fatalf("adapter.start = %+v, %v; the application saw %d calls", res, err, len(app.started))
				}
				return
			}
			if got := codeOf(t, err); got != tc.wantCode {
				t.Errorf("adapter.start: code %d, want %d", got, tc.wantCode)
			}
			if len(app.started) != 0 {
				t.Error("an attached entry reached an application that did not declare attach")
			}
		})
	}
}

// The happy path: initialize answers the identity and capabilities of the adapter,
// the application receives the instance and the typed calls, and what it sends
// through Conn reaches the daemon after validation.
func TestAServedAdapterAnswersAndSends(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "state", "instance.lock")
	app := &stub{}
	s := serveOn(t, config(app, runtimeCaps()))
	var init adapterproto.InitializeResult
	if err := s.call(adapterproto.MethodInitialize, initParams(lock), &init); err != nil {
		t.Fatal(err)
	}
	if init.ProtocolVersion != 1 || init.Adapter.ID != "stub" || init.Capabilities.Runtime == nil {
		t.Errorf("initialize answered %+v", init)
	}
	if app.instance.LockPath != lock || app.instance.ID != "work" {
		t.Errorf("the application got the instance %+v", app.instance)
	}

	var start adapterproto.StartResult
	startParams := adapterproto.StartParams{
		Node: "node", Config: adapterproto.ConfigValues{}, Presets: []adapterproto.PresetConfig{},
		Sessions: []adapterproto.SessionAttach{}, DirectEndpoints: []adapterproto.DirectAttach{},
	}
	if err := s.call(adapterproto.MethodAdapterStart, startParams, &start); err != nil || start.Sessions == nil {
		t.Errorf("adapter.start: %+v, %v", start, err)
	}
	var created adapterproto.SessionCreateResult
	if err := s.call(adapterproto.MethodSessionCreate, createParams(), &created); err != nil || created.NativeRef != "native-1" {
		t.Errorf("session.create: %+v, %v", created, err)
	}

	end := adapterproto.TurnEndedParams{Endpoint: endpoint, Turn: "t1", Final: adapterproto.TurnCompleted, AtMS: 1}
	if err := app.conn.TurnEnded(context.Background(), end); err != nil {
		t.Fatal(err)
	}
	if err := app.conn.TurnEnded(context.Background(), adapterproto.TurnEndedParams{}); err == nil {
		t.Error("an invalid notification was sent")
	}
	if err := app.conn.Notify(context.Background(), adapterproto.MethodSessionCreate, adapterproto.Empty{}); err == nil {
		t.Error("a method only the daemon sends was sent")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.mu.Lock()
		n := len(s.notes)
		s.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon saw %d notifications, want 1", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Serve ends with nil when the daemon closes the stream and with the error of the
// context when the context ends.
func TestServeEndsWithTheStreamOrTheContext(t *testing.T) {
	s := serveOn(t, config(&stub{}, runtimeCaps()))
	if err := s.end(); err != nil {
		t.Errorf("Serve after the daemon closed the stream: %v", err)
	}

	aR, dW := io.Pipe()
	dR, aW := io.Pipe()
	defer dW.Close()
	defer dR.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapterkit.Serve(ctx, aR, aW, config(&stub{}, runtimeCaps())) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Serve after the context ended: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return when the context ended")
	}
}

// A configuration that would make initialize answer something invalid is refused
// before anything is served.
func TestServeRefusesAnInvalidIdentity(t *testing.T) {
	cfg := config(&stub{}, runtimeCaps())
	cfg.Adapter.ID = "Not Valid"
	r, w := io.Pipe()
	defer r.Close()
	if err := adapterkit.Serve(context.Background(), r, w, cfg); err == nil {
		t.Error("an invalid adapter identity was served")
	}
	cfg = config(nil, runtimeCaps())
	if err := adapterkit.Serve(context.Background(), r, w, cfg); err == nil {
		t.Error("a nil application was served")
	}
}

// deadlineApp records how long the contexts of Start and Probe have left.
type deadlineApp struct {
	adapterkit.Base
	left chan time.Duration
}

func (a *deadlineApp) record(ctx context.Context) {
	d, ok := ctx.Deadline()
	if !ok {
		a.left <- -1
		return
	}
	a.left <- time.Until(d)
}

func (a *deadlineApp) Start(ctx context.Context, p adapterproto.StartParams) (adapterproto.StartResult, error) {
	a.record(ctx)
	return a.Base.Start(ctx, p)
}

func (a *deadlineApp) Probe(ctx context.Context, p adapterproto.ProbeParams) (adapterproto.ProbeResult, error) {
	a.record(ctx)
	return a.Base.Probe(ctx, p)
}

// Start and Probe must answer inside the limit the daemon waits for a handshake
// call, so their contexts end a little before it, counted from the request.
func TestStartAndProbeContextsEndBeforeTheHandshakeLimit(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "work.lock")
	app := &deadlineApp{left: make(chan time.Duration, 2)}
	s := serveOn(t, config(app, adapterproto.Capabilities{}))
	if err := s.initialize(lock); err != nil {
		t.Fatal(err)
	}
	start := adapterproto.StartParams{
		Node: "node", Config: adapterproto.ConfigValues{}, Presets: []adapterproto.PresetConfig{},
		Sessions: []adapterproto.SessionAttach{}, DirectEndpoints: []adapterproto.DirectAttach{},
	}
	var started adapterproto.StartResult
	if err := s.call(adapterproto.MethodAdapterStart, start, &started); err != nil {
		t.Fatal(err)
	}
	var probed adapterproto.ProbeResult
	if err := s.call(adapterproto.MethodAdapterProbe, adapterproto.ProbeParams{ToolProbes: []adapterproto.ToolProbeRequest{}}, &probed); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{adapterproto.MethodAdapterStart, adapterproto.MethodAdapterProbe} {
		left := <-app.left
		if want := adapterproto.HandshakeLimit; left <= 0 || left >= want-time.Second || left < want-5*time.Second {
			t.Errorf("%s: the context has %v left, want a little under %v", method, left, want)
		}
	}
}
