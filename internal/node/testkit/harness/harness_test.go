package harness_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent/engineagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/publication"
)

func bg() context.Context { return context.Background() }

// gate is an observer that holds the first write step of one transaction, from
// the moment it is armed, until it is released: it keeps the engine's loop in the
// middle of a command, which is the state a crash or an unquiet system is in.
type gate struct {
	tx      string
	step    fsx.Step // zero holds the first step of any kind
	armed   atomic.Bool
	reached chan struct{}
	release chan struct{}
	once    sync.Once
	relOnce sync.Once
}

func newGate(tx string, step fsx.Step) *gate {
	return &gate{tx: tx, step: step, reached: make(chan struct{}), release: make(chan struct{})}
}

func (g *gate) arm() { g.armed.Store(true) }

func (g *gate) Reached(p fsx.Point, _ txn.Kind) {
	if !g.armed.Load() || p.Tag.Tx != g.tx || (g.step != 0 && p.Step != g.step) {
		return
	}
	g.once.Do(func() {
		close(g.reached)
		<-g.release
	})
}

func (g *gate) open() { g.relOnce.Do(func() { close(g.release) }) }

// openAddresses returns the addresses of the endpoints the engine lists, sorted.
func openAddresses(t *testing.T, h *harness.H) []string {
	t.Helper()
	eps, err := h.Engine().Endpoints(bg())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ep := range eps {
		out = append(out, ep.Address.String())
	}
	sort.Strings(out)
	return out
}

func TestOneClockIsTheSourceOfTimeForTheEngineAndTheFakes(t *testing.T) {
	h := harness.New(t, harness.Options{})
	if got := h.Clock().Now(); !got.Equal(harness.DefaultStart) {
		t.Fatalf("the clock starts at %v, want %v", got, harness.DefaultStart)
	}
	h.MustDirect("a")
	h.Advance(150 * time.Minute)
	want := harness.DefaultStart.Add(150 * time.Minute)

	// The engine stamps what it records with the clock the fixture moves.
	if _, err := h.Engine().RaiseAlert(bg(), "probe_alert", "test", "detail"); err != nil {
		t.Fatal(err)
	}
	alerts, err := h.Engine().Alerts(bg())
	if err != nil || len(alerts) != 1 {
		t.Fatalf("alerts %v, %v", alerts, err)
	}
	if !alerts[0].At.Equal(want) {
		t.Errorf("the engine stamped an alert %v, want %v", alerts[0].At, want)
	}

	// The ledger and the agents write the time of the same clock.
	if err := h.BeginStep("after the advance"); err != nil {
		t.Fatal(err)
	}
	if err := h.Agent("a").Declare(bg(), fakeagent.DeclareInput{Paused: new(bool)}); err != nil {
		t.Fatal(err)
	}
	log, err := h.Ledger()
	if err != nil {
		t.Fatal(err)
	}
	entries := log.Select(func(e ledger.Entry) bool { return e.Step == "after the advance" })
	if len(entries) < 3 {
		t.Fatalf("%d entries of the step, want the step entry and the two of the call: %v", len(entries), entries)
	}
	for _, e := range entries {
		if e.AtMS != want.UnixMilli() {
			t.Errorf("entry %s of %s is at %d, want %d", e.Kind, e.Writer, e.AtMS, want.UnixMilli())
		}
	}

	// The method value a fake takes is the same clock; moving it back moves it for
	// everyone and fires no timer.
	now := h.Clock().Now
	back := harness.DefaultStart.Add(-time.Hour)
	h.Set(back)
	if !now().Equal(back) {
		t.Errorf("the method value reads %v after Set(%v)", now(), back)
	}
}

func TestADirectEndpointRegisteredThroughTheFixtureAppearsInTheEnginesList(t *testing.T) {
	h := harness.New(t, harness.Options{})
	addr, err := h.Direct("a")
	if err != nil {
		t.Fatal(err)
	}
	if addr != h.Addr("a") {
		t.Errorf("Direct returned %q but Addr says %q", addr, h.Addr("a"))
	}
	eps, err := h.Engine().Endpoints(bg())
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 || eps[0].Address.String() != addr {
		t.Fatalf("the engine lists %v, want exactly the endpoint %s", eps, addr)
	}
	if eps[0].Instance != harness.Instance {
		t.Errorf("instance %q, want %q", eps[0].Instance, harness.Instance)
	}
	again, err := h.Direct("a")
	if err != nil || again != addr {
		t.Errorf("the same key again gave %q, %v: the same key is the same endpoint", again, err)
	}
	other := h.MustDirect("b")
	if other == addr {
		t.Error("two keys gave one address")
	}
}

