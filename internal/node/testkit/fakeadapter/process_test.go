package fakeadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// procFixture is what the binary layer's fixture does with the fake programs: a
// control server, shared directories, a test clock, and a daemon side that talks
// to the adapter program over its standard input and output.
type procFixture struct {
	t       *testing.T
	clock   *clocktest.Fake
	srv     *fakectl.Server
	cfg     fakeadapter.ProcessConfig
	ctrl    fakeruntime.RemoteController
	lock    string
	started []*procAdapter
}

func newProcFixture(t *testing.T) *procFixture {
	t.Helper()
	leakGuard(t)
	_, runtimeBin := programs(t)
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := fakectl.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f := &procFixture{t: t, clock: clocktest.NewFake(testStart), srv: srv, lock: filepath.Join(t.TempDir(), "instance.lock")}
	f.cfg = fakeadapter.ProcessConfig{
		InstanceID: "fake", Manifest: fakeadapter.DefaultManifestOptions(),
		LedgerDir: filepath.Join(t.TempDir(), "ledger"), RuntimeDir: t.TempDir(), RuntimeBinary: runtimeBin, RuntimeSeed: 9,
		ClockStartMS: testStart.UnixMilli(), Control: srv.Addr(),
		// As in the in-process rig, the first prompt waits for the script; see
		// procFixture.settle.
		RuntimeFirstTurn: fakeruntime.KnobOf(fakeruntime.TurnPlan{ManualStart: true}),
	}
	f.ctrl = fakeruntime.RemoteController{Server: srv}
	t.Cleanup(func() {
		for _, a := range f.started {
			a.stop()
		}
		_ = srv.Close()
		cancel()
		files, _ := filepath.Glob(filepath.Join(f.cfg.RuntimeDir, "*.json"))
		for _, file := range files {
			raw, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			if info, err := fakeruntime.DecodeProcessInfo(raw); err == nil {
				if p, err := os.FindProcess(int(info.PID)); err == nil {
					_ = p.Kill()
				}
			}
		}
	})
	return f
}

// settle gives the session an ordinary plan once it exists, so that the tests that
// drive turns by hand see a prompt start a turn.
func (f *procFixture) settle(ref string) {
	f.t.Helper()
	if err := f.ctrl.SetPlan(ref, fakeruntime.TurnPlan{}); err != nil {
		f.t.Fatal(err)
	}
}

// procAdapter is one run of the adapter program with the daemon side attached.
type procAdapter struct {
	f       *procFixture
	cmd     *exec.Cmd
	daemon  *peer.Peer
	conn    *fakectl.Conn
	exited  chan struct{} // closed when the program has ended
	exitErr error

	mu       sync.Mutex
	notified []notification
	observed int
	changed  chan struct{}
}

func (f *procFixture) startAdapter() *procAdapter {
	f.t.Helper()
	adapterBin, _ := programs(f.t)
	raw, err := json.Marshal(f.cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	cmd := exec.Command(adapterBin)
	cmd.Env = append(os.Environ(), fakeadapter.ConfigEnv+"="+string(raw))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		f.t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		f.t.Fatal(err)
	}
	cmd.Stderr = testWriter{f.t}
	if err := cmd.Start(); err != nil {
		f.t.Fatalf("start the adapter program: %v", err)
	}
	a := &procAdapter{f: f, cmd: cmd, exited: make(chan struct{}), changed: make(chan struct{}, 1)}
	go func() { a.exitErr = cmd.Wait(); close(a.exited) }()
	d, err := peer.New(stdout, writeCloser{stdin}, peer.Config{})
	if err != nil {
		f.t.Fatal(err)
	}
	a.daemon = d
	d.Handle(adapterproto.MethodCheckpointPut, func(context.Context, json.RawMessage) (any, error) { return adapterproto.Empty{}, nil })
	d.Handle(adapterproto.MethodRecvObserved, func(context.Context, json.RawMessage) (any, error) {
		a.mu.Lock()
		a.observed++
		a.mu.Unlock()
		return adapterproto.RecvObservedResult{Verdict: adapterproto.AckRefused, Reason: adapterproto.AckUnknownLease, Items: []adapterproto.PartAck{}}, nil
	})
	for _, m := range []string{adapterproto.MethodTurnStarted, adapterproto.MethodTurnEnded, adapterproto.MethodAdapterAvailability, adapterproto.MethodPromptPersisted} {
		method := m
		d.HandleNotification(method, func(_ context.Context, params json.RawMessage) {
			a.mu.Lock()
			a.notified = append(a.notified, notification{method, bytes.Clone(params)})
			a.mu.Unlock()
			select {
			case a.changed <- struct{}{}:
			default:
			}
		})
	}
	d.Start()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	conn, err := f.srv.Wait(ctx, "fake")
	if err != nil {
		f.t.Fatalf("the adapter program never joined the control channel: %v", err)
	}
	a.conn = conn
	f.started = append(f.started, a)
	return a
}

