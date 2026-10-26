package fakeadapter_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// TestHeldIsAMultisetOfMethodsWhateverHoldsTheRequest: a request is held by an
// outcome or at a pause point, and either way it is listed under its method, never
// under the name of a point. A fixture compares this multiset with the methods of
// the requests the daemon has in flight to the adapter; a list that mixed methods
// and points could never be equal to it. A request leaves the list as it is let go,
// not when its handler next runs: a fixture that asks for quiet right after a
// release would otherwise discount a request that is about to be answered.
func TestHeldIsAMultisetOfMethodsWhateverHoldsTheRequest(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.ready()
	if err := r.knobs.SetCreate(fakeadapter.Outcome{Kind: fakeadapter.OutcomeHold}); err != nil {
		t.Fatal(err)
	}
	for i, ep := range []string{"sess-2", "sess-3"} {
		n := i + 2
		go func() { _, _ = r.call(adapterproto.MethodSessionCreate, r.createParams(n, ep)) }()
	}
	if err := r.knobs.PauseAt(fakeadapter.PausePromptAfterForward); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = r.prompt("sess-1", 5, "work") }()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	if err := r.knobs.WaitPaused(ctx, fakeadapter.PausePromptAfterForward); err != nil {
		t.Fatal(err)
	}
	r.waitFor("two held creations", func() bool { return len(r.knobs.Held()) == 3 })
	held := r.knobs.Held()
	sort.Strings(held)
	if want := []string{adapterproto.MethodPromptDeliver, adapterproto.MethodSessionCreate, adapterproto.MethodSessionCreate}; !reflect.DeepEqual(held, want) {
		t.Fatalf("Held() = %v, want the multiset of methods %v", held, want)
	}
	if got := r.knobs.HeldPoints(); !reflect.DeepEqual(got, []string{fakeadapter.PausePromptAfterForward}) {
		t.Fatalf("HeldPoints() = %v, want only the point a request stopped at", got)
	}
	r.knobs.ReleaseHeld()
	if held := r.knobs.Held(); !reflect.DeepEqual(held, []string{adapterproto.MethodPromptDeliver}) {
		t.Fatalf("Held() = %v once the creations were let go, want only the paused prompt", held)
	}
	r.knobs.Release(fakeadapter.PausePromptAfterForward)
	if held := r.knobs.Held(); len(held) != 0 {
		t.Fatalf("Held() = %v once every request was let go, want nothing", held)
	}
}