func TestTheNodeCapRefusesTheEndpointBeyondItThroughTheFixture(t *testing.T) {
	h := harness.New(t, harness.Options{})
	for i := 0; i < publication.MaxEndpointsPerNode; i++ {
		if _, err := h.Direct(key(i)); err != nil {
			t.Fatalf("endpoint %d: %v", i, err)
		}
	}
	_, err := h.Direct(key(publication.MaxEndpointsPerNode))
	ref := new(*engine.Refusal)
	if !errors.As(err, ref) || (*ref).Reason != engine.ReasonEndpointLimit || (*ref).Limit != engine.LimitNode {
		t.Fatalf("the endpoint beyond the cap got %v, want a node-limit refusal", err)
	}
}

func key(i int) string {
	return "key-" + string(rune('a'+i/26/26%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i%26))
}

func TestQuiesceReturnsOnlyWhenTheEnginesConditionsHoldAndOtherwiseFailsWithAReport(t *testing.T) {
	g := newGate("direct-register", 0)
	h := harness.New(t, harness.Options{Observer: g})
	g.arm()
	done := make(chan error, 1)
	go func() { _, err := h.Direct("a"); done <- err }()
	<-g.reached // the loop is inside the command, in the middle of its write

	err := h.Quiesce(scenario.WithTimeout(150 * time.Millisecond))
	if err == nil {
		t.Fatal("Quiesce returned while the loop was inside a command")
	}
	var qe *harness.QuiesceError
	if !errors.As(err, &qe) {
		t.Fatalf("error %T %v is not a *QuiesceError", err, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v does not wrap the deadline", err)
	}
	if !strings.Contains(err.Error(), "not quiet") {
		t.Errorf("error %q does not say the system was not quiet", err)
	}
	if qe.Answered {
		t.Error("the report says the loop answered the probe, but it was inside a command")
	}

	g.open()
	if err := <-done; err != nil {
		t.Fatalf("the registration failed: %v", err)
	}
	if err := h.Quiesce(); err != nil {
		t.Fatalf("Quiesce after the loop was released: %v", err)
	}
}

func TestQuiesceFollowsAnAdvanceThatMakesWorkDue(t *testing.T) {
	h := harness.New(t, harness.Options{Params: engine.Params{ChunkReplaces: 1}})
	h.MustDirect("a")
	h.Advance(48 * time.Hour)
	if err := h.Quiesce(); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	// Quiet is a state of the engine: the probe saw nothing due at the time of the
	// clock now, and a second one sees the same.
	rep, err := h.Engine().Quiesce(bg(), engine.Expect{})
	if err != nil || !rep.Satisfied {
		t.Errorf("the engine is not quiet after Quiesce: %+v, %v", rep, err)
	}
}

func TestAProcessCrashThenRestartKeepsExactlyWhatWasDurable(t *testing.T) {
	h := harness.New(t, harness.Options{})
	a, b := h.MustDirect("a"), h.MustDirect("b")
	if err := h.Agent("a").Declare(bg(), fakeagent.DeclareInput{Paused: ptrBool(true)}); err != nil {
		t.Fatal(err)
	}

	// A registration whose write fails before its commit was acknowledged to no
	// one and must not be there after a crash.
	rule := h.Faults().Fail(fsxtest.Match{Tx: "direct-register", Step: fsx.StepWrite}, fsxtest.DiskFull)
	_, err := h.Direct("c")
	if r, ok := engine.ReasonOf(err); !ok || r != engine.ReasonStorageFull {
		t.Fatalf("the registration on a full disk got %v, want storage_full", err)
	}
	if rule.Hits() == 0 {
		t.Fatal("the fault was not injected")
	}

	before := h.Clock().Now()
	h.Crash(harness.ProcessCrash)

	if got, want := openAddresses(t, h), sorted(a, b); !reflect.DeepEqual(got, want) {
		t.Fatalf("after the crash the engine lists %v, want exactly %v", got, want)
	}
	eps, _ := h.Engine().Endpoints(bg())
	for _, ep := range eps {
		if ep.Address.String() == a && !ep.Declaration.Paused {
			t.Error("the acknowledged declaration of a was lost")
		}
	}
	if !h.Clock().Now().Equal(before) {
		t.Error("a crash moved the clock: the test clock belongs to the fixture, not to the process")
	}
	if again, err := h.Direct("a"); err != nil || again != a {
		t.Errorf("the key of a gave %q, %v after the crash, want the same endpoint", again, err)
	}
	if _, err := h.Direct("c"); err != nil {
		t.Errorf("the registration that failed is possible after the restart: %v", err)
	}
}

func TestACleanRestartKeepsTheEndpointsAndTheClock(t *testing.T) {
	h := harness.New(t, harness.Options{})
	a := h.MustDirect("a")
	h.Advance(time.Hour)
	h.Restart()
	if got := openAddresses(t, h); !reflect.DeepEqual(got, []string{a}) {
		t.Errorf("after a restart the engine lists %v, want [%s]", got, a)
	}
	if want := harness.DefaultStart.Add(time.Hour); !h.Clock().Now().Equal(want) {
		t.Errorf("the clock is %v after a restart, want %v", h.Clock().Now(), want)
	}
	if err := h.Quiesce(); err != nil {
		t.Fatal(err)
	}
}

// snapshot is every file of the disk with its content, as a power loss left it.
func snapshot(t *testing.T, h *harness.H) map[string]string { return snapshotOf(t, h.Disk()) }

// snapshotOf is snapshot of any disk, such as one an abandoned process had.
func snapshotOf(t *testing.T, disk *fsxtest.Mem) map[string]string {
	t.Helper()
	got := map[string]string{}
	var walk func(d string)
	walk = func(d string) {
		entries, err := disk.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			p := d + "/" + e.Name
			if d == "." {
				p = e.Name
			}
			if e.IsDir {
				walk(p)
				continue
			}
			b, err := disk.ReadFile(p)
			if err != nil {
				t.Fatalf("ReadFile(%q): %v", p, err)
			}
			got[p] = string(b)
		}
	}
	walk(".")
	return got
}

