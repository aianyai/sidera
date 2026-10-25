package daemon_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	nodeconfig "github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
)

// probe is a set of hooks that notes where in the start each ran, by the last step
// the log of the daemon said it had begun, and what it could see.
type probe struct {
	mu      sync.Mutex
	home    string
	at      map[string]string // hook -> the step the log was at
	phase   map[string]engine.Phase
	stopped []string
	failAt  string
	t       testing.TB
}

func newProbe(t testing.TB, home string) *probe {
	return &probe{t: t, home: home, at: map[string]string{}, phase: map[string]engine.Phase{}}
}

func (p *probe) note(name string, env *daemon.Env) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := steps(logLines(p.t, p.home))
	if len(st) > 0 {
		p.at[name] = st[len(st)-1]
	}
	p.phase[name] = env.Engine.Phase()
}

func (p *probe) hook(name string) func(context.Context, *daemon.Env) error {
	return func(_ context.Context, env *daemon.Env) error {
		p.note(name, env)
		if p.failAt == name {
			return errors.New("the " + name + " hook failed")
		}
		return nil
	}
}

func (p *probe) stop(name string) func(context.Context) error {
	return func(context.Context) error {
		p.mu.Lock()
		defer p.mu.Unlock()
		st := steps(logLines(p.t, p.home))
		p.stopped = append(p.stopped, name+" at "+st[len(st)-1])
		return nil
	}
}

func (p *probe) attachments() daemon.Attachments {
	return daemon.Attachments{
		TrustSync:      p.hook("trust"),
		AgentProbeOnly: p.hook("agent-probe"),
		Adapters:       p.hook("adapters"),
		AgentServe:     p.hook("agent-serve"),
		StopInterfaces: p.stop("interfaces"),
		StopAdapters:   p.stop("adapters"),
	}
}