type writeCloser struct{ io.WriteCloser }

func (a *procAdapter) stop() {
	_ = a.daemon.Close()
	if a.cmd.Process != nil {
		_ = a.cmd.Process.Kill()
	}
	select {
	case <-a.exited:
	case <-time.After(waitLimit):
	}
}

func (a *procAdapter) call(method string, params any, out adapterproto.Message) error {
	a.f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	var raw json.RawMessage
	if err := a.daemon.Call(ctx, method, params, &raw); err != nil {
		return err
	}
	if out != nil {
		if err := adapterproto.Decode(raw, out); err != nil {
			a.f.t.Fatalf("%s result: %v", method, err)
		}
	}
	return nil
}

func (a *procAdapter) initialize() {
	a.f.t.Helper()
	var res adapterproto.InitializeResult
	if err := a.call(adapterproto.MethodInitialize, adapterproto.InitializeParams{
		Protocol: adapterproto.SupportedRange(), Daemon: adapterproto.DaemonInfo{Version: "0.1.0"},
		Instance:     adapterproto.InstanceInfo{ID: "fake", LockPath: a.f.lock},
		Capabilities: adapterproto.DaemonCapabilities{},
	}, &res); err != nil {
		a.f.t.Fatalf("initialize: %v", err)
	}
}

