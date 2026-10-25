package daemon_test

import (
	"context"
	"errors"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	nodeconfig "github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

const chat = "chat"

var setupTag = fsx.Tag{Tx: "setup", Role: "f"}

// makeDirs makes dir and the parents of it that are missing.
func makeDirs(t testing.TB, fsys fsx.FS, dir string) {
	t.Helper()
	if dir == "." || dir == "" {
		return
	}
	if ok, err := fsx.Exists(fsys, dir); err != nil {
		t.Fatal(err)
	} else if ok {
		return
	}
	makeDirs(t, fsys, path.Dir(dir))
	if err := fsx.MakeDir(fsys, setupTag, dir); err != nil {
		t.Fatalf("making %s: %v", dir, err)
	}
}

// installChat puts the adapter package of the manifest on the disk, as an
// installation leaves it, and enables an instance of it named chat, while no
// daemon runs over the disk.
func installChat(t testing.TB, fsys fsx.FS, m fakeadapter.ManifestOptions) {
	t.Helper()
	raw, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	cur, err := nodeconfig.EncodeAdapterCurrent(m.Version)
	if err != nil {
		t.Fatal(err)
	}
	dir := nodeconfig.AdapterVersionDir(m.ID, m.Version)
	makeDirs(t, fsys, path.Join(dir, path.Dir(m.Program)))
	for p, c := range map[string][]byte{
		path.Join(dir, "sidera-adapter.json"): raw,
		path.Join(dir, m.Program):             []byte("#!fake\n"),
		nodeconfig.AdapterCurrentPath(m.ID):   cur,
	} {
		if err := fsx.Replace(fsys, setupTag, p, c, fsx.ModePrivate); err != nil {
			t.Fatalf("writing %s: %v", p, err)
		}
	}
	ins, err := nodeconfig.Instances{Version: 1, Items: map[string]nodeconfig.Instance{
		chat: {Adapter: m.ID, Enabled: true},
	}}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fsx.Replace(fsys, setupTag, nodeconfig.InstancesPath, ins, fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
}

// adapters is the adapter host of a test node, over fake adapters that run in the
// test process, with the ledger they write and what a test needs to see them.
type adapters struct {
	host     *adapterhost.Host
	launcher *harness.AdapterLauncher
	ledgers  *ledger.Memory
}

// newAdapters makes the host for a node whose clock is clk. launched is the
// manifest the fake adapters declare; arm, which may be nil, is called with the
// knobs of each adapter before it starts.
func newAdapters(t testing.TB, clk *clocktest.Fake, launched fakeadapter.ManifestOptions, arm func(n int, k *fakeadapter.Knobs)) *adapters {
	t.Helper()
	a := &adapters{ledgers: ledger.NewMemory()}
	a.launcher = harness.NewAdapterLauncher(harness.LauncherConfig{
		T: t, Ctx: context.Background(), Now: clk.Now, Ledgers: a.ledgers, Manifest: launched,
		Arm: func(_ string, n int, k *fakeadapter.Knobs) {
			if arm != nil {
				arm(n, k)
			}
		},
	})
	host, err := adapterhost.New(adapterhost.Config{Launcher: a.launcher, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	a.host = host
	t.Cleanup(a.launcher.Close)
	t.Cleanup(host.Abort)
	return a
}

// installedChat is the declaration of the adapter that is installed.
func installedChat() fakeadapter.ManifestOptions {
	return harness.AdapterManifest(adapterproto.MaxDirectEndpoints)
}

// openHome opens the data directory of c on the disk, to write what an
// installation leaves.
func openHome(t testing.TB, c daemon.Config) fsx.FS {
	t.Helper()
	fsys, err := fsx.OpenOS(c.Home, seeded(9))
	if err != nil {
		t.Fatal(err)
	}
	return fsys
}

// launches waits for the n-th adapter process of the instance to have been started
// and returns them.
func (a *adapters) launches(t testing.TB, n int) []*harness.AdapterLaunch {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if l := a.launcher.Launches(chat); len(l) >= n {
			return l
		}
		if time.Now().After(deadline) {
			t.Fatalf("fewer than %d adapter processes were started", n)
		}
		time.Sleep(time.Millisecond)
	}
}

type startResult struct {
	d   *daemon.Daemon
	err error
}

// The adapters that the owner enabled are started at step 10, and the step does
// not end before each has answered adapter.start: the recovery of deliveries that
// follows releases a lease only when no turn is running, and cannot know that from
// an adapter that has not said so. The adapter is held just before its answer, so
// that the step has to wait, and what the step saw is read from the ledger the
// adapter wrote and from the log of the daemon.
func TestEnabledAdapterInstancesAreStartedAtStepTenAndTheStepWaitsForThem(t *testing.T) {
	noLeaks(t)
	c, clk := initialized(t)
	installChat(t, openHome(t, c), installedChat())
	a := newAdapters(t, clk, installedChat(), func(_ int, k *fakeadapter.Knobs) {
		if err := k.PauseAt("start.before-reply"); err != nil {
			t.Error(err)
		}
	})
	c.AdapterHost = a.host

	var mu sync.Mutex
	var seen struct {
		ran     bool
		steps   []string
		methods []string
		online  bool
	}
	c.Attach.Adapters = func(ctx context.Context, env *daemon.Env) error {
		v, err := env.Engine.Adapter(ctx, chat)
		mu.Lock()
		defer mu.Unlock()
		seen.ran, seen.steps, seen.methods, seen.online = true, steps(logLines(t, c.Home)), a.launcher.Launches(chat)[0].Methods(), err == nil && v.Online
		return nil
	}

	result := make(chan startResult, 1)
	go func() { d, err := daemon.Start(context.Background(), c); result <- startResult{d, err} }()

	first := a.launches(t, 1)[0]
	ctx := context.Background()
	if err := first.Controller().WaitPaused(ctx, "start.before-reply"); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-result:
		t.Fatalf("the start returned (%v) while the adapter had not answered adapter.start", r.err)
	case <-time.After(100 * time.Millisecond):
	}
	got := steps(logLines(t, c.Home))
	if indexOf(got, "startup 10 begin") < 0 || indexOf(got, "startup 10 end") >= 0 || indexOf(got, "startup 11 begin") >= 0 {
		t.Fatalf("while the adapter had not answered, the log said %v; want step 10 begun, not ended, and no recovery of deliveries", got)
	}
	mu.Lock()
	ran := seen.ran
	mu.Unlock()
	if ran {
		t.Fatal("the hook of step 10 ran before the adapters had answered")
	}

	if err := first.Controller().Release(ctx, "start.before-reply"); err != nil {
		t.Fatal(err)
	}
	r := <-result
	if r.err != nil {
		t.Fatalf("Start: %v", r.err)
	}
	t.Cleanup(func() {
		r.d.RequestStop("test over")
		if err := r.d.Wait(); err != nil {
			t.Errorf("the daemon stopped with an error: %v", err)
		}
	})

	mu.Lock()
	defer mu.Unlock()
	if !seen.ran || len(seen.steps) == 0 || seen.steps[len(seen.steps)-1] != "startup 10 begin" {
		t.Errorf("the hook ran at %v, want inside step 10", seen.steps)
	}
	if !slices.Contains(seen.methods, adapterproto.MethodAdapterStart) || !seen.online {
		t.Errorf("when the hook ran the daemon had written %v to the adapter and the engine had it online: %v; want adapter.start written and the instance online", seen.methods, seen.online)
	}
	all := steps(logLines(t, c.Home))
	if i, j := indexOf(all, "startup 10 end"), indexOf(all, "startup 11 begin"); i < 0 || j < i {
		t.Errorf("the recovery of deliveries began at %d, step 10 ended at %d in %v; want it after", j, i, all)
	}
	if m := first.Methods(); len(m) < 2 || m[0] != adapterproto.MethodInitialize || m[1] != adapterproto.MethodAdapterStart {
		t.Errorf("the daemon wrote %v to the adapter, want initialize and then adapter.start", m)
	}

	// The engine reaches the host the daemon was given: asking whether the
	// connection is quiet is refused when the engine has no host.
	stats, err := first.Controller().Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := r.d.Engine().Quiesce(qctx, engine.Expect{Adapters: map[string]engine.AdapterExpect{
		chat: {Notifications: stats.NotificationsSent, Held: stats.Held},
	}}); err != nil {
		t.Errorf("the engine is not quiet about the adapter it was given: %v", err)
	}
}

// An adapter that cannot be taken on (here it reports another version than the one
// installed) does not stop the start: the instance is judged offline, the start goes
// on to the recovery of deliveries and serving, and the owner sees the instance
// offline.
func TestAnInstanceThatCannotBeStartedIsJudgedOfflineAndTheStartGoesOn(t *testing.T) {
	noLeaks(t)
	c, clk := initialized(t)
	installChat(t, openHome(t, c), installedChat())
	other := installedChat()
	other.Version = "9.9.9"
	a := newAdapters(t, clk, other, nil)
	c.AdapterHost = a.host
	var hooked bool
	c.Attach.Adapters = func(context.Context, *daemon.Env) error { hooked = true; return nil }

	d := started(t, c)
	if !hooked {
		t.Error("the hook of step 10 did not run after the host had judged the instance")
	}
	got := steps(logLines(t, c.Home))
	for _, s := range []string{"startup 10 end", "startup 11 begin", "startup 15 end"} {
		if indexOf(got, s) < 0 {
			t.Errorf("the log has no %q: %v", s, got)
		}
	}
	v, err := d.Engine().Adapter(context.Background(), chat)
	if err != nil {
		t.Fatal(err)
	}
	if v.Online || v.Admitted {
		t.Errorf("the engine has the instance %+v, want it neither admitted nor online", v)
	}
	l := a.launches(t, 1)[0]
	if m := l.Methods(); len(m) == 0 || m[0] != adapterproto.MethodInitialize || slices.Contains(m, adapterproto.MethodAdapterStart) {
		t.Errorf("the daemon wrote %v to the adapter that was refused, want initialize and no adapter.start", m)
	}
}

// The adapters are stopped at step 3 of the shutdown, after the interfaces and
// before the engine ends: the adapter is asked to shut down, and the hook that
// stops what the host does not run comes after the host.
func TestShutdownStopsTheAdaptersAtStepThree(t *testing.T) {
	noLeaks(t)
	c, clk := initialized(t)
	installChat(t, openHome(t, c), installedChat())
	a := newAdapters(t, clk, installedChat(), nil)
	c.AdapterHost = a.host

	var d *daemon.Daemon
	var hookSteps, methodsAtHook []string
	var engineDoneAtHook, endedAtHook bool
	c.Attach.StopAdapters = func(context.Context) error {
		hookSteps = steps(logLines(t, c.Home))
		l := a.launcher.Launches(chat)[0]
		methodsAtHook, endedAtHook = l.Methods(), l.Ended()
		select {
		case <-d.Engine().Done():
			engineDoneAtHook = true
		default:
		}
		return nil
	}
	var err error
	d, err = daemon.Start(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	first := a.launches(t, 1)[0]
	d.RequestStop("test")
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}

	if len(hookSteps) == 0 || hookSteps[len(hookSteps)-1] != "shutdown 3 begin" {
		t.Errorf("the hook ran at %v, want inside step 3", hookSteps)
	}
	if !slices.Contains(methodsAtHook, adapterproto.MethodShutdown) || !endedAtHook || engineDoneAtHook {
		t.Errorf("when the hook ran the adapter had been told %v and ended: %v, and the engine had ended: %v; want it shut down and gone, the engine still running", methodsAtHook, endedAtHook, engineDoneAtHook)
	}
	if m := first.Methods(); m[len(m)-1] != adapterproto.MethodShutdown {
		t.Errorf("the daemon wrote %v to the adapter, want shutdown last", m)
	}
	if !first.Ended() {
		t.Error("the adapter process was left running")
	}
	got := steps(logLines(t, c.Home))
	i := indexOf(got, "shutdown 3 begin")
	if i < 0 || i+1 >= len(got) || got[i+1] != "shutdown 3 end" {
		t.Errorf("the log of the shutdown is %v, want step 3 to begin and end", got)
	}
}

// A fatal failure of the disk ends the daemon, and the adapters are still stopped,
// within the short grace: an adapter that was left running would go on writing
// beside the daemon that starts next.
func TestAFatalFailureStillStopsTheAdapters(t *testing.T) {
	noLeaks(t)
	n := newMemNode(t)
	installChat(t, n.mem, installedChat())
	a := newAdapters(t, n.clk, installedChat(), nil)
	faulty := fsxtest.NewFaulty(t, n.mem)
	rule := faulty.Fail(fsxtest.Match{Tx: "daemon-stop", Step: fsx.StepSyncDir}, errors.New("input/output error"))
	c := memConfig(n.home, n.clk, faulty)
	c.AdapterHost = a.host
	var hookRan bool
	c.Attach.StopAdapters = func(context.Context) error { hookRan = true; return nil }

	d, err := daemon.Start(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	first := a.launches(t, 1)[0]
	status, body := manage(t, n.home, "daemon.stop", map[string]any{"operation_id": strings.Repeat("0", 30) + "01"})
	if status != 500 || !strings.Contains(string(body), "storage_fatal") {
		t.Errorf("the request that met the failure answered %d %s", status, body)
	}
	waitDone(t, d)
	var fatal *daemon.FatalError
	if err := d.Wait(); !errors.As(err, &fatal) {
		t.Fatalf("Wait = %v; want a *daemon.FatalError", err)
	}
	if rule.Hits() == 0 {
		t.Fatal("the injected fault was never hit")
	}
	if !slices.Contains(first.Methods(), adapterproto.MethodShutdown) || !first.Ended() {
		t.Errorf("after the fatal failure the adapter was told %v and ended: %v; want it shut down and gone", first.Methods(), first.Ended())
	}
	if !hookRan {
		t.Error("the hook that stops the adapters did not run after the fatal failure")
	}
}

// A daemon that was given no adapter host starts no adapter, whatever the owner
// enabled, and starts and stops all the same: the hooks are the only thing at step
// 10 then.
func TestANodeWithoutAnAdapterHostStartsNoAdapter(t *testing.T) {
	noLeaks(t)
	c, clk := initialized(t)
	installChat(t, openHome(t, c), installedChat())
	a := newAdapters(t, clk, installedChat(), nil) // made, and not given to the daemon
	var hooked bool
	c.Attach.Adapters = func(context.Context, *daemon.Env) error { hooked = true; return nil }

	d := started(t, c)
	if !hooked {
		t.Error("the hook of step 10 did not run")
	}
	if n := len(a.launcher.Launches(chat)); n != 0 {
		t.Errorf("%d adapter processes were started by a node with no adapter host", n)
	}
	v, err := d.Engine().Adapter(context.Background(), chat)
	if err != nil {
		t.Fatal(err)
	}
	if v.Online {
		t.Errorf("the instance is online: %+v", v)
	}
	if _, err := d.Engine().Quiesce(context.Background(), engine.Expect{Adapters: map[string]engine.AdapterExpect{chat: {}}}); err == nil {
		t.Error("the engine answered a question about an adapter connection although it has no host")
	}
	if got := steps(logLines(t, c.Home)); indexOf(got, "startup 15 end") < 0 {
		t.Errorf("the start did not go through: %v", got)
	}
}