// A power loss in the middle of a registration leaves a disk that depends on the
// seed alone: the same seed gives the same disk, and the seeds give more than
// one. Whatever the seed, what was acknowledged before it is there.
func TestAPowerLossRestartIsReproducibleUnderAFixedSeedAndKeepsWhatWasAcknowledged(t *testing.T) {
	disks := map[uint64]map[string]string{}
	distinct := map[string]bool{}
	for _, seed := range []uint64{1, 2, 3, 4, 5, 6, 7, 8} {
		var first map[string]string
		for run := 0; run < 2; run++ {
			// Held after the first rename and before the sync of its directory: the
			// new name is in the directory but not yet certain to be on the disk.
			g := newGate("direct-register", fsx.StepSyncDir)
			h := harness.New(t, harness.Options{Observer: g})
			a := h.MustDirect("a")
			g.arm()
			go func() { _, _ = h.Direct("b") }()
			<-g.reached
			// The loop is inside the registration of b: its files are partly
			// written and partly synced. Take the disk a power loss leaves.
			h.Crash(harness.PowerLoss(seed))
			g.open()

			snap := snapshot(t, h)
			if got := openAddresses(t, h); !contains(got, a) {
				t.Fatalf("seed %d run %d: the acknowledged endpoint a is gone: %v", seed, run, got)
			}
			if run == 0 {
				first = snap
			} else if !reflect.DeepEqual(first, snap) {
				t.Errorf("seed %d: two runs left different disks: the power loss is not a function of the seed", seed)
			}
		}
		disks[seed] = first
		distinct[renderDisk(first)] = true
	}
	// Coverage: the model chose differently for different seeds, so the equality
	// above is a property of the seed and not of a disk that had nothing to lose.
	if len(distinct) < 2 {
		t.Errorf("all %d seeds left the same disk: the power loss did not take anything the seed decides", len(disks))
	}
	t.Logf("%d seeds left %d distinct disks", len(disks), len(distinct))
}

func renderDisk(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + m[k] + "\n")
	}
	return b.String()
}