func (a *procAdapter) waitNotifications(method string, n int) []json.RawMessage {
	a.f.t.Helper()
	deadline := time.Now().Add(waitLimit)
	for {
		a.mu.Lock()
		var got []json.RawMessage
		for _, x := range a.notified {
			if x.Method == method {
				got = append(got, x.Params)
			}
		}
		a.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			a.f.t.Fatalf("timed out waiting for %d %s notifications", n, method)
		}
		select {
		case <-a.changed:
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (a *procAdapter) createParams(n int, ep string) adapterproto.SessionCreateParams {
	return adapterproto.SessionCreateParams{
		Attempt: attemptID(n), RuntimeEndpoint: "rt-1", SessionEndpoint: adapterproto.EndpointRef(ep), Preset: "default",
		FirstPrompt: adapterproto.FirstPrompt{Attempt: attemptID(n), Text: "start working"},
		ToolAccess:  adapterproto.ToolAccess{Env: map[string]string{"SIDERA_TOKEN": "t"}},
	}
}

// TestProcessAdapterDrivesARuntimeProcessAndTakesItBackAfterARestart is the
// binary layer in miniature: an adapter program creates a session in a runtime
// program of its own; the test clock and the script step reach both; the adapter
// is killed, the runtime lives on; a second adapter program takes the session back
// and hears the turns of the runtime that never stopped; and when the native
// process is killed the session is lost, with its record still readable.
func TestProcessAdapterDrivesARuntimeProcessAndTakesItBackAfterARestart(t *testing.T) {
	f := newProcFixture(t)
	a1 := f.startAdapter()
	a1.initialize()

	if err := a1.conn.SetStep(context.Background(), "create the session"); err != nil {
		t.Fatal(err)
	}
	var created adapterproto.SessionCreateResult
	if err := a1.call(adapterproto.MethodSessionCreate, a1.createParams(1, "sess-1"), &created); err != nil {
		t.Fatalf("session.create: %v", err)
	}
	if created.Outcome != adapterproto.CreateCreated {
		t.Fatalf("create: %+v", created)
	}
	ref := string(created.NativeRef)
	f.settle(ref)
	if created.Process == nil || created.Process.PID == int64(os.Getpid()) || created.Process.PID == int64(a1.cmd.Process.Pid) {
		t.Fatalf("the native process must be a process of its own: %+v (adapter %d, test %d)", created.Process, a1.cmd.Process.Pid, os.Getpid())
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	rconn, err := f.srv.Wait(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}

	// The clock of every process is the fixture's: the daemon's first, the fakes' after.
	f.clock.Advance(time.Hour)
	for _, c := range []*fakectl.Conn{a1.conn, rconn} {
		if err := c.SetClock(ctx, f.clock.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	var delivered adapterproto.PromptDeliverResult
	if err := a1.call(adapterproto.MethodPromptDeliver, adapterproto.PromptDeliverParams{Endpoint: "sess-1", Attempt: attemptID(2), Text: "work"}, &delivered); err != nil {
		t.Fatal(err)
	}
	raw := a1.waitNotifications(adapterproto.MethodTurnStarted, 1)[0]
	var ts adapterproto.TurnStartedParams
	if err := adapterproto.Decode(raw, &ts); err != nil {
		t.Fatal(err)
	}
	if ts.AtMS != f.clock.Now().UnixMilli() || ts.Attempt != attemptID(2) || ts.Process.PID != created.Process.PID {
		t.Fatalf("turn.started %+v: want the test clock %d, attempt 2, the runtime's pid", ts, f.clock.Now().UnixMilli())
	}
	if err := f.ctrl.EndTurn(ref, fakeruntime.FinalCompleted); err != nil {
		t.Fatal(err)
	}
	a1.waitNotifications(adapterproto.MethodTurnEnded, 1)

	// Kill the adapter. The runtime must not notice.
	a1.stop()
	if !f.ctrl.Idle() {
		t.Fatal("the runtime process is not answering after its adapter was killed")
	}

	// A second adapter program, the same instance, takes the session back.
	a2 := f.startAdapter()
	a2.initialize()
	var started adapterproto.StartResult
	if err := a2.call(adapterproto.MethodAdapterStart, adapterproto.StartParams{
		Node: "node-1", RuntimeEndpoint: "rt-1", Config: adapterproto.ConfigValues{}, Presets: []adapterproto.PresetConfig{},
		Sessions: []adapterproto.SessionAttach{{
			Endpoint: "sess-1", Preset: "default", NativeRef: created.NativeRef, Process: created.Process,
			ToolAccess: adapterproto.ToolAccess{Env: map[string]string{}},
		}},
		DirectEndpoints: []adapterproto.DirectAttach{},
	}, &started); err != nil {
		t.Fatal(err)
	}
	if len(started.Sessions) != 1 || started.Sessions[0].State != adapterproto.AttachAttached || started.Sessions[0].TurnActive {
		t.Fatalf("adapter.start after the restart: %+v", started)
	}
	if started.Sessions[0].Process == nil || started.Sessions[0].Process.PID != created.Process.PID || started.Sessions[0].Process.StartKey != created.Process.StartKey {
		t.Fatalf("the session was taken back as another process: %+v, created %+v", started.Sessions[0].Process, created.Process)
	}
	// The runtime that never stopped now tells the new adapter about its turns.
	if err := f.ctrl.StartTurn(ref); err != nil {
		t.Fatal(err)
	}
	a2.waitNotifications(adapterproto.MethodTurnStarted, 1)
	if err := f.ctrl.EndTurn(ref, fakeruntime.FinalCompleted); err != nil {
		t.Fatal(err)
	}
	a2.waitNotifications(adapterproto.MethodTurnEnded, 1)
	// The same attempt, repeated to the new adapter, is the same session: no second one.
	var again adapterproto.SessionCreateResult
	if err := a2.call(adapterproto.MethodSessionCreate, a2.createParams(1, "sess-1"), &again); err != nil || again.NativeRef != created.NativeRef {
		t.Fatalf("the repeated attempt: %+v, %v", again, err)
	}

	// The native process dies. The session is lost; its record says what it held.
	if err := f.ctrl.KillNative(ref); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the runtime process to be gone", func() bool {
		var res adapterproto.PromptDeliverResult
		err := a2.call(adapterproto.MethodPromptDeliver, adapterproto.PromptDeliverParams{Endpoint: "sess-1", Attempt: attemptID(3), Text: "late"}, &res)
		return err == nil && res.Stage == adapterproto.PromptRefused && res.Reason == adapterproto.RefusalSessionLost
	})
	var rec adapterproto.ReconcileResult
	if err := a2.call(adapterproto.MethodPromptReconcile, adapterproto.ReconcileParams{Endpoint: "sess-1", Attempt: attemptID(2)}, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Verdict != adapterproto.VerdictFound {
		t.Fatalf("reconcile of a delivered attempt on the dead session: %+v", rec)
	}
	var absent adapterproto.ReconcileResult
	if err := a2.call(adapterproto.MethodPromptReconcile, adapterproto.ReconcileParams{Endpoint: "sess-1", Attempt: attemptID(77)}, &absent); err != nil {
		t.Fatal(err)
	}
	if absent.Verdict != adapterproto.VerdictProvablyAbsent {
		t.Fatalf("reconcile of an attempt that never arrived on the dead session: %+v", absent)
	}

	// The ledger, written by four processes, reads back whole.
	fixtureLedger, err := ledger.NewDir(f.cfg.LedgerDir).Open("fixture", 0, f.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixtureLedger.Append(ledger.KindFixtureKill, ledger.Attrs{PIDs: []int64{int64(a1.cmd.Process.Pid)}}); err != nil {
		t.Fatal(err)
	}
	log, err := ledger.ReadDir(f.cfg.LedgerDir)
	if err != nil {
		t.Fatalf("the ledger of the processes is damaged: %v", err)
	}
	if n := len(log.Kind(ledger.KindRuntimeSessionCreated)); n != 1 {
		t.Fatalf("%d native sessions for one attempt across a restart, want 1", n)
	}
	if n := len(log.Kind(ledger.KindAdapterLockAcquired)); n != 2 {
		t.Fatalf("%d adapter.lock.acquired entries, want one per adapter process", n)
	}
	pids := map[int64]bool{}
	for _, e := range log.Writer("adapter/fake") {
		pids[e.PID] = true
	}
	if len(pids) != 2 {
		t.Fatalf("the adapter wrote as %d processes, want 2", len(pids))
	}
	stepped := false
	for _, e := range log.Writer("adapter/fake") {
		if e.Kind == ledger.KindSessionCreateReceived && e.Step == "create the session" {
			stepped = true
		}
	}
	if !stepped {
		t.Fatal("the entry written during the announced step does not carry the step")
	}
	var runtimeAt int64
	for _, e := range log.Kind(ledger.KindRuntimeTurnStarted) {
		if e.AtMS > runtimeAt {
			runtimeAt = e.AtMS
		}
	}
	if runtimeAt != f.clock.Now().UnixMilli() {
		t.Fatalf("the runtime process wrote turn times from %d, want the test clock %d", runtimeAt, f.clock.Now().UnixMilli())
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestProcessAdapterEndsWhenItsControlConnectionEnds(t *testing.T) {
	f := newProcFixture(t)
	a := f.startAdapter()
	a.initialize()
	if err := f.srv.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.exited:
		if a.exitErr != nil {
			t.Fatalf("the adapter program ended with %v: ending because its fixture is gone is not a failure", a.exitErr)
		}
	case <-time.After(waitLimit):
		t.Fatal("the adapter program is still running although its fixture is gone")
	}
}

func TestProcessAdapterKnobsOverTheControlChannel(t *testing.T) {
	f := newProcFixture(t)
	a := f.startAdapter()
	a.initialize()
	k := fakeadapter.RemoteKnobs{Conn: a.conn}
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()

	// A one-shot outcome.
	if err := k.QueueOutcome(ctx, fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeFail, Reason: "preset_unknown"}); err != nil {
		t.Fatal(err)
	}
	var res adapterproto.SessionCreateResult
	if err := a.call(adapterproto.MethodSessionCreate, a.createParams(1, "sess-1"), &res); err != nil || res.Reason != adapterproto.CreateFailPresetUnknown {
		t.Fatalf("create with the outcome: %+v, %v", res, err)
	}
	// A misuse is refused, as an error of the call.
	if err := k.QueueOutcome(ctx, fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeAccepted}); err == nil {
		t.Fatal("an outcome a creation does not take was accepted")
	}
	// A pause point.
	if err := k.PauseAt(ctx, fakeadapter.PauseCreateAfterNative); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		var r adapterproto.SessionCreateResult
		done <- a.call(adapterproto.MethodSessionCreate, a.createParams(2, "sess-2"), &r)
	}()
	if err := k.WaitPaused(ctx, fakeadapter.PauseCreateAfterNative); err != nil {
		t.Fatal(err)
	}
	st, err := k.Stats(ctx)
	if err != nil || !reflect.DeepEqual(st.Held, []string{adapterproto.MethodSessionCreate}) || !reflect.DeepEqual(st.HeldPoints, []string{fakeadapter.PauseCreateAfterNative}) {
		t.Fatalf("Stats while paused: %+v, %v; want the method held, and the point it stopped at", st, err)
	}
	// The process also says that it stopped, over the control channel.
	rep, err := a.conn.NextReport(ctx)
	if err != nil || rep.Name != "paused" || rep.Detail != fakeadapter.PauseCreateAfterNative {
		t.Fatalf("report %+v, %v: want paused at create.after-native", rep, err)
	}
	if err := k.Release(ctx, fakeadapter.PauseCreateAfterNative); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("the paused create after release: %v", err)
	}
	// A notification the test makes the adapter send.
	if err := k.Availability(ctx, []adapterproto.Availability{{Condition: adapterproto.ConditionRuntimeAuth, SinceMS: testStart.UnixMilli()}}); err != nil {
		t.Fatal(err)
	}
	a.waitNotifications(adapterproto.MethodAdapterAvailability, 1)
	if st, _ := k.Stats(ctx); st.NotificationsSent != 1 {
		t.Fatalf("Stats: %+v, want one notification sent", st)
	}
	// Disconnect ends the program's protocol stream, and so the program.
	if err := k.Disconnect(ctx); err != nil && !errors.Is(err, peer.ErrClosed) {
		t.Fatalf("Disconnect: %v", err)
	}
	select {
	case <-a.exited:
	case <-time.After(waitLimit):
		t.Fatal("the adapter program did not end after Disconnect")
	}
}

// --- the decoders of the process interface ---

var goldenArgs = []struct {
	name  string
	knob  string
	json  string
	value any
}{
	{"outcome", fakeadapter.KnobOutcome, `{"request":"create","mode":"queue","kind":"fail","reason":"preset_unknown"}`, fakeadapter.OutcomeArgs{Request: "create", Mode: "queue", Kind: "fail", Reason: "preset_unknown"}},
	{"outcome with a code after the effect", fakeadapter.KnobOutcome, `{"request":"prompt","mode":"set","kind":"error","code":-32603,"after_effect":true}`, fakeadapter.OutcomeArgs{Request: "prompt", Mode: "set", Kind: "error", Code: -32603, AfterEffect: true}},
	{"pause points", fakeadapter.KnobPauseAt, `{"points":["create.after-native","start.before-reply"]}`, fakeadapter.PointsArgs{Points: []string{"create.after-native", "start.before-reply"}}},
	{"one pause point", fakeadapter.KnobPauseWait, `{"point":"close.after-forward"}`, fakeadapter.PointArgs{Point: "close.after-forward"}},
	{"observed mode", fakeadapter.KnobObserved, `{"mode":"truncated"}`, fakeadapter.ObservedArgs{Mode: "truncated"}},
	{"availability", fakeadapter.KnobAvailability, `{"conditions":[{"condition":"runtime_auth","since_ms":5}]}`, fakeadapter.AvailabilityArgs{Conditions: []adapterproto.Availability{{Condition: adapterproto.ConditionRuntimeAuth, SinceMS: 5}}}},
	{"persist", fakeadapter.KnobPersist, `{"endpoint":"sess-1","attempt":"00000000-0000-4000-8000-000000000003"}`, fakeadapter.PersistArgs{Endpoint: "sess-1", Attempt: "00000000-0000-4000-8000-000000000003"}},
	{"native request", fakeadapter.KnobNativeReq, `{"endpoint":"sess-1","method":"item/tool/call","disposition":"preset"}`, fakeadapter.NativeRequestArgs{Endpoint: "sess-1", Method: "item/tool/call", Disposition: adapterproto.DispositionPreset}},
	{"stats reply", fakeadapter.KnobStats + ".reply", `{"notifications_sent":3,"outstanding":1,"held":["session.create"],"held_points":["create.after-native"]}`, fakeadapter.StatsReply{NotificationsSent: 3, Outstanding: 1, Held: []string{"session.create"}, HeldPoints: []string{"create.after-native"}}},
}

func TestGoldenKnobArguments(t *testing.T) {
	for _, g := range goldenArgs {
		t.Run(g.name, func(t *testing.T) {
			got, err := fakeadapter.DecodeKnobArgs(g.knob, []byte(g.json))
			if err != nil {
				t.Fatalf("DecodeKnobArgs: %v", err)
			}
			if !reflect.DeepEqual(got, g.value) {
				t.Fatalf("decoded %#v, want %#v", got, g.value)
			}
			enc, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(enc, []byte(g.json)) {
				t.Fatalf("encoded %s, want %s", enc, g.json)
			}
		})
	}
}

func TestKnobArgumentsReject(t *testing.T) {
	rows := []struct{ rule, knob, json string }{
		{"an unknown member", fakeadapter.KnobOutcome, `{"request":"create","mode":"set","kind":"hold","extra":1}`},
		{"a request that is not one", fakeadapter.KnobOutcome, `{"request":"dance","mode":"set","kind":"hold"}`},
		{"a mode that is neither set nor queue", fakeadapter.KnobOutcome, `{"request":"create","mode":"always","kind":"hold"}`},
		{"a kind that is not an outcome", fakeadapter.KnobOutcome, `{"request":"create","mode":"set","kind":"explode"}`},
		{"an outcome a request does not take", fakeadapter.KnobOutcome, `{"request":"create","mode":"set","kind":"accepted"}`},
		{"an error without a code", fakeadapter.KnobOutcome, `{"request":"create","mode":"set","kind":"error"}`},
		{"no pause point", fakeadapter.KnobPauseAt, `{"points":[]}`},
		{"a pause point that does not exist", fakeadapter.KnobPauseAt, `{"points":["nowhere"]}`},
		{"an observed mode that does not exist", fakeadapter.KnobObserved, `{"mode":"garbled"}`},
		{"a condition that is not one", fakeadapter.KnobAvailability, `{"conditions":[{"condition":"sulking"}]}`},
		{"an attempt that is not a UUID v4", fakeadapter.KnobPersist, `{"endpoint":"e","attempt":"a"}`},
		{"a disposition that is not one", fakeadapter.KnobNativeReq, `{"endpoint":"e","method":"m","disposition":"shrug"}`},
		{"a knob that takes no arguments", fakeadapter.KnobHang, `{}`},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if v, err := fakeadapter.DecodeKnobArgs(r.knob, []byte(r.json)); err == nil {
				t.Fatalf("accepted %s as %#v", r.json, v)
			}
		})
	}
}

