package harness_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The timing of the host in these tests is the test clock's, and every number is
// written out so that each step of a script is one move of the clock that makes
// exactly one thing due.
const (
	pingEvery    = 30 * time.Second
	pingWithin   = 10 * time.Second
	endWithin    = 5 * time.Second
	restartAfter = 10 * time.Second
)

func hostParams() adapterhost.Params {
	return adapterhost.Params{
		PingInterval: pingEvery, PingTimeout: pingWithin, ShutdownWait: endWithin,
		RestartBackoffMin: restartAfter, RestartBackoffMax: restartAfter,
	}
}

func withAdapter(t *testing.T) *harness.H {
	t.Helper()
	return harness.New(t, harness.Options{Adapter: &harness.AdapterOptions{Host: hostParams()}})
}

// step moves the clock and waits for the system to stop reacting to it: a timer that
// the host has not armed yet is not moved by the next step, so a step is never taken
// before the host has finished with the last.
func step(t *testing.T, h *harness.H, d time.Duration) {
	t.Helper()
	h.Advance(d)
	if err := h.Quiesce(); err != nil {
		t.Fatalf("after moving the clock by %v: %v", d, err)
	}
}

func lockEntries(t *testing.T, h *harness.H, kind string) int {
	t.Helper()
	log, err := h.Ledger()
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	return len(log.Kind(kind))
}

func online(t *testing.T, h *harness.H) bool {
	t.Helper()
	v, err := h.Engine().Adapter(context.Background(), harness.Instance)
	if err != nil {
		t.Fatalf("Adapter(%q): %v", harness.Instance, err)
	}
	return v.Online
}

// A hung adapter that nobody can end holds its instance lock. The host judges it
// offline after two unanswered pings, starts the instance again as often as its
// backoff says, and the replacement is refused the lock every time: it is never
// taken on, never told adapter.start, and so never controls a session beside the
// old one. When the old process is finally gone the next replacement gets the lock
// and is taken on. What happened is read from the ledger of the adapters and from
// what the daemon wrote to each process.
func TestNoSecondWriterAfterAdapterRestart(t *testing.T) {
	h := withAdapter(t)
	ctx := context.Background()
	if err := h.Quiesce(); err != nil {
		t.Fatalf("the first adapter was not taken on: %v", err)
	}
	launches := h.AdapterLaunches(harness.Instance)
	if len(launches) != 1 {
		t.Fatalf("%d adapter processes after the start, want 1", len(launches))
	}
	first := launches[0]
	if !online(t, h) {
		t.Fatal("the adapter is not online after the start")
	}
	if got := lockEntries(t, h, ledger.KindAdapterLockAcquired); got != 1 {
		t.Fatalf("%d adapter.lock.acquired after the start, want 1", got)
	}

	// The old process hangs and cannot be ended: it keeps answering nothing and
	// keeps the lock.
	first.Survive()
	if err := first.Controller().Hang(ctx); err != nil {
		t.Fatalf("Hang: %v", err)
	}

	// One unanswered ping is a busy adapter, not an offline one.
	step(t, h, pingEvery)
	step(t, h, pingWithin)
	if !online(t, h) {
		t.Fatal("one unanswered ping made the adapter offline")
	}
	// The second one is.
	step(t, h, pingEvery)
	step(t, h, pingWithin)
	if online(t, h) {
		t.Fatal("two unanswered pings in a row left the adapter online")
	}

	// The host cannot end the process: it waits for it as long as it said, then
	// goes on, and starts the instance again when the backoff is over.
	step(t, h, endWithin)
	step(t, h, restartAfter)
	launches = h.AdapterLaunches(harness.Instance)
	if len(launches) != 2 {
		t.Fatalf("%d adapter processes after the first restart, want 2", len(launches))
	}
	second := launches[1]
	if got := lockEntries(t, h, ledger.KindAdapterLockRefused); got != 1 {
		t.Fatalf("%d adapter.lock.refused after the first restart, want 1", got)
	}
	// Again and again while the old process lives.
	step(t, h, restartAfter)
	step(t, h, restartAfter)
	if got := lockEntries(t, h, ledger.KindAdapterLockRefused); got != 3 {
		t.Fatalf("%d adapter.lock.refused after three restarts, want 3", got)
	}
	if got := lockEntries(t, h, ledger.KindAdapterLockAcquired); got != 1 {
		t.Fatalf("%d adapter.lock.acquired while the old process holds the lock, want 1", got)
	}
	if online(t, h) {
		t.Fatal("the instance is online with the lock held by a hung process")
	}
	for i, l := range h.AdapterLaunches(harness.Instance)[1:] {
		for _, m := range l.Methods() {
			if m == adapterproto.MethodAdapterStart || m == adapterproto.MethodAdapterProbe {
				t.Fatalf("replacement %d was sent %s while the old process held the lock", i+1, m)
			}
		}
	}
	if m := second.Methods(); len(m) == 0 || m[0] != adapterproto.MethodInitialize {
		t.Fatalf("the first thing the daemon wrote to the replacement was %v, want initialize", m)
	}

	// The old process ends; the next replacement takes the lock and is taken on.
	first.End()
	step(t, h, restartAfter)
	if !online(t, h) {
		t.Fatal("the instance is not online after the old process ended")
	}
	if got := lockEntries(t, h, ledger.KindAdapterLockAcquired); got != 2 {
		t.Fatalf("%d adapter.lock.acquired after the old process ended, want 2", got)
	}
	launches = h.AdapterLaunches(harness.Instance)
	last := launches[len(launches)-1].Methods()
	want := []string{adapterproto.MethodInitialize, adapterproto.MethodAdapterStart, adapterproto.MethodAdapterProbe}
	if len(last) < len(want) || !slices.Equal(last[:len(want)], want) {
		t.Fatalf("the daemon wrote %v to the process that got the lock, want it to begin with %v", last, want)
	}
	if err := h.CheckLedger(); err != nil {
		t.Fatalf("ledger: %v", err)
	}
}