func TestFaultsAreInjectedThroughTheFileLayerAndTheirHitsCounted(t *testing.T) {
	h := harness.New(t, harness.Options{})
	rule := h.Faults().Fail(fsxtest.Match{Tx: "direct-register", Step: fsx.StepWrite}, fsxtest.DiskFull)
	_, err := h.Direct("a")
	if r, ok := engine.ReasonOf(err); !ok || r != engine.ReasonStorageFull {
		t.Fatalf("a full disk gave %v, want storage_full", err)
	}
	if rule.Hits() == 0 {
		t.Error("the rule was never hit")
	}
	if got := openAddresses(t, h); len(got) != 0 {
		t.Errorf("the engine lists %v after a refused registration", got)
	}
}

func TestEachFixtureStartsFromItsOwnCopyOfTheInitSnapshot(t *testing.T) {
	first := harness.New(t, harness.Options{})
	first.MustDirect("a")
	second := harness.New(t, harness.Options{})
	if got := openAddresses(t, second); len(got) != 0 {
		t.Errorf("a second fixture lists %v: it shares a disk with the first", got)
	}
	if !reflect.DeepEqual(snapshot(t, second), snapshotOfFreshNode(t)) {
		t.Error("the second fixture's disk is not the initialised node")
	}
}

func snapshotOfFreshNode(t *testing.T) map[string]string {
	t.Helper()
	return snapshot(t, harness.New(t, harness.Options{}))
}

func TestTheLedgerCarriesTheScriptStepTheFixtureAnnounced(t *testing.T) {
	h := harness.New(t, harness.Options{})
	h.MustDirect("a")
	if err := h.BeginStep("first"); err != nil {
		t.Fatal(err)
	}
	if err := h.Agent("a").Declare(bg(), fakeagent.DeclareInput{Paused: ptrBool(true)}); err != nil {
		t.Fatal(err)
	}
	if err := h.BeginStep("second"); err != nil {
		t.Fatal(err)
	}
	_ = h.Agent("a").Declare(bg(), fakeagent.DeclareInput{Paused: ptrBool(false)})
	log, err := h.Ledger()
	if err != nil {
		t.Fatal(err)
	}
	steps := log.Kind(ledger.KindFixtureStep)
	if len(steps) != 2 || steps[0].Attrs.Name != "first" || steps[1].Attrs.Name != "second" {
		t.Fatalf("fixture.step entries %+v, want first then second", steps)
	}
	for _, step := range []string{"first", "second"} {
		calls := log.Select(func(e ledger.Entry) bool { return e.Kind == ledger.KindClientCall && e.Step == step })
		if len(calls) != 1 {
			t.Errorf("%d client.call entries of step %q, want 1", len(calls), step)
		}
	}
	if got := h.BeginStep("first"); got == nil {
		t.Error("a step name announced twice was accepted: an entry names its step and nothing else")
	}
}

func TestWhatTheEngineLayerCannotProvideYetIsAnErrorAndNotAFake(t *testing.T) {
	h := harness.New(t, harness.Options{})
	if _, err := h.Adapter("chat"); !errors.Is(err, scenario.ErrNotAvailable) {
		t.Errorf("Adapter returned %v, want ErrNotAvailable", err)
	}
	if _, err := h.Runtime(); !errors.Is(err, scenario.ErrNotAvailable) {
		t.Errorf("Runtime returned %v, want ErrNotAvailable", err)
	}
}

func TestRecordsAreReadOnlyAndShowTheDisk(t *testing.T) {
	h := harness.New(t, harness.Options{})
	h.MustDirect("a")
	sc, err := h.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Endpoints) != 1 {
		t.Errorf("the scan shows %d endpoint records, want 1", len(sc.Endpoints))
	}
}