// The start goes through the steps in a fixed order, and every step
// says when it begins and when it ends, so the order can be read from the log of
// the daemon. The wall clock is judged (step 4) before anything is signed (step
// 5); the scan of state/ (6) precedes the management interface (8); the
// adapters (10) have started before the recovery of deliveries (11 to 14) goes
// on, so that a lease is released only after the adapters said which turns run;
// and the Agent interface serves everything last (15).
func TestStartupStepsRunInTheirFixedOrder(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	c.Attach = newProbe(t, c.Home).attachments()
	started(t, c)
	var want []string
	for i := 1; i <= 15; i++ {
		want = append(want, "startup "+itoa(i)+" begin", "startup "+itoa(i)+" end")
	}
	if got := steps(logLines(t, c.Home)); !reflect.DeepEqual(got, want) {
		t.Errorf("the steps of the start are\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return "1" + string(rune('0'+i-10))
}

// The hooks run inside the step that they belong to, in the state of the engine
// that the step needs: before the loop exists at step 7, with the loop recovering
// at steps 9 and 10, running at step 15.
func TestEachHookRunsInsideItsStepWithTheEngineInTheStateTheStepNeeds(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	p := newProbe(t, c.Home)
	c.Attach = p.attachments()
	started(t, c)
	for _, tc := range []struct {
		hook  string
		at    string
		phase engine.Phase
	}{
		{"trust", "startup 7 begin", engine.PhaseOpened},
		{"agent-probe", "startup 9 begin", engine.PhaseRecovering},
		{"adapters", "startup 10 begin", engine.PhaseRecovering},
		{"agent-serve", "startup 15 begin", engine.PhaseRunning},
	} {
		if p.at[tc.hook] != tc.at || p.phase[tc.hook] != tc.phase {
			t.Errorf("hook %s ran at %q with the engine %s; want %q with the engine %s", tc.hook, p.at[tc.hook], p.phase[tc.hook], tc.at, tc.phase)
		}
	}
}

// While the node starts, the management interface answers reads and refuses what
// changes state as unavailable, recovering: the owner can see that it is starting,
// and cannot change anything that the start has not finished building on.
func TestTheManagementInterfaceReadsAndRefusesChangesWhileTheNodeStarts(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	var listStatus, stopStatus int
	var stopBody []byte
	hook := func(context.Context, *daemon.Env) error {
		listStatus, _ = manage(t, c.Home, "endpoints.list", map[string]any{})
		stopStatus, stopBody = manage(t, c.Home, "daemon.stop", map[string]any{"operation_id": strings.Repeat("0", 31) + "1"})
		return nil
	}
	c.Attach.AgentProbeOnly = hook
	started(t, c)
	if listStatus != 200 {
		t.Errorf("a read while the node started answered %d", listStatus)
	}
	if stopStatus != 503 || !strings.Contains(string(stopBody), `"category":"unavailable"`) || !strings.Contains(string(stopBody), `"code":"recovering"`) {
		t.Errorf("a change while the node started answered %d %s; want 503 unavailable/recovering", stopStatus, stopBody)
	}
}

// A hook that fails stops the start, and what the start had begun is stopped in the
// order of the shutdown: nothing is left running, the management interface does not
// answer, and the lock is free for the next start. The stops are those of what had
// begun: a hook of step 7 fails before the loop runs and stops nothing, the Agent
// interface of step 9 is stopped by the failure of any later step, and the adapters
// of step 10 by the failure of that step and of the next.
func TestAHookThatFailsStopsTheStartAndLeavesNothingRunning(t *testing.T) {
	for _, tc := range []struct {
		failAt string
		step   string
		stops  []string // the stops that run, in order
	}{
		{"trust", "step 7", nil},
		{"agent-probe", "step 9", []string{"interfaces"}},
		{"adapters", "step 10", []string{"interfaces", "adapters"}},
		{"agent-serve", "step 15", []string{"interfaces", "adapters"}},
	} {
		t.Run(tc.failAt, func(t *testing.T) {
			noLeaks(t)
			c, _ := initialized(t)
			p := newProbe(t, c.Home)
			p.failAt = tc.failAt
			c.Attach = p.attachments()
			_, err := daemon.Start(context.Background(), c)
			if err == nil || !strings.Contains(err.Error(), "the "+tc.failAt+" hook failed") || !strings.Contains(err.Error(), tc.step) {
				t.Fatalf("Start = %v; want the failure of the hook, naming the step", err)
			}
			var got []string
			for _, s := range p.stopped {
				got = append(got, strings.SplitN(s, " ", 2)[0])
			}
			if !reflect.DeepEqual(got, tc.stops) {
				t.Errorf("stops %v; want %v", got, tc.stops)
			}
			if _, err := os.Stat(filepath.Join(c.Home, "manage", "manage.sock")); err == nil && runtime.GOOS != "windows" {
				t.Error("the management socket is left")
			}
			if tryManage(c.Home, "endpoints.list") == 200 {
				t.Error("the management interface answers after the start failed")
			}
			// the lock is free and the node starts
			c.Attach = daemon.Attachments{}
			started(t, c)
		})
	}
}

// A directory of the layout that the owner removed is made again by the start, and
// the node runs.
func TestTheStartMakesTheLayoutTheOwnerRemovedAgain(t *testing.T) {
	requireUnix(t)
	noLeaks(t)
	c, _ := initialized(t)
	for _, d := range []string{"state", "run", "logs"} {
		if err := os.RemoveAll(filepath.Join(c.Home, d)); err != nil {
			t.Fatal(err)
		}
	}
	started(t, c)
	for _, d := range []string{"state", "state/out", "state/admin", "run", "logs"} {
		info, err := os.Stat(filepath.Join(c.Home, d))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Errorf("%s after the start: %v, %v; want a directory of mode 0700", d, info, err)
		}
	}
	if status, _ := manage(t, c.Home, "endpoints.list", map[string]any{}); status != 200 {
		t.Errorf("the node did not answer: %d", status)
	}
}

// The log of the daemon is a file under logs/ written through the restricted
// handle, with the time of the injected clock and mode 0600.
func TestTheDaemonLogsToTheFileUnderLogsWithTheTimeOfItsClock(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	started(t, c)
	p := filepath.Join(c.Home, "logs", "daemon.log")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "time=2026-10-24T09:00:00") {
		t.Errorf("the log does not carry the time of the clock of the daemon:\n%s", b)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("the log: %v, %v; want mode 0600", info, err)
		}
	}
}

// A hook logs through the logger it is given, which it may extend with attributes
// and groups: the attributes are on its lines in the file, and a debug line is not
// written by a log at info.
func TestTheLoggerOfAHookKeepsItsAttributesAndWritesNoDebugLines(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	c.Attach.AgentServe = func(_ context.Context, env *daemon.Env) error {
		l := env.Log.With("instance", "chat").WithGroup("turn")
		l.Info("a hook line", "n", 3)
		l.Debug("a debug line")
		return nil
	}
	started(t, c)
	var found bool
	for _, line := range logLines(t, c.Home) {
		if strings.Contains(line, "a debug line") {
			t.Errorf("a debug line was written: %s", line)
		}
		if strings.Contains(line, `msg="a hook line"`) {
			found = true
			if !strings.Contains(line, "instance=chat") || !strings.Contains(line, "turn.n=3") {
				t.Errorf("the attributes of the logger are not on its line: %s", line)
			}
		}
	}
	if !found {
		t.Error("the line of the hook is not in the log")
	}
}