// A crash of the daemon cuts the pipe of the adapter and sends no shutdown, as a
// process that died would; only a clean stop asks the adapter to shut down.
func TestACrashCutsTheAdapterWithoutAShutdownAndACleanRestartShutsItDown(t *testing.T) {
	h := withAdapter(t)
	if err := h.Quiesce(); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	h.Crash(harness.ProcessCrash)
	if err := h.Quiesce(); err != nil {
		t.Fatalf("Quiesce after the crash: %v", err)
	}
	launches := h.AdapterLaunches(harness.Instance)
	if len(launches) != 2 {
		t.Fatalf("%d adapter processes after the crash, want 2", len(launches))
	}
	if slices.Contains(launches[0].Methods(), adapterproto.MethodShutdown) {
		t.Fatalf("the crashed process wrote shutdown to the adapter: %v", launches[0].Methods())
	}
	if !launches[0].Ended() {
		t.Fatal("the pipe of the adapter was not cut by the crash")
	}
	h.Restart()
	if err := h.Quiesce(); err != nil {
		t.Fatalf("Quiesce after the restart: %v", err)
	}
	launches = h.AdapterLaunches(harness.Instance)
	if len(launches) != 3 {
		t.Fatalf("%d adapter processes after the restart, want 3", len(launches))
	}
	if !slices.Contains(launches[1].Methods(), adapterproto.MethodShutdown) {
		t.Fatalf("a clean stop did not ask the adapter to shut down: %v", launches[1].Methods())
	}
	if !online(t, h) {
		t.Fatal("the adapter is not online after the restart")
	}
}

// What the adapter says reaches the engine before the system is called quiet: the
// notification count the fake adapter reports is what the engine's quiet condition
// is measured against. A snapshot is sent and the check made at once, many times;
// a fixture that only asked whether the engine's own queue was empty would pass
// some of them by luck and fail others.
func TestQuiesceWaitsUntilTheEngineHasHandledEveryNotificationTheAdapterSent(t *testing.T) {
	h := withAdapter(t)
	if err := h.Quiesce(); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	ctl, err := h.Adapter(harness.Instance)
	if err != nil {
		t.Fatalf("Adapter: %v", err)
	}
	ctx := context.Background()
	since := harness.DefaultStart.Add(-time.Minute)
	for i := 0; i < 40; i++ {
		var snapshot []adapterproto.Availability
		var want handoff.Signals
		if i%2 == 0 {
			snapshot = []adapterproto.Availability{{Condition: adapterproto.ConditionRuntimeAuth, SinceMS: since.UnixMilli()}}
			want = handoff.Signals{AuthInvalid: true, Since: since}
		}
		if err := ctl.Availability(ctx, snapshot); err != nil {
			t.Fatalf("Availability: %v", err)
		}
		if err := h.Quiesce(); err != nil {
			t.Fatalf("Quiesce: %v", err)
		}
		got, err := h.Engine().AdapterSignals(ctx, harness.Instance, "any-endpoint")
		if err != nil {
			t.Fatalf("AdapterSignals: %v", err)
		}
		if got != want {
			t.Fatalf("snapshot %d: the engine's signals are %+v, want %+v", i, got, want)
		}
	}
}

// The instance of the fixture is taken on through the host, as the daemon takes it
// on, and the controller of the adapter is the one of the process that serves it.
func TestTheAdapterOfTheFixtureIsTakenOnThroughTheHost(t *testing.T) {
	h := withAdapter(t)
	if err := h.Quiesce(); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	v, err := h.Engine().Adapter(context.Background(), harness.Instance)
	if err != nil {
		t.Fatalf("Adapter: %v", err)
	}
	if !v.Admitted || !v.Online || v.Info.Adapter.ID != "chat" {
		t.Fatalf("the engine's view of the instance is %+v", v)
	}
	ctl, err := h.Adapter(harness.Instance)
	if err != nil {
		t.Fatalf("Adapter: %v", err)
	}
	stats, err := ctl.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if len(stats.Held) != 0 {
		t.Fatalf("the adapter holds %v at rest", stats.Held)
	}
	if n := len(h.AdapterLaunches(harness.Instance)); n != 1 {
		t.Fatalf("%d adapter processes, want 1", n)
	}
	if _, err := h.Direct("notes"); err != nil {
		t.Fatalf("a direct endpoint cannot be registered on an instance the host took on: %v", err)
	}
}