// A script runs through scenario.Env on the engine layer: setup, steps with a
// quiesce after each, the ledger judged at the end.
func TestAScenarioScriptRunsThroughEnvOnTheEngineLayer(t *testing.T) {
	h := harness.New(t, harness.Options{})
	var seen []string
	script := scenario.Script{
		Name:  "declare-and-wait",
		Setup: scenario.Setup{Direct: []string{"a", "b"}},
		Steps: []scenario.Step{
			{Name: "a pauses", Do: func(e scenario.Env) error {
				seen = append(seen, "a pauses at "+e.Addr("a"))
				return e.Agent("a").Declare(e.Ctx(), fakeagent.DeclareInput{Paused: ptrBool(true)})
			}},
			scenario.Advance("an hour passes", time.Hour),
			{Name: "the adapter is not there yet", Do: func(e scenario.Env) error {
				if _, err := e.Adapter("chat"); !errors.Is(err, scenario.ErrNotAvailable) {
					return errors.New("Adapter did not say it is not available")
				}
				return nil
			}},
		},
	}
	if err := scenario.Run(h, script); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Errorf("steps ran %v", seen)
	}
	if want := harness.DefaultStart.Add(time.Hour); !h.Clock().Now().Equal(want) {
		t.Errorf("clock %v, want %v", h.Clock().Now(), want)
	}
	log, err := h.Ledger()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range log.Kind(ledger.KindFixtureStep) {
		names = append(names, e.Attrs.Name)
	}
	if want := []string{"a pauses", "an hour passes", "the adapter is not there yet"}; !reflect.DeepEqual(names, want) {
		t.Errorf("announced steps %v, want %v", names, want)
	}
	if err := h.CheckLedger(); err != nil {
		t.Errorf("CheckLedger: %v", err)
	}
	// The same fixture is a crash-walk scenario's environment too: the script
	// describes itself to the walk.
	if got := script.Scenario().Steps(); len(got) != 3 {
		t.Errorf("walk steps %v", got)
	}
}

func ptrBool(b bool) *bool { return &b }