// knobScript is one script against the Controller of a fake adapter, written once
// for both layers: call sends a request of the daemon side to the adapter, and
// notifications waits until the daemon side has n notifications of a method.
func knobScript(t *testing.T, ctl fakeadapter.Controller, call func(method string, params any, out adapterproto.Message) error, create func(n int, ep string) adapterproto.SessionCreateParams, notifications func(method string, n int)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*waitLimit)
	defer cancel()
	var res adapterproto.SessionCreateResult

	// A one-shot outcome, and a misuse that is refused.
	if err := ctl.QueueOutcome(ctx, fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeFail, Reason: "preset_unknown"}); err != nil {
		t.Fatal(err)
	}
	if err := call(adapterproto.MethodSessionCreate, create(1, "sess-1"), &res); err != nil || res.Reason != adapterproto.CreateFailPresetUnknown {
		t.Fatalf("create with the outcome: %+v, %v", res, err)
	}
	if err := ctl.QueueOutcome(ctx, fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeAccepted}); err == nil {
		t.Fatal("an outcome a creation does not take was accepted")
	}

	// A hold: the request waits, Stats says which method, and a release lets it go.
	if err := ctl.SetOutcome(ctx, fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeHold}); err != nil {
		t.Fatal(err)
	}
	held := make(chan error, 1)
	go func() {
		var r adapterproto.SessionCreateResult
		held <- call(adapterproto.MethodSessionCreate, create(2, "sess-2"), &r)
	}()
	waitUntil(t, "a held create", func() bool {
		st, err := ctl.Stats(ctx)
		return err == nil && reflect.DeepEqual(st.Held, []string{adapterproto.MethodSessionCreate}) && len(st.HeldPoints) == 0
	})
	if err := ctl.ReleaseHeld(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-held; err != nil {
		t.Fatalf("the held create after its release: %v", err)
	}
	if err := ctl.ClearOutcomes(ctx); err != nil {
		t.Fatal(err)
	}

	// A pause point: Stats names the method and the point.
	if err := ctl.PauseAt(ctx, fakeadapter.PauseCreateAfterNative); err != nil {
		t.Fatal(err)
	}
	paused := make(chan error, 1)
	go func() {
		var r adapterproto.SessionCreateResult
		paused <- call(adapterproto.MethodSessionCreate, create(3, "sess-3"), &r)
	}()
	if err := ctl.WaitPaused(ctx, fakeadapter.PauseCreateAfterNative); err != nil {
		t.Fatal(err)
	}
	st, err := ctl.Stats(ctx)
	if err != nil || !reflect.DeepEqual(st.Held, []string{adapterproto.MethodSessionCreate}) || !reflect.DeepEqual(st.HeldPoints, []string{fakeadapter.PauseCreateAfterNative}) {
		t.Fatalf("Stats while paused: %+v, %v", st, err)
	}
	if err := ctl.Release(ctx, fakeadapter.PauseCreateAfterNative); err != nil {
		t.Fatal(err)
	}
	if err := <-paused; err != nil {
		t.Fatalf("the paused create after its release: %v", err)
	}

	// A notification the script makes the adapter send is counted when sent.
	before, _ := ctl.Stats(ctx)
	if err := ctl.Availability(ctx, []adapterproto.Availability{{Condition: adapterproto.ConditionRuntimeAuth, SinceMS: testStart.UnixMilli()}}); err != nil {
		t.Fatal(err)
	}
	notifications(adapterproto.MethodAdapterAvailability, 1)
	if after, _ := ctl.Stats(ctx); after.NotificationsSent != before.NotificationsSent+1 {
		t.Fatalf("notifications sent: %d then %d, want one more", before.NotificationsSent, after.NotificationsSent)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestOneScriptDrivesTheKnobsOfBothLayers: the adapter in the test process and the
// adapter program are driven through one interface, so a scenario written once
// turns the same knobs in both layers.
func TestOneScriptDrivesTheKnobsOfBothLayers(t *testing.T) {
	t.Run("in the test process", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		r.initialize()
		knobScript(t, r.knobs.Controller(),
			func(m string, p any, out adapterproto.Message) error {
				raw, err := r.call(m, p)
				if err == nil && out != nil {
					err = adapterproto.Decode(raw, out)
				}
				return err
			},
			r.createParams,
			func(m string, n int) { r.waitNotifications(m, n) })
	})
	t.Run("as a program", func(t *testing.T) {
		f := newProcFixture(t)
		a := f.startAdapter()
		a.initialize()
		knobScript(t, fakeadapter.RemoteKnobs{Conn: a.conn}, a.call, a.createParams,
			func(m string, n int) { a.waitNotifications(m, n) })
	})
}

// --- the first turn and the end of a killed one ---

// TestTheFirstPromptStartsATurnTheDaemonHearsOf: a session is not created idle. The
// first prompt of session.create starts a turn as it does in a real runtime, and the
// adapter reports it with the attempt of that prompt, so the daemon can count the
// prompt as delivered. The plan of the first turn is the runtime's to be given.
func TestTheFirstPromptStartsATurnTheDaemonHearsOf(t *testing.T) {
	r := newRig(t, rigOptions{autoFirstTurn: true})
	r.initialize()
	created := r.createSession(1, "sess-1")
	raw := r.waitNotifications(adapterproto.MethodTurnStarted, 1)[0]
	var ts adapterproto.TurnStartedParams
	if err := adapterproto.Decode(raw, &ts); err != nil {
		t.Fatal(err)
	}
	if ts.Endpoint != "sess-1" || ts.Attempt != attemptID(1) || ts.Process == nil || created.Process == nil || *ts.Process != *created.Process {
		t.Fatalf("turn.started %+v for session %+v: want the first prompt's attempt and the session's process", ts, created)
	}
	if err := r.runtime.EndTurn(string(created.NativeRef), fakeruntime.FinalCompleted); err != nil {
		t.Fatal(err)
	}
	r.waitNotifications(adapterproto.MethodTurnEnded, 1)
}

// TestAKilledNativeProcessEndsItsRunningTurnAsFailedExactlyOnce: every turn has one
// turn.started and one turn.ended; the native process cannot end its turn, so the
// adapter that sees it die does.
func TestAKilledNativeProcessEndsItsRunningTurnAsFailedExactlyOnce(t *testing.T) {
	r := newRig(t, rigOptions{autoFirstTurn: true})
	r.initialize()
	created := r.createSession(1, "sess-1")
	r.waitNotifications(adapterproto.MethodTurnStarted, 1)
	if err := r.runtime.KillNative(string(created.NativeRef)); err != nil {
		t.Fatal(err)
	}
	raws := r.waitNotifications(adapterproto.MethodTurnEnded, 1)
	var te adapterproto.TurnEndedParams
	if err := adapterproto.Decode(raws[0], &te); err != nil {
		t.Fatal(err)
	}
	if te.Final != adapterproto.TurnFailed || te.Endpoint != "sess-1" {
		t.Fatalf("turn.ended %+v, want the killed turn failed", te)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(r.notifications(adapterproto.MethodTurnEnded)); n != 1 {
		t.Fatalf("%d turn.ended notifications, want exactly one", n)
	}
	rec, err := r.reconcile("sess-1", 1)
	if err != nil || rec.Verdict != adapterproto.VerdictFound || rec.Turn == nil || rec.Turn.State != adapterproto.TurnStateFailed {
		t.Fatalf("reconcile of the first prompt after the kill: %+v, %v; want found, its turn failed", rec, err)
	}
}

// --- what the ledger shows ---

// TestAConfirmedCreationNamesTheRuntimesEntry: the adapter's entry for a native
// session names the runtime's own entry for it, which exists; the ledger can say so.
func TestAConfirmedCreationNamesTheRuntimesEntry(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.initialize()
	created := r.createSession(1, "sess-1")
	log := r.ledgerLog()
	returned := log.Kind(ledger.KindNativeCreateReturned)
	if len(returned) != 1 {
		t.Fatalf("%d native.create.returned entries", len(returned))
	}
	a := returned[0].Attrs
	if a.NativeRef != string(created.NativeRef) || a.RuntimeWriter != "runtime/"+string(created.NativeRef) || a.RuntimeSeq < 1 {
		t.Fatalf("the entry %+v does not name the runtime's entry for %s", a, created.NativeRef)
	}
	if err := log.CheckConfirmations(); err != nil {
		t.Fatal(err)
	}
}

// --- how the program is configured ---

func TestProcessConfigComesFromTheFileInTheArgumentsOrElseFromTheEnvironment(t *testing.T) {
	cfg := fakeadapter.ProcessConfig{
		InstanceID: "from-file", Manifest: fakeadapter.DefaultManifestOptions(), LedgerDir: "/l", RuntimeDir: "/r",
		RuntimeBinary: "/b", ClockStartMS: 1, Control: "127.0.0.1:1",
	}
	other := cfg
	other.InstanceID = "from-env"
	file := filepath.Join(t.TempDir(), "adapter.json")
	write := func(c fakeadapter.ProcessConfig) []byte {
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if err := os.WriteFile(file, write(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(k string) string {
		if k == fakeadapter.ConfigEnv {
			return string(write(other))
		}
		return ""
	}
	rows := []struct {
		rule    string
		args    []string
		getenv  func(string) string
		wantID  string
		wantErr bool
	}{
		{"the file named by the arguments wins over the environment", fakeadapter.ConfigArgs(file), env, "from-file", false},
		{"the environment is the fallback", nil, env, "from-env", false},
		{"neither is an error", nil, func(string) string { return "" }, "", true},
		{"a file that does not exist is an error", fakeadapter.ConfigArgs(filepath.Join(t.TempDir(), "none.json")), env, "", true},
		{"an argument the program does not know is an error", []string{"--verbose"}, env, "", true},
		{"the flag without a file is an error", []string{fakeadapter.ConfigFlag}, env, "", true},
		{"more arguments than the flag and its file are an error", append(fakeadapter.ConfigArgs(file), "extra"), env, "", true},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			got, err := fakeadapter.ReadProcessConfig(row.args, row.getenv)
			if (err != nil) != row.wantErr {
				t.Fatalf("err = %v, want error %v", err, row.wantErr)
			}
			if err == nil && got.InstanceID != row.wantID {
				t.Fatalf("instance %q, want %q", got.InstanceID, row.wantID)
			}
		})
	}
}

// TestTheAdapterProgramStartsFromTheConfigFileOfItsArguments: the entry arguments of
// an adapter package are what tell apart several fake adapters under one daemon, so
// the program must really take its configuration from them and not from the
// environment it shares with the others.
func TestTheAdapterProgramStartsFromTheConfigFileOfItsArguments(t *testing.T) {
	f := newProcFixture(t)
	adapterBin, _ := programs(t)
	cfg := f.cfg
	cfg.InstanceID = "second"
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "second.json")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(adapterBin, fakeadapter.ConfigArgs(file)...)
	cmd.Env = os.Environ() // no ConfigEnv: the arguments are all it has
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.StdoutPipe(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	conn, err := f.srv.Wait(ctx, "second")
	if err != nil {
		t.Fatalf("the program did not join the control channel as the instance in its file: %v", err)
	}
	if conn.Hello().Role != fakectl.RoleAdapter {
		t.Fatalf("hello %+v", conn.Hello())
	}
}

func TestTheExtensionOfAnExecutableDependsOnTheOperatingSystem(t *testing.T) {
	for goos, want := range map[string]string{"windows": ".exe", "linux": "", "darwin": ""} {
		if got := fakeadapter.ExeSuffixOn(goos); got != want {
			t.Errorf("ExeSuffixOn(%q) = %q, want %q", goos, got, want)
		}
	}
}

func TestTheDefaultProgramHasTheExtensionOfAnExecutableOfThePlatform(t *testing.T) {
	// A path that names a program is used as it is, and Windows starts only a file
	// that has an extension.
	got := fakeadapter.DefaultManifestOptions().Program
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(got, ".exe") {
			t.Fatalf("Program = %q on Windows, want a .exe", got)
		}
	} else if strings.Contains(filepath.Base(got), ".") {
		t.Fatalf("Program = %q, want no extension outside Windows", got)
	}
}

// TestTheDaemonSideIsToldBeforeTheCallThatCausedTheEventReturns: in the binary layer
// the runtime is another process. It makes its events requests that the adapter
// answers after it has handed them to the daemon, so when the script's call that
// started or ended a turn returns, the turn's notification is already on its way
// and counted. A fixture that waited for quiet and then advanced the clock would
// otherwise be ahead of a notification nobody had yet handled.
func TestTheDaemonSideIsToldBeforeTheCallThatCausedTheEventReturns(t *testing.T) {
	f := newProcFixture(t)
	a := f.startAdapter()
	a.initialize()
	var created adapterproto.SessionCreateResult
	if err := a.call(adapterproto.MethodSessionCreate, a.createParams(1, "sess-1"), &created); err != nil {
		t.Fatal(err)
	}
	ref := string(created.NativeRef)
	f.settle(ref)
	k := fakeadapter.RemoteKnobs{Conn: a.conn}
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	sent := func() int {
		st, err := k.Stats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return st.NotificationsSent
	}
	base := sent()

	var delivered adapterproto.PromptDeliverResult
	if err := a.call(adapterproto.MethodPromptDeliver, adapterproto.PromptDeliverParams{Endpoint: "sess-1", Attempt: attemptID(2), Text: "work"}, &delivered); err != nil {
		t.Fatal(err)
	}
	if got := sent(); got != base+1 {
		t.Fatalf("notifications sent when prompt.deliver returned: %d, want %d: the turn start was not handed over", got, base+1)
	}
	if err := f.ctrl.EndTurn(ref, fakeruntime.FinalCompleted); err != nil {
		t.Fatal(err)
	}
	if got := sent(); got != base+2 {
		t.Fatalf("notifications sent when EndTurn returned: %d, want %d: the turn end was not handed over", got, base+2)
	}
	if err := f.ctrl.StartTurn(ref); err != nil {
		t.Fatal(err)
	}
	if got := sent(); got != base+3 {
		t.Fatalf("notifications sent when StartTurn returned: %d, want %d", got, base+3)
	}
	if err := f.ctrl.KillNative(ref); err != nil {
		t.Fatal(err)
	}
	if got := sent(); got != base+4 {
		t.Fatalf("notifications sent when KillNative returned: %d, want %d: the end of the killed turn was not handed over", got, base+4)
	}
	raws := a.waitNotifications(adapterproto.MethodTurnEnded, 2)
	var te adapterproto.TurnEndedParams
	if err := adapterproto.Decode(raws[1], &te); err != nil || te.Final != adapterproto.TurnFailed {
		t.Fatalf("the killed turn ended as %+v (%v), want failed", te, err)
	}
}

// TestTheLedgerOfAProcessAdapterConfirmsItsCreations: across separate processes the
// runtime's entry for the session and the adapter's confirmation of it are on
// record, and the confirmation names the runtime's entry.
func TestTheLedgerOfAProcessAdapterConfirmsItsCreations(t *testing.T) {
	f := newProcFixture(t)
	a := f.startAdapter()
	a.initialize()
	var created adapterproto.SessionCreateResult
	if err := a.call(adapterproto.MethodSessionCreate, a.createParams(1, "sess-1"), &created); err != nil {
		t.Fatal(err)
	}
	log, err := ledger.ReadDir(f.cfg.LedgerDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.CheckConfirmations(); err != nil {
		t.Fatal(err)
	}
	if n := len(log.Kind(ledger.KindNativeCreateReturned)); n != 1 {
		t.Fatalf("%d native.create.returned entries, want 1", n)
	}
}