// The logs share the total of the configuration among the daemon's and the enabled
// instances' (two files each): the daemon rotates its log at the share the
// configuration gives when it writes, the number of enabled instances read from the
// configuration it loaded. With a total of 12 MiB and two enabled instances that is
// 2 MiB a file; with none, 6 MiB.
func TestTheDaemonRotatesItsLogAtTheShareOfTheConfiguredTotal(t *testing.T) {
	const mib = 1 << 20
	for _, tc := range []struct {
		name      string
		instances []string
		rotates   bool
	}{
		{"two enabled instances share it", []string{"chat", "mail"}, true},
		{"the daemon alone has it", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noLeaks(t)
			c, _ := initialized(t)
			settings := nodeconfig.DefaultNodeSettings()
			settings.LogTotalBytes = 12 * mib
			node, err := nodeconfig.Node{Version: 1, Settings: settings}.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(c.Home, "config", "node.json"), node, 0o600); err != nil {
				t.Fatal(err)
			}
			items := map[string]nodeconfig.Instance{}
			for _, in := range tc.instances {
				items[in] = nodeconfig.Instance{Adapter: in, Enabled: true}
			}
			ins, err := nodeconfig.Instances{Version: 1, Items: items}.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(c.Home, "config", "instances.json"), ins, 0o600); err != nil {
				t.Fatal(err)
			}
			var log *slog.Logger
			c.Attach.AgentServe = func(_ context.Context, env *daemon.Env) error { log = env.Log; return nil }
			started(t, c)
			for i := 0; i < 4; i++ {
				log.Info("filling the log", "padding", strings.Repeat("x", mib/2))
			}
			_, err = os.Stat(filepath.Join(c.Home, "logs", "daemon.log.1"))
			if rotated := err == nil; rotated != tc.rotates {
				t.Errorf("after 2 MiB of lines the log was rotated: %v, want %v (stat: %v)", rotated, tc.rotates, err)
			}
		})
	}
}

// The periodic tasks are the engine's, run on the injected clock: the clock state
// is saved every minute and never goes back, and a task that another feature adds
// runs on its own period.
func TestThePeriodicTasksRunEveryPeriodOnTheInjectedClock(t *testing.T) {
	noLeaks(t)
	c, clk := initialized(t)
	var mu sync.Mutex
	var ran []time.Time
	c.Attach.Periodic = []daemon.PeriodicTask{{Name: "test-task", Every: 10 * time.Minute, Run: func(in engine.TaskInput) error {
		mu.Lock()
		defer mu.Unlock()
		ran = append(ran, in.Now)
		return nil
	}}}
	d := started(t, c)
	reader, err := fsx.OpenOS(c.Home, seeded(3))
	if err != nil {
		t.Fatal(err)
	}
	settle := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := d.Engine().Quiesce(ctx, engine.Expect{}); err != nil {
			t.Fatalf("Quiesce: %v", err)
		}
	}
	mark := func() time.Time {
		t.Helper()
		st, err := keys.ReadClock(reader)
		if err != nil {
			t.Fatal(err)
		}
		return st.HighWater
	}
	before := mark()
	clk.Advance(daemon.ClockStateEvery)
	settle()
	if got := mark(); !got.Equal(t0.Add(daemon.ClockStateEvery)) || !got.After(before) {
		t.Errorf("the high water mark after a minute is %v, want %v", got, t0.Add(daemon.ClockStateEvery))
	}
	clk.Advance(9 * time.Minute)
	settle()
	mu.Lock()
	n := len(ran)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("the added task ran %d times after ten minutes, want once", n)
	}
	if got := mark(); !got.Equal(t0.Add(10 * time.Minute)) {
		t.Errorf("the mark after ten minutes is %v", got)
	}
}

// A clock found behind the mark it saved is not trusted: the start notes that the
// wall clock was set back, saves the pause with the mark that was, and does not
// lower the mark. The judgment is made at step 4, before the step that signs.
func TestAClockSetBackIsPausedAtStartAndThePauseAndTheMarkAreKept(t *testing.T) {
	noLeaks(t)
	c, clk := initialized(t)
	d := started(t, c)
	clk.Advance(2 * time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := d.Engine().Quiesce(ctx, engine.Expect{}); err != nil { // the clock state is saved
		t.Fatal(err)
	}
	d.RequestStop("test")
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	reader, err := fsx.OpenOS(c.Home, seeded(3))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := keys.ReadClock(reader)
	if err != nil {
		t.Fatal(err)
	}
	clk.Set(t0.Add(-3 * time.Hour)) // the clock was set back by five hours from the mark
	started(t, c)
	got, err := keys.ReadClock(reader)
	if err != nil {
		t.Fatal(err)
	}
	if got.RollbackPausedSince.IsZero() {
		t.Error("the pause was not saved")
	}
	if got.HighWater.Before(saved.HighWater) {
		t.Errorf("the mark went back from %v to %v", saved.HighWater, got.HighWater)
	}
	var judged bool
	for _, l := range logLines(t, c.Home) {
		if strings.Contains(l, "wall clock was set back") {
			judged = true
		}
	}
	if !judged {
		t.Error("the log does not say that the wall clock was set back")
	}
	st := steps(logLines(t, c.Home))
	var four, five int = -1, -1
	for i, s := range st {
		if s == "startup 4 end" {
			four = i
		}
		if s == "startup 5 begin" {
			five = i
		}
	}
	if four < 0 || five < 0 || four > five {
		t.Errorf("the wall clock is judged at %d, signing begins at %d", four, five)
	}
}