func FuzzDecodeKnobArguments(f *testing.F) {
	for _, g := range goldenArgs {
		f.Add(g.knob, []byte(g.json))
	}
	f.Fuzz(func(t *testing.T, knob string, data []byte) {
		v, err := fakeadapter.DecodeKnobArgs(knob, data)
		if err != nil {
			return
		}
		enc, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("a decoded value does not encode: %v", err)
		}
		again, err := fakeadapter.DecodeKnobArgs(knob, enc)
		if err != nil || !reflect.DeepEqual(v, again) {
			t.Fatalf("decode, encode, decode: %v\n%#v\n%#v", err, v, again)
		}
	})
}

func TestGoldenProcessConfig(t *testing.T) {
	cfg := fakeadapter.ProcessConfig{
		InstanceID: "fake", Manifest: fakeadapter.DefaultManifestOptions(), LedgerDir: "/tmp/l", RuntimeDir: "/tmp/r",
		RuntimeBinary: "/tmp/sidera-fake-runtime", RuntimeSeed: 9, ClockStartMS: 1792832400000, Control: "127.0.0.1:5555",
		RuntimeTestSeed: "base", RuntimeFirstTurn: fakeruntime.PlanKnob{ManualStart: true},
	}
	cfg.Manifest.Program = "bin/sidera-fake-adapter" // the default has the extension of the platform
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"instance_id":"fake","manifest":{"id":"fake","version":"0.1.0","runtime":true,"direct":true,"frontends":["cli","mcp"],"interrupt":true,"ack_on_behalf":true,"max_direct_endpoints":10,"recv_wait_s":{"cli":20,"mcp":45},"budgets":[{"frontend":"cli","unit":"chars","limit":27000},{"frontend":"mcp","unit":"tokens","limit":22500},{"frontend":"direct","unit":"chars","limit":9000}],"runtime_name":"fake-runtime","runtime_version":"1.0.0","tested":[{"runtime":"fake-runtime","min":"1.0.0","max":"1.9.9"}],"ack_markers":{"replaced":[{"literal":"[result saved to file]"}],"truncated":[{"regexp":"\\.\\.\\. \\d+ more lines"}]},"program":"bin/sidera-fake-adapter"},"ledger_dir":"/tmp/l","runtime_dir":"/tmp/r","runtime_binary":"/tmp/sidera-fake-runtime","runtime_seed":9,"runtime_test_seed":"base","runtime_first_turn":{"manual_start":true},"clock_start_ms":1792832400000,"control":"127.0.0.1:5555"}`
	if string(raw) != want {
		t.Fatalf("encoded\n %s\nwant\n %s", raw, want)
	}
	got, err := fakeadapter.DecodeProcessConfig([]byte(want))
	if err != nil {
		t.Fatalf("DecodeProcessConfig: %v", err)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Fatalf("decoded %#v", got)
	}
	for name, bad := range map[string]string{
		"an unknown member":                 strings.Replace(want, `"instance_id"`, `"extra":1,"instance_id"`, 1),
		"no instance id":                    strings.Replace(want, `"instance_id":"fake"`, `"instance_id":""`, 1),
		"a runtime declared and no program": strings.Replace(want, `"runtime_binary":"/tmp/sidera-fake-runtime"`, `"runtime_binary":""`, 1),
		"a negative clock":                  strings.Replace(want, `1792832400000`, `-5`, 1),
		"a clock that starts at zero, which is a field left out read as 1970": strings.Replace(want, `1792832400000`, `0`, 1),
		"no control address (a process nobody can end)":                       strings.Replace(want, `"control":"127.0.0.1:5555"`, `"control":""`, 1),
		"a first turn with an unknown action":                                 strings.Replace(want, `"runtime_first_turn":{"manual_start":true}`, `"runtime_first_turn":{"actions":[{"kind":"dance"}]}`, 1),
	} {
		if _, err := fakeadapter.DecodeProcessConfig([]byte(bad)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func FuzzDecodeProcessConfig(f *testing.F) {
	f.Add([]byte(`{"instance_id":"x","manifest":{"id":"x","version":"1.0.0","runtime":true,"frontends":["cli"],"program":"p"},"ledger_dir":"l","runtime_dir":"r","runtime_binary":"b","runtime_seed":0,"runtime_first_turn":{},"clock_start_ms":1,"control":"127.0.0.1:1"}`))
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = fakeadapter.DecodeProcessConfig(data) })
}