func sorted(s ...string) []string { sort.Strings(s); return s }

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// A process crash leaves the page cache: a rename that completed and whose
// directory was not yet synced is on the disk the next process reads, which is
// what separates it from a power loss.
func TestAProcessCrashKeepsAWriteThatWasNotSyncedYet(t *testing.T) {
	g := newGate("direct-register", fsx.StepSyncDir)
	h := harness.New(t, harness.Options{Observer: g})
	before := snapshot(t, h)
	g.arm()
	go func() { _, _ = h.Direct("a") }()
	<-g.reached
	h.Crash(harness.ProcessCrash)
	g.open()
	after := snapshot(t, h)
	added := 0
	for p := range after {
		if _, ok := before[p]; !ok && strings.Contains(p, "endpoint-ids") {
			added++
		}
	}
	if added == 0 {
		t.Errorf("the disk after a process crash has no id file of the registration that was in its rename: %v", keysOf(after))
	}
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// A process that crashed is gone: a call it was still working on is answered by
// no one, so the caller learns nothing of its outcome, and whatever the old
// process would have done after the crash reaches neither the disk the next
// process reads nor the one it left.
func TestACallInFlightAtACrashHasAnUnknownOutcomeAndLeavesNoEffectOfTheAbandonedProcess(t *testing.T) {
	g := newGate("endpoint-update", 0)
	h := harness.New(t, harness.Options{Observer: g})
	h.MustDirect("a")
	abandoned := h.Disk()
	g.arm()
	done := make(chan error, 1)
	go func() { done <- h.Agent("a").Declare(bg(), fakeagent.DeclareInput{Paused: ptrBool(true)}) }()
	<-g.reached // the loop is inside the declaration, before its first write
	left := snapshotOf(t, abandoned)

	h.Crash(harness.ProcessCrash)
	g.open() // the old process goes on with the command it was in
	err := <-done

	if got := fakeagent.CategoryOf(err); got != fakeagent.CategoryOutcomeUnknown {
		t.Errorf("the call that was in flight at the crash returned %v (category %q), want outcome_unknown", err, got)
	}
	eps, listErr := h.Engine().Endpoints(bg())
	if listErr != nil || len(eps) != 1 {
		t.Fatalf("the new process lists %v, %v", eps, listErr)
	}
	if eps[0].Declaration.Paused {
		t.Error("the declaration the crash interrupted is in the new process")
	}
	if got := snapshotOf(t, abandoned); !reflect.DeepEqual(got, left) {
		t.Error("the abandoned process wrote to the disk it left after the crash")
	}
	log, ledgerErr := h.Ledger()
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	results := log.Kind(ledger.KindClientResult)
	if len(results) != 1 || results[0].Attrs.Category != string(fakeagent.CategoryOutcomeUnknown) {
		t.Errorf("the ledger records the result %+v, want one outcome_unknown", results)
	}
}

// While the node is down a client finds no one listening, and the disk is the
// test's: what it changes there is what the next process reads.
func TestAStoppedNodeIsDownAndAFileChangedOfflineIsWhatTheNextStartReads(t *testing.T) {
	h := harness.New(t, harness.Options{})
	h.MustDirect("a")
	h.Stop()

	err := h.Agent("a").Declare(bg(), fakeagent.DeclareInput{Paused: ptrBool(true)})
	if fakeagent.CategoryOf(err) != fakeagent.CategoryUnavailable || fakeagent.CodeOf(err) != "daemon_not_running" {
		t.Errorf("a call to a node that is down returned %v, want unavailable/daemon_not_running", err)
	}
	if err := h.Quiesce(); !errors.Is(err, engineagent.ErrNodeNotRunning) {
		t.Errorf("Quiesce of a node that is down returned %v, want ErrNodeNotRunning", err)
	}

	removed := 0
	for p := range snapshot(t, h) {
		if strings.HasPrefix(p, "state/endpoints/") {
			if err := h.Disk().Remove(fsx.Tag{Tx: "offline", Role: "f"}, p); err != nil {
				t.Fatalf("removing %s offline: %v", p, err)
			}
			removed++
		}
	}
	if removed != 1 {
		t.Fatalf("removed %d endpoint records, want the one of a", removed)
	}
	h.Start()
	if got := openAddresses(t, h); len(got) != 0 {
		t.Errorf("the new process lists %v, but the record of a was removed while the node was down", got)
	}
}

// A rule armed for the next start belongs to that process and is counted like
// any other.
func TestAFaultArmedForTheNextStartAppliesToThatProcess(t *testing.T) {
	h := harness.New(t, harness.Options{})
	h.Stop()
	var rule *fsxtest.Rule
	h.StartWith(func(f *fsxtest.Faulty) {
		rule = f.Fail(fsxtest.Match{Tx: "direct-register", Step: fsx.StepWrite}, fsxtest.DiskFull)
	})
	_, err := h.Direct("a")
	if r, ok := engine.ReasonOf(err); !ok || r != engine.ReasonStorageFull {
		t.Fatalf("the registration got %v, want storage_full", err)
	}
	if rule.Hits() == 0 {
		t.Error("the rule armed before the start was not hit")
	}
}

// A crash is a kill and a start; between them the node is down on the disk the
// crash left.
func TestAKilledNodeIsDownOnTheDiskTheCrashLeftUntilItIsStarted(t *testing.T) {
	h := harness.New(t, harness.Options{})
	a := h.MustDirect("a")
	h.Kill(harness.ProcessCrash)

	if err := h.Agent("a").Declare(bg(), fakeagent.DeclareInput{Paused: ptrBool(true)}); fakeagent.CategoryOf(err) != fakeagent.CategoryUnavailable {
		t.Errorf("a call to a killed node returned %v, want unavailable", err)
	}
	records := 0
	for p := range snapshot(t, h) {
		if strings.HasPrefix(p, "state/endpoints/") {
			records++
		}
	}
	if records != 1 {
		t.Errorf("the disk the crash left has %d endpoint records, want the one of a", records)
	}
	h.Start()
	if got := openAddresses(t, h); !reflect.DeepEqual(got, []string{a}) {
		t.Errorf("the new process lists %v, want [%s]", got, a)
	}
}

// A crash is an event of the run, and the ledger says what kind: a judgement or a
// reader of a failed run must not have to guess from the disk.
func TestACrashIsWrittenToTheLedgerWithItsModeAndSeed(t *testing.T) {
	h := harness.New(t, harness.Options{})
	h.MustDirect("a")
	if err := h.BeginStep("the crashes"); err != nil {
		t.Fatal(err)
	}
	h.Crash(harness.ProcessCrash)
	h.Crash(harness.PowerLoss(7))
	log, err := h.Ledger()
	if err != nil {
		t.Fatal(err)
	}
	crashes := log.Kind(ledger.KindFixtureCrash)
	if len(crashes) != 2 {
		t.Fatalf("%d fixture.crash entries, want 2: %+v", len(crashes), crashes)
	}
	if got := crashes[0].Attrs.CrashPoint; !strings.Contains(got, "process crash") {
		t.Errorf("the first crash is recorded as %q, want it to say process crash", got)
	}
	if got := crashes[1].Attrs.CrashPoint; !strings.Contains(got, "power loss") || !strings.Contains(got, "seed 7") {
		t.Errorf("the second crash is recorded as %q, want it to say power loss and seed 7", got)
	}
	if crashes[0].Step != "the crashes" || crashes[0].Writer != "fixture" {
		t.Errorf("the entry is %s of step %q, want the fixture's, in the step the crash happened in", crashes[0].Writer, crashes[0].Step)
	}
}
