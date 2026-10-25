package daemon_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

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
func TestStartupStepsRunInTheOrderOfTheDesign(t *testing.T) {
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
// answer, and the lock is free for the next start.
func TestAHookThatFailsStopsTheStartAndLeavesNothingRunning(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	p := newProbe(t, c.Home)
	p.failAt = "adapters"
	c.Attach = p.attachments()
	_, err := daemon.Start(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "the adapters hook failed") || !strings.Contains(err.Error(), "step 10") {
		t.Fatalf("Start = %v; want the failure of the hook, naming the step", err)
	}
	// the stops of what had begun, interfaces before adapters
	if len(p.stopped) != 2 || !strings.HasPrefix(p.stopped[0], "interfaces") || !strings.HasPrefix(p.stopped[1], "adapters") {
		t.Errorf("stops %v; want the interfaces and then the adapters", p.stopped)
	}
	if _, err := os.Stat(filepath.Join(c.Home, "manage", "manage.sock")); err == nil && runtime.GOOS != "windows" {
		t.Error("the management socket is left")
	}
	// the lock is free and the node starts
	c.Attach = daemon.Attachments{}
	started(t, c)
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
