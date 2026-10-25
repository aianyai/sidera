package fakeruntime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

// procEnv is a fixture for runtime processes: a control server, directories and a
// launcher on the test clock.
type procEnv struct {
	t        *testing.T
	clock    *clocktest.Fake
	srv      *fakectl.Server
	dir      string
	ledgers  string
	launcher *fakeruntime.ProcessLauncher
	ctrl     fakeruntime.RemoteController
}

func newProcEnv(t *testing.T) *procEnv {
	t.Helper()
	leakGuard(t)
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := fakectl.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e := &procEnv{t: t, clock: clocktest.NewFake(start), srv: srv, dir: t.TempDir(), ledgers: t.TempDir()}
	e.launcher = &fakeruntime.ProcessLauncher{
		Binary: runtimeBinary(t), Dir: e.dir, LedgerDir: e.ledgers, Control: srv.Addr(), Seed: 9, Now: e.clock.Now,
		// The exec actions of the tests run this test binary as a helper program.
		Env: []string{helperEnv + "=1"},
	}
	e.ctrl = fakeruntime.RemoteController{Server: srv}
	t.Cleanup(func() {
		// A runtime process ends when its control connection does; the ones that
		// are still there are killed by their process id.
		_ = e.launcher.Close()
		_ = srv.Close()
		cancel()
		killAll(t, e.dir)
	})
	return e
}

// killAll ends every runtime process that has an info file in dir.
func killAll(t *testing.T, dir string) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		info, err := fakeruntime.DecodeProcessInfo(raw)
		if err != nil {
			continue
		}
		if p, err := os.FindProcess(int(info.PID)); err == nil {
			_ = p.Kill()
		}
	}
}

func (e *procEnv) create(n int, endpoint string) (fakeruntime.Session, *watcher) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	s, err := e.launcher.Create(ctx, fakeruntime.CreateRequest{Attempt: attempt(n), Endpoint: endpoint, Preset: "default", FirstAttempt: attempt(n), FirstPrompt: "hello", Env: map[string]string{"SIDERA_TOKEN": "tok"}})
	if err != nil {
		e.t.Fatalf("Create: %v", err)
	}
	w := newWatcher()
	s.Watch(w)
	return s, w
}

func (e *procEnv) waitGone(ref string) {
	e.t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		s, ok := e.launcher.Find(ref)
		if ok && !s.Alive() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("the runtime process of %s did not end", ref)
}

func TestProcessSessionIsReachableAndDrivenOverTheControlChannel(t *testing.T) {
	e := newProcEnv(t)
	s, w := e.create(1, "sess-1")
	if want := nativeRef(9, attempt(1)); s.Ref() != want {
		t.Fatalf("Ref() = %s, want %s", s.Ref(), want)
	}
	if s.PID() == int64(os.Getpid()) || s.PID() < 1 || !s.Alive() {
		t.Fatalf("pid %d alive %v: a session of its own process was expected", s.PID(), s.Alive())
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	conn, err := e.srv.Wait(ctx, s.Ref())
	if err != nil {
		t.Fatal(err)
	}
	// The clock of the process is the fixture's.
	e.clock.Advance(90 * time.Minute)
	if err := conn.SetClock(ctx, e.clock.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := s.Deliver(ctx, attempt(2), "go"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "turn.started", func() bool { return len(w.started) == 1 })
	if got := w.started[0]; got.Attempt != attempt(2) || got.AtMS != e.clock.Now().UnixMilli() {
		t.Fatalf("turn start %+v: want attempt 2 at %d", got, e.clock.Now().UnixMilli())
	}
	if turn, active := s.TurnActive(); !active || turn != w.started[0].Turn {
		t.Fatalf("TurnActive() = %q, %v", turn, active)
	}
	if err := s.Deliver(ctx, attempt(3), "again"); !errors.Is(err, fakeruntime.ErrBusy) {
		t.Fatalf("a prompt during a turn: %v, want ErrBusy", err)
	}
	if err := e.ctrl.EndTurn(s.Ref(), fakeruntime.FinalCompleted); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "turn.ended", func() bool { return len(w.ended) == 1 })
	if err := e.ctrl.EndTurn(s.Ref(), fakeruntime.FinalCompleted); !errors.Is(err, fakeruntime.ErrNoTurn) {
		t.Fatalf("ending a turn that is over: %v, want ErrNoTurn", err)
	}
	rec, err := s.Reconcile(attempt(2))
	if err != nil || !rec.Found || rec.TurnState != "completed" || rec.WriterStopped {
		t.Fatalf("Reconcile = %+v, %v", rec, err)
	}
	if !e.ctrl.Idle() {
		t.Fatal("the runtime is not idle with no actions")
	}
	// A plan with an exec action runs the real command with the session's tool
	// access, and what it printed is what the model saw.
	if err := e.ctrl.SetPlan(s.Ref(), fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{{Kind: fakeruntime.ActionExec, Args: map[string]string{"argv": argvOfHelper(t, "marker")}}}, EndAfter: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Deliver(ctx, attempt(4), "run"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "second turn.ended", func() bool { return len(w.ended) == 2 })
}

func argvOfHelper(t *testing.T, arg string) string {
	t.Helper()
	b, _ := json.Marshal([]string{os.Args[0], arg})
	return string(b)
}

func TestARestartedAdapterTakesTheLiveSessionBack(t *testing.T) {
	e := newProcEnv(t)
	s, _ := e.create(1, "sess-1")
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	if err := s.Deliver(ctx, attempt(2), "go"); err != nil {
		t.Fatal(err)
	}
	// A new launcher, as a restarted adapter has, finds the process by reference.
	again := &fakeruntime.ProcessLauncher{Binary: e.launcher.Binary, Dir: e.dir, LedgerDir: e.ledgers, Control: e.launcher.Control, Seed: 9, Now: e.clock.Now}
	t.Cleanup(func() { _ = again.Close() })
	found, ok := again.Find(s.Ref())
	if !ok || !found.Alive() {
		t.Fatalf("Find after a restart: %v, alive %v", ok, ok && found.Alive())
	}
	if found.PID() != s.PID() || found.StartKey() != s.StartKey() || found.Endpoint() != "sess-1" {
		t.Fatalf("identity of the found session: pid %d key %s, want pid %d key %s", found.PID(), found.StartKey(), s.PID(), s.StartKey())
	}
	if _, active := found.TurnActive(); !active {
		t.Fatal("the found session does not know its turn is running")
	}
	// The same attempt, asked again, is the same session and no second process.
	same, err := again.Create(ctx, fakeruntime.CreateRequest{Attempt: attempt(1), Endpoint: "sess-1", Preset: "default", FirstAttempt: attempt(1), FirstPrompt: "hello"})
	if err != nil || same.PID() != s.PID() {
		t.Fatalf("Create of the same attempt: %v, pid %v", err, same)
	}
	n := 0
	log, err := ledger.ReadDir(e.ledgers)
	if err != nil {
		t.Fatal(err)
	}
	n = len(log.Kind(ledger.KindRuntimeSessionCreated))
	if n != 1 {
		t.Fatalf("%d native sessions for one attempt, want 1", n)
	}
}

func TestAKilledNativeProcessIsGoneAndItsRecordIsReadable(t *testing.T) {
	e := newProcEnv(t)
	s, _ := e.create(1, "sess-1")
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	if err := s.Deliver(ctx, attempt(2), "go"); err != nil {
		t.Fatal(err)
	}
	if err := e.ctrl.KillNative(s.Ref()); err != nil {
		t.Fatalf("KillNative: %v", err)
	}
	e.waitGone(s.Ref())
	dead, ok := e.launcher.Find(s.Ref())
	if !ok || dead.Alive() {
		t.Fatalf("Find after the kill: %v, alive %v", ok, ok && dead.Alive())
	}
	if err := dead.Deliver(ctx, attempt(3), "late"); !errors.Is(err, fakeruntime.ErrNotAlive) {
		t.Fatalf("Deliver to a dead session: %v", err)
	}
	found, err := dead.Reconcile(attempt(2))
	if err != nil || !found.Found || !found.WriterStopped {
		t.Fatalf("Reconcile of a delivered attempt: %+v, %v", found, err)
	}
	absent, err := dead.Reconcile(attempt(7))
	if err != nil || absent.Found || !absent.WriterStopped {
		t.Fatalf("Reconcile of an attempt that never arrived: %+v, %v", absent, err)
	}
	log, err := ledger.ReadDir(e.ledgers)
	if err != nil {
		t.Fatal(err)
	}
	if len(log.Kind(ledger.KindRuntimeKilled)) != 1 {
		t.Fatalf("%d runtime.killed entries", len(log.Kind(ledger.KindRuntimeKilled)))
	}
}

func TestARuntimeProcessEndsWhenItsControlConnectionEnds(t *testing.T) {
	e := newProcEnv(t)
	s, _ := e.create(1, "sess-1")
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	if _, err := e.srv.Wait(ctx, s.Ref()); err != nil {
		t.Fatal(err)
	}
	_ = e.srv.Close()
	e.waitGone(s.Ref())
}

func TestARuntimeProcessLedgerEntriesAreOnDiskAndValid(t *testing.T) {
	e := newProcEnv(t)
	s, _ := e.create(1, "sess-1")
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	_ = s.Deliver(ctx, attempt(2), "go")
	log, err := ledger.ReadDir(e.ledgers)
	if err != nil {
		t.Fatalf("the ledger of the runtime process is not readable: %v", err)
	}
	for _, en := range log.Entries() {
		if en.Writer != "runtime/"+s.Ref() || en.PID != s.PID() {
			t.Errorf("entry %+v: want writer runtime/%s and the process's pid %d", en, s.Ref(), s.PID())
		}
	}
	if n := len(log.Kind(ledger.KindRuntimePromptReceived)); n != 2 {
		t.Fatalf("%d runtime.prompt.received entries, want 2", n)
	}
}

func TestExecRunnerRunsTheCommandWithTheSessionEnvironment(t *testing.T) {
	t.Setenv(helperEnv, "1")
	r := fakeruntime.ExecRunner{}
	res, err := r.RunAction(context.Background(), fakeruntime.SessionInfo{Env: map[string]string{"SIDERA_TOKEN": "tok"}},
		fakeruntime.TurnAction{Kind: fakeruntime.ActionExec, Args: map[string]string{"argv": argvOfHelper(t, "arg")}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(res.Output)); got != "helper arg token=tok" {
		t.Fatalf("output %q", got)
	}
}

func TestExecRunnerRefusesWhatItCannotRun(t *testing.T) {
	r := fakeruntime.ExecRunner{}
	rows := map[string]fakeruntime.TurnAction{
		"another kind of action":       {Kind: fakeruntime.ActionRecv},
		"an exec without a vector":     {Kind: fakeruntime.ActionExec},
		"an exec with an empty vector": {Kind: fakeruntime.ActionExec, Args: map[string]string{"argv": "[]"}},
		"an exec with a bad vector":    {Kind: fakeruntime.ActionExec, Args: map[string]string{"argv": "ls"}},
	}
	for name, a := range rows {
		t.Run(name, func(t *testing.T) {
			if _, err := r.RunAction(context.Background(), fakeruntime.SessionInfo{}, a); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestExecRunnerKeepsTheOutputOfACommandThatFails(t *testing.T) {
	t.Setenv(helperEnv, "1")
	// The helper exits 0; ask for a command that does not exist to get the error.
	r := fakeruntime.ExecRunner{}
	b, _ := json.Marshal([]string{filepath.Join(t.TempDir(), "missing")})
	if _, err := r.RunAction(context.Background(), fakeruntime.SessionInfo{}, fakeruntime.TurnAction{Kind: fakeruntime.ActionExec, Args: map[string]string{"argv": string(b)}}); err == nil {
		t.Fatal("a missing command did not fail")
	}
}

// --- decoders of the process link ---

var goldenLinks = []struct {
	name  string
	msg   string
	json  string
	value any
}{
	{"deliver", fakeruntime.MethodDeliver, `{"attempt":"00000000-0000-4000-8000-000000000002","text":"go"}`, fakeruntime.DeliverParams{Attempt: "00000000-0000-4000-8000-000000000002", Text: "go"}},
	{"reconcile", fakeruntime.MethodReconcile, `{"attempt":"00000000-0000-4000-8000-000000000002"}`, fakeruntime.ReconcileParams{Attempt: "00000000-0000-4000-8000-000000000002"}},
	{"interrupt reply", fakeruntime.MethodInterrupt + ".reply", `{"result":"stopped"}`, fakeruntime.InterruptReply{Result: "stopped"}},
	{"state reply", fakeruntime.MethodState + ".reply", `{"alive":true,"active":true,"turn":"t1"}`, fakeruntime.StateReply{Alive: true, Active: true, Turn: "t1"}},
	{"state reply of a dead session", fakeruntime.MethodState + ".reply", `{"alive":false,"active":false}`, fakeruntime.StateReply{}},
	{"reconcile reply", fakeruntime.MethodReconcile + ".reply", `{"found":true,"turn":"t1","turn_state":"completed","turn_running":false,"writer_stopped":false}`, fakeruntime.ReconcileReply{Found: true, Turn: "t1", TurnState: "completed"}},
	{"plan knob", "knob." + fakeruntime.KnobPlan, `{"manual_start":true,"actions":[{"kind":"recv","args":{"frontend":"cli"}}],"end_after":true}`, fakeruntime.PlanKnob{ManualStart: true, Actions: []fakeruntime.TurnAction{{Kind: "recv", Args: map[string]string{"frontend": "cli"}}}, EndAfter: true}},
	{"turn end knob", "knob." + fakeruntime.KnobTurnEnd, `{"final":"interrupted"}`, fakeruntime.TurnEndKnob{Final: "interrupted"}},
	{"idle reply", "knob." + fakeruntime.KnobIdle + ".reply", `{"idle":true}`, fakeruntime.IdleReply{Idle: true}},
	{"turn started", fakeruntime.NoteTurnStarted, `{"turn":"t1","attempt":"00000000-0000-4000-8000-000000000002","at_ms":5}`, fakeruntime.TurnStartedNote{Turn: "t1", Attempt: "00000000-0000-4000-8000-000000000002", AtMS: 5}},
	{"turn ended", fakeruntime.NoteTurnEnded, `{"turn":"t1","final":"completed","at_ms":6}`, fakeruntime.TurnEndedNote{Turn: "t1", Final: "completed", AtMS: 6}},
	{"tool result", fakeruntime.NoteToolResult, `{"lease":"0123456789abcdef0123456789abcdef","text":"{}","frontend":"cli"}`, fakeruntime.ToolResultNote{Lease: "0123456789abcdef0123456789abcdef", Text: "{}", Frontend: "cli"}},
}

func TestGoldenLinkMessages(t *testing.T) {
	for _, g := range goldenLinks {
		t.Run(g.name, func(t *testing.T) {
			got, err := fakeruntime.DecodeLink(g.msg, []byte(g.json))
			if err != nil {
				t.Fatalf("DecodeLink: %v", err)
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

func TestLinkMessagesReject(t *testing.T) {
	rows := []struct{ rule, msg, json string }{
		{"an unknown member", fakeruntime.MethodDeliver, `{"attempt":"00000000-0000-4000-8000-000000000002","text":"x","extra":1}`},
		{"an attempt that is not a UUID v4", fakeruntime.MethodDeliver, `{"attempt":"abc","text":"x"}`},
		{"a result that is not stopped or not_running", fakeruntime.MethodInterrupt + ".reply", `{"result":"maybe"}`},
		{"active without a turn", fakeruntime.MethodState + ".reply", `{"alive":true,"active":true}`},
		{"a turn without being active", fakeruntime.MethodState + ".reply", `{"alive":true,"active":false,"turn":"t"}`},
		{"a turn state without a turn", fakeruntime.MethodReconcile + ".reply", `{"found":true,"turn_state":"running","turn_running":false,"writer_stopped":false}`},
		{"a turn of an attempt that was not found", fakeruntime.MethodReconcile + ".reply", `{"found":false,"turn":"t","turn_state":"running","turn_running":false,"writer_stopped":false}`},
		{"a plan with an unknown action", "knob." + fakeruntime.KnobPlan, `{"actions":[{"kind":"dance"}]}`},
		{"a final that is not one", "knob." + fakeruntime.KnobTurnEnd, `{"final":"vanished"}`},
		{"a turn start without a turn", fakeruntime.NoteTurnStarted, `{"turn":"","at_ms":1}`},
		{"a negative time", fakeruntime.NoteTurnEnded, `{"turn":"t","final":"completed","at_ms":-1}`},
		{"a tool result without a lease", fakeruntime.NoteToolResult, `{"lease":"","text":"x","frontend":"cli"}`},
		{"an unknown message", "runtime.nothing", `{}`},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if v, err := fakeruntime.DecodeLink(r.msg, []byte(r.json)); err == nil {
				t.Fatalf("accepted %s as %#v", r.json, v)
			}
		})
	}
}

func FuzzDecodeLink(f *testing.F) {
	for _, g := range goldenLinks {
		f.Add(g.msg, []byte(g.json))
	}
	f.Fuzz(func(t *testing.T, msg string, data []byte) {
		v, err := fakeruntime.DecodeLink(msg, data)
		if err != nil {
			return
		}
		enc, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("a decoded value does not encode: %v", err)
		}
		again, err := fakeruntime.DecodeLink(msg, enc)
		if err != nil || !reflect.DeepEqual(v, again) {
			t.Fatalf("decode, encode, decode: %v\n%#v\n%#v", err, v, again)
		}
	})
}

func TestGoldenProcessSpecAndInfo(t *testing.T) {
	const spec = `{"create":{"attempt":"00000000-0000-4000-8000-000000000001","endpoint":"sess-1","preset":"default","first_attempt":"00000000-0000-4000-8000-000000000001","first_prompt":"hello","env":{"SIDERA_TOKEN":"t"}},"dir":"/tmp/r","ledger_dir":"/tmp/l","seed":9,"clock_start_ms":1792832400000,"control":"127.0.0.1:5555"}`
	got, err := fakeruntime.DecodeProcessSpec([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := json.Marshal(got)
	if string(enc) != spec {
		t.Fatalf("encoded\n %s\nwant\n %s", enc, spec)
	}
	const info = `{"ref":"n-0123abcd","endpoint":"sess-1","addr":"127.0.0.1:5555","pid":4242,"start_key":"k-abc"}`
	pi, err := fakeruntime.DecodeProcessInfo([]byte(info))
	if err != nil {
		t.Fatal(err)
	}
	enc, _ = json.Marshal(pi)
	if string(enc) != info {
		t.Fatalf("encoded %s, want %s", enc, info)
	}
	for name, bad := range map[string]string{
		"a ref that is not a native reference": strings.Replace(info, "n-0123abcd", "x", 1),
		"a pid of zero":                        strings.Replace(info, `"pid":4242`, `"pid":0`, 1),
		"an unknown member":                    strings.Replace(info, `"pid"`, `"extra":1,"pid"`, 1),
	} {
		if _, err := fakeruntime.DecodeProcessInfo([]byte(bad)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	for name, bad := range map[string]string{
		"a spec without directories": strings.Replace(spec, `"dir":"/tmp/r"`, `"dir":""`, 1),
		"an unknown member":          strings.Replace(spec, `"seed"`, `"extra":1,"seed"`, 1),
		"a negative clock":           strings.Replace(spec, `1792832400000`, `-1`, 1),
	} {
		if _, err := fakeruntime.DecodeProcessSpec([]byte(bad)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func FuzzDecodeProcessSpecAndInfo(f *testing.F) {
	f.Add([]byte(`{"ref":"n-0123abcd","endpoint":"s","addr":"a","pid":1,"start_key":"k"}`))
	f.Add([]byte(`{"create":{"attempt":"a","endpoint":"e","preset":"p","first_attempt":"f","first_prompt":"t"},"dir":"d","ledger_dir":"l","seed":0,"clock_start_ms":0}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = fakeruntime.DecodeProcessInfo(data)
		_, _ = fakeruntime.DecodeProcessSpec(data)
	})
}
