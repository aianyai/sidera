package adapterhost_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// waitPings waits until the scripted adapter has received n pings.
func waitPings(t *testing.T, a *scripted, n int) {
	t.Helper()
	eventually(t, fmt.Sprintf("%d pings", n), func() bool { return a.pingCount() >= n })
}

// silentUntil answers a ping only when the test lets it go, ignoring the
// cancellation the host sends when it gives up: a late answer.
type gate chan struct{}

func (g gate) wait(ctx context.Context) {
	select {
	case <-g:
	case <-ctx.Done():
	}
}

// Two pings in a row that go unanswered make the adapter offline; one does not, and
// an answer in between starts the count again.
func TestTwoUnansweredPingsMakeTheAdapterOfflineAndOneDoesNot(t *testing.T) {
	p := testParams()
	t.Run("two in a row: offline once, killed, and a new pass after the backoff", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		var adapters []*scripted
		var procs []*pipeProcess
		r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
			pp, a := r.scriptedProcess(func(a *scripted) {
				a.ping = func(ctx context.Context, _ int) error { <-ctx.Done(); return ctx.Err() }
			}, false)
			adapters, procs = append(adapters, a), append(procs, pp)
			return pp, nil
		}
		r.start()
		r.ready("chat", 1)

		// The first ping is not answered: a busy adapter, not an absent one.
		r.advance(p.PingInterval)
		waitPings(t, adapters[0], 1)
		r.advance(p.PingTimeout)
		if got := r.goneCauses("chat"); len(got) != 0 {
			t.Fatalf("one unanswered ping was reported as %v", got)
		}
		if !r.host.Counts("chat").Connected {
			t.Fatal("one unanswered ping took the adapter out of service")
		}

		// The second one is.
		r.advance(p.PingInterval)
		waitPings(t, adapters[0], 2)
		r.advance(p.PingTimeout)
		if got := r.goneCauses("chat"); !slices.Equal(got, []adapterhost.GoneCause{adapterhost.GonePingsFailed}) {
			t.Fatalf("after two unanswered pings the engine was told %v, want [pings_failed] once", got)
		}
		if r.host.Counts("chat").Connected {
			t.Fatal("the adapter is still in service after two unanswered pings")
		}
		eventually(t, "the process to be killed", func() bool { return procs[0].killed() == 1 })

		// The second pass starts after the backoff, not before.
		if n := r.lau.launches(); n != 1 {
			t.Fatalf("%d launches before the backoff, want 1", n)
		}
		r.advance(p.RestartBackoffMin - time.Nanosecond)
		if n := r.lau.launches(); n != 1 {
			t.Fatalf("%d launches a nanosecond before the backoff ended, want 1", n)
		}
		r.advance(time.Nanosecond)
		r.waitLaunches(2)
		r.ready("chat", 2)
	})

	t.Run("one failure is forgiven and an answer starts the count again", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		letGo := make(gate)
		var a *scripted
		r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
			pp, ad := r.scriptedProcess(func(ad *scripted) {
				ad.ping = func(ctx context.Context, n int) error {
					switch n {
					case 1:
						// Too late: answered after the host gave up. The answer
						// is not an answer to a ping the host still waits for.
						letGo.wait(ctx)
						return nil
					case 3:
						<-ctx.Done()
						return ctx.Err()
					}
					return nil
				}
			}, false)
			a = ad
			return pp, nil
		}
		r.start()
		r.ready("chat", 1)

		// Ping 1 fails (too late); its late answer arrives and changes nothing.
		r.advance(p.PingInterval)
		waitPings(t, a, 1)
		r.advance(p.PingTimeout)
		close(letGo)
		a.barrier(r.eng)
		r.settle()

		// Ping 2 is answered: the count starts again. The clock moves on only once
		// the host has taken the answer and armed the next interval.
		r.advance(p.PingInterval)
		waitPings(t, a, 2)
		r.quiet("chat")

		// Ping 3 fails: one failure since the last answer, so not offline.
		r.advance(p.PingInterval)
		waitPings(t, a, 3)
		r.advance(p.PingTimeout)
		if got := r.goneCauses("chat"); len(got) != 0 {
			t.Fatalf("the engine was told %v after failures that were not in a row", got)
		}
		if !r.host.Counts("chat").Connected {
			t.Fatal("the adapter went offline after failures that were not in a row")
		}
		if n := r.lau.launches(); n != 1 {
			t.Fatalf("%d launches, want 1", n)
		}
	})
}

// The engine is told the adapter is gone only after the forwarding of its
// connection is over: a notification that was still buffered when the second ping
// failed is dropped, and none reaches the engine once it was told. The engine
// holds the first notification until it is told the adapter is gone, as an engine
// that is slow to take the report does, and would take every other one in the
// meantime if the forwarder still ran.
func TestNoNotificationReachesTheEngineAfterItWasToldThatTheAdapterIsGone(t *testing.T) {
	p := testParams()
	r := newRig(t, rigOptions{})
	r.eng.notifyGate = make(chan struct{})
	const sent = 5
	var a *scripted
	r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
		pp, ad := r.scriptedProcess(func(ad *scripted) {
			ad.ping = func(ctx context.Context, _ int) error { <-ctx.Done(); return ctx.Err() }
		}, false)
		a = ad
		return pp, nil
	}
	var release sync.Once
	r.eng.onEvent = func(kind string) {
		if kind != "gone" {
			return
		}
		release.Do(func() {
			close(r.eng.notifyGate)
			// Long enough for a forwarder that still runs to take what is buffered.
			deadline := time.Now().Add(100 * time.Millisecond)
			for len(r.eng.of("notified")) < sent && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
		})
	}
	r.start()
	r.ready("chat", 1)

	for range sent {
		a.notify(adapterproto.MethodAdapterAvailability, adapterproto.AvailabilityParams{Conditions: []adapterproto.Availability{}})
	}
	eventually(t, "the engine to hold one notification and the host the rest", func() bool {
		return len(r.eng.of("notified")) == 1 && r.host.Counts("chat").Buffered == sent
	})

	for ping := 1; ping <= adapterhost.OfflineAfterPingFailures; ping++ {
		r.advance(p.PingInterval)
		waitPings(t, a, ping)
		r.advance(p.PingTimeout)
	}
	r.waitEvents("gone", 1)
	kinds := r.eng.kinds()
	for i, k := range kinds {
		if k != "gone" {
			continue
		}
		if after := slices.Index(kinds[i+1:], "notified"); after >= 0 {
			t.Fatalf("a notification reached the engine after it was told the adapter is gone: %v", kinds)
		}
	}
	if c := r.host.Counts("chat"); c.Buffered != 0 {
		t.Fatalf("%d notifications are still counted as held after the connection ended", c.Buffered)
	}
}

// The wait before the next pass starts at the minimum, doubles after every failed
// pass or lost connection and stops at the maximum; a connection that stayed in
// service for StableAfter makes the next wait begin at the minimum again.
func TestRestartBackoffDoublesIsCappedAndBeginsAgainAfterAStableConnection(t *testing.T) {
	params := testParams()
	params.RestartBackoffMin = time.Second
	params.RestartBackoffMax = 8 * time.Second
	params.StableAfter = 30 * time.Second
	const failures = 5
	r := newRig(t, rigOptions{params: params})
	r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
		if n <= failures {
			return nil, errors.New("the program is not there")
		}
		return r.fakeProcess(inst), nil
	}
	r.start()

	// Five failed passes: the waits written out by hand.
	for i, wait := range []time.Duration{1, 2, 4, 8, 8} {
		wait *= time.Second
		launches := i + 1
		r.waitLaunches(launches)
		r.settle()
		if got := len(r.goneCauses("chat")); got != launches {
			t.Fatalf("pass %d: %d reports, want %d", launches, got, launches)
		}
		r.advance(wait - time.Nanosecond)
		if n := r.lau.launches(); n != launches {
			t.Fatalf("pass %d: launched again a nanosecond before the wait of %v ended", launches, wait)
		}
		r.advance(time.Nanosecond)
		r.waitLaunches(launches + 1)
	}
	for _, c := range r.goneCauses("chat") {
		if c != adapterhost.GoneStartFailed {
			t.Fatalf("a failed launch was reported as %v", c)
		}
	}

	// The sixth pass is in service. It is lost before StableAfter: the wait goes on
	// where it was, at the cap.
	r.ready("chat", 1)
	r.advance(params.StableAfter - time.Nanosecond)
	if err := r.lau.proc(0).Kill(); err != nil {
		t.Fatal(err)
	}
	r.waitDisconnected("chat")
	r.settle()
	r.advance(8*time.Second - time.Nanosecond)
	if n := r.lau.launches(); n != failures+1 {
		t.Fatalf("launched again before the capped wait of 8s ended: %d launches", n)
	}
	r.advance(time.Nanosecond)
	r.waitLaunches(failures + 2)

	// The seventh stays in service for StableAfter and is then lost: the wait begins
	// again at the minimum.
	r.ready("chat", 2)
	r.advance(params.StableAfter)
	if err := r.lau.proc(1).Kill(); err != nil {
		t.Fatal(err)
	}
	r.waitDisconnected("chat")
	r.settle()
	r.advance(time.Second - time.Nanosecond)
	if n := r.lau.launches(); n != failures+2 {
		t.Fatalf("launched again before the minimum wait after a stable connection: %d launches", n)
	}
	r.advance(time.Nanosecond)
	r.waitLaunches(failures + 3)
	r.ready("chat", 3)

	// Every loss of a connection that was in service is reported as such.
	var ended int
	for _, c := range r.goneCauses("chat") {
		if c == adapterhost.GoneConnectionEnded {
			ended++
		}
	}
	if ended != 2 {
		t.Fatalf("%d connection_ended reports, want 2", ended)
	}
}

// An instance can be started and stopped by the engine: EnsureRunning is idempotent
// and does nothing before Start, Stop ends the adapter gracefully and reports
// nothing, and an instance stopped while it still ends is not started twice.
func TestEnsureRunningAndStopOfAnInstance(t *testing.T) {
	t.Run("before Start nothing happens; twice is one adapter; Stop is graceful and silent", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		r.host.EnsureRunning("chat")
		if n := r.lau.launches(); n != 0 {
			t.Fatalf("EnsureRunning before Start launched %d adapters", n)
		}
		// The instance is not enabled in the configuration when the host starts.
		r.src.instances["chat"] = config.Instance{Adapter: r.opts.ID}
		r.start()
		if n := r.lau.launches(); n != 0 {
			t.Fatalf("Start launched %d adapters for instances that are not enabled", n)
		}
		r.host.EnsureRunning("chat")
		r.host.EnsureRunning("chat")
		r.ready("chat", 1)
		if n := r.lau.launches(); n != 1 {
			t.Fatalf("%d launches after two EnsureRunning, want 1", n)
		}

		r.host.Stop("chat", adapterproto.ShutdownRestart)
		r.host.Stop("chat", adapterproto.ShutdownRestart)
		r.waitDisconnected("chat")
		eventually(t, "the supervision to end", func() bool { return zeroCounts(r.host.Counts("chat")) })
		eventually(t, "Stop to send shutdown", func() bool { return slices.Contains(r.tl.written(), adapterproto.MethodShutdown) })
		if got := r.eng.of("gone"); len(got) != 0 {
			t.Fatalf("Stop reported %d times to the engine", len(got))
		}
		if k := r.lau.proc(0).(*tapProcess).killed.Load(); k != 0 {
			t.Fatalf("an adapter that ended by itself was killed %d times", k)
		}
		r.advance(time.Hour)
		if n := r.lau.launches(); n != 1 {
			t.Fatalf("a stopped instance was started again: %d launches", n)
		}

		// Once it has ended, EnsureRunning starts it again.
		r.host.EnsureRunning("chat")
		r.waitLaunches(2)
		r.waitConnected("chat")
	})

	t.Run("an instance started while it is still ending waits for the old adapter", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		var first *pipeProcess
		r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
			pp, _ := r.scriptedProcess(nil, n == 1)
			if n == 1 {
				first = pp
			}
			return pp, nil
		}
		r.start()
		r.waitConnected("chat")
		r.settle()

		r.host.Stop("chat", adapterproto.ShutdownRestart)
		r.host.EnsureRunning("chat")
		// The old adapter is stubborn: it ends only when killed, after ShutdownWait.
		<-first.serveDone
		eventually(t, "the shutdown wait to be armed", func() bool { return r.clk.Timers() >= 1 })
		r.settle()
		if n := r.lau.launches(); n != 1 {
			t.Fatalf("the instance was launched again while the old adapter was ending: %d launches", n)
		}
		r.clk.Advance(testParams().ShutdownWait)
		eventually(t, "the old adapter to be killed", func() bool { return first.killed() == 1 })
		first.release()
		r.waitLaunches(2)
		r.waitConnected("chat")
	})
}

// Stop returns at once and the process may live up to ShutdownWait (and as long
// again after the kill) with the files of its version open, so the host says that
// the instance is still stopping until its supervision has finished.
func TestAnInstanceIsStoppingUntilItsProcessHasEnded(t *testing.T) {
	r := newRig(t, rigOptions{})
	var first *pipeProcess
	r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
		pp, _ := r.scriptedProcess(nil, true)
		first = pp
		return pp, nil
	}
	if r.host.Stopping("chat") {
		t.Fatal("an instance the host has not started is stopping")
	}
	r.start()
	r.waitConnected("chat")
	r.settle()
	if r.host.Stopping("chat") {
		t.Fatal("an instance that is running is stopping")
	}

	r.host.Stop("chat", adapterproto.ShutdownRestart)
	if !r.host.Stopping("chat") {
		t.Fatal("an instance Stop was called for is not stopping when Stop returns")
	}
	// The adapter is stubborn: it ends only when killed, after ShutdownWait.
	<-first.serveDone
	eventually(t, "the shutdown wait to be armed", func() bool { return r.clk.Timers() >= 1 })
	if !r.host.Stopping("chat") {
		t.Fatal("an instance whose process is still ending is not stopping")
	}
	r.clk.Advance(testParams().ShutdownWait)
	eventually(t, "the process to be killed", func() bool { return first.killed() == 1 })
	first.release()
	eventually(t, "the instance to have stopped", func() bool { return !r.host.Stopping("chat") })
	if r.host.Stopping("chat") {
		t.Fatal("an instance whose process has ended is stopping")
	}
}

// An instance that is stopped again before its new supervisor has launched is
// still stopping while the process of the supervisor before it ends: that process
// holds the files of its version just as the one the caller stopped last did, and
// no supervisor started after it launches before it has ended.
func TestAnInstanceStoppedBeforeItLaunchedIsStoppingUntilThePreviousProcessHasEnded(t *testing.T) {
	r := newRig(t, rigOptions{})
	var first *pipeProcess
	r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
		pp, _ := r.scriptedProcess(nil, n == 1)
		if n == 1 {
			first = pp
		}
		return pp, nil
	}
	r.start()
	r.waitConnected("chat")
	r.settle()

	r.host.Stop("chat", adapterproto.ShutdownRestart)
	r.host.EnsureRunning("chat")
	r.host.Stop("chat", adapterproto.ShutdownRestart)
	// The first adapter is stubborn: it ends only when killed, after ShutdownWait.
	<-first.serveDone
	eventually(t, "the shutdown wait to be armed", func() bool { return r.clk.Timers() >= 1 })

	// The supervisor that was stopped before it launched has nothing of its own to
	// end; the instance stays stopping all the same. The spin gives a supervisor
	// that finishes early time to do so.
	for end := time.Now().Add(100 * time.Millisecond); time.Now().Before(end); time.Sleep(time.Millisecond) {
		if !r.host.Stopping("chat") {
			t.Fatal("an instance whose previous process is still ending is not stopping")
		}
	}
	// A restart now waits for that process too.
	r.host.EnsureRunning("chat")
	r.settle()
	if n := r.lau.launches(); n != 1 {
		t.Fatalf("the instance was launched again while the first adapter was ending: %d launches", n)
	}

	r.clk.Advance(testParams().ShutdownWait)
	eventually(t, "the first adapter to be killed", func() bool { return first.killed() == 1 })
	first.release()
	r.waitLaunches(2)
	r.waitConnected("chat")
}

// Shutdown sends shutdown to every adapter in service, closes the stream and waits
// for the process at most ShutdownWait before it kills it; the host reports nothing.
func TestShutdownSendsShutdownWaitsThenKills(t *testing.T) {
	t.Run("a process that ends by itself is not killed", func(t *testing.T) {
		r := newRig(t, rigOptions{instances: []string{"chat", "mail"}})
		r.start()
		r.waitConnected("chat")
		r.waitConnected("mail")
		r.settle()
		if err := r.host.Shutdown(timeoutCtx(t), adapterproto.ShutdownStop); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		written := r.tl.written()
		if n := countOf(written, adapterproto.MethodShutdown); n != 2 {
			t.Fatalf("%d shutdown requests written, want 2: %v", n, written)
		}
		for i := range 2 {
			if k := r.lau.proc(i).(*tapProcess).killed.Load(); k != 0 {
				t.Fatalf("process %d ended by itself and was killed %d times", i, k)
			}
		}
		if got := r.eng.of("gone"); len(got) != 0 {
			t.Fatalf("Shutdown reported to the engine: %d reports", len(got))
		}
		if n := r.clk.Timers(); n != 0 {
			t.Fatalf("%d timers of the host are still armed after Shutdown", n)
		}
		if c := r.host.Counts("chat"); c.Connected || c.Working {
			t.Fatalf("counts after Shutdown = %+v, want all zero", c)
		}
		if _, err := r.host.CreateSession(timeoutCtx(t), "chat", adapterproto.SessionCreateParams{}); !errors.Is(err, adapterhost.ErrNotConnected) {
			t.Fatalf("a call after Shutdown returned %v, want ErrNotConnected", err)
		}
		if err := r.host.Start(timeoutCtx(t), r.env(false)); err == nil {
			t.Fatal("Start after Shutdown succeeded")
		}
		if err := r.host.Shutdown(timeoutCtx(t), adapterproto.ShutdownStop); err != nil {
			t.Fatalf("a second Shutdown: %v", err)
		}
	})

	t.Run("a process that does not end is killed after ShutdownWait", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		var pp *pipeProcess
		var a *scripted
		r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
			pp, a = r.scriptedProcess(nil, true)
			return pp, nil
		}
		r.start()
		r.waitConnected("chat")
		r.settle()
		done := make(chan error, 1)
		go func() { done <- r.host.Shutdown(timeoutCtx(t), adapterproto.ShutdownStop) }()
		eventually(t, "the adapter to receive shutdown", func() bool { return a.count(adapterproto.MethodShutdown) == 1 })
		<-pp.serveDone
		eventually(t, "the shutdown wait to be armed", func() bool { return r.clk.Timers() >= 1 })
		select {
		case err := <-done:
			t.Fatalf("Shutdown returned (%v) before ShutdownWait passed", err)
		default:
		}
		if pp.killed() != 0 {
			t.Fatal("the process was killed before ShutdownWait passed")
		}
		r.clk.Advance(testParams().ShutdownWait - time.Nanosecond)
		if pp.killed() != 0 {
			t.Fatal("the process was killed a nanosecond before ShutdownWait passed")
		}
		r.clk.Advance(time.Nanosecond)
		eventually(t, "the process to be killed", func() bool { return pp.killed() == 1 })
		// A process that stays after the kill does not hold the host.
		r.clk.Advance(testParams().ShutdownWait)
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
		case <-time.After(waitLimit):
			t.Fatal("Shutdown did not return although the process cannot be ended")
		}
		pp.release()
	})

	t.Run("a context that ends first ends the rest as Abort does", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		var pp *pipeProcess
		r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
			pp, _ = r.scriptedProcess(nil, true)
			return pp, nil
		}
		r.start()
		r.waitConnected("chat")
		r.settle()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- r.host.Shutdown(ctx, adapterproto.ShutdownStop) }()
		<-pp.serveDone
		eventually(t, "the shutdown wait to be armed", func() bool { return r.clk.Timers() >= 1 })
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Shutdown returned %v, want the context's error", err)
			}
		case <-time.After(waitLimit):
			t.Fatal("Shutdown did not return when its context ended")
		}
		eventually(t, "the process to be killed", func() bool { return pp.killed() >= 1 })
		pp.release()
	})
}

// Abort kills every process at once and writes nothing to it: not shutdown, not
// even what is left of the handshake.
func TestAbortKillsAtOnceAndSendsNoShutdown(t *testing.T) {
	t.Run("in service", func(t *testing.T) {
		r := newRig(t, rigOptions{instances: []string{"chat", "mail"}})
		r.start()
		r.waitConnected("chat")
		r.waitConnected("mail")
		r.waitEvents("probed", 2)
		r.settle()
		before := r.tl.written()
		r.host.Abort()
		after := r.tl.written()
		if !slices.Equal(before, after) {
			t.Fatalf("Abort wrote to the adapters: %v", after[len(before):])
		}
		if countOf(after, adapterproto.MethodShutdown) != 0 {
			t.Fatalf("Abort sent shutdown: %v", after)
		}
		for i := range 2 {
			if k := r.lau.proc(i).(*tapProcess).killed.Load(); k < 1 {
				t.Fatalf("process %d killed %d times, want at least once", i, k)
			}
		}
		if got := r.eng.of("gone"); len(got) != 0 {
			t.Fatalf("Abort reported %d times to the engine", len(got))
		}
		if c := r.host.Counts("chat"); c.Connected || c.Working || c.Handled != 0 {
			t.Fatalf("counts after Abort = %+v, want all zero", c)
		}
		if err := r.host.Start(timeoutCtx(t), r.env(false)); err == nil {
			t.Fatal("Start after Abort succeeded")
		}
		r.host.Abort() // idempotent
		if err := r.host.Shutdown(timeoutCtx(t), adapterproto.ShutdownStop); err != nil {
			t.Fatalf("Shutdown after Abort: %v", err)
		}
	})

	t.Run("in the middle of a handshake", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		k := r.knobsOf("chat")
		if err := k.PauseAt(fakeadapter.PauseStartBeforeReply); err != nil {
			t.Fatal(err)
		}
		started := make(chan error, 1)
		go func() { started <- r.host.Start(timeoutCtx(t), r.env(false)) }()
		if err := k.WaitPaused(timeoutCtx(t), fakeadapter.PauseStartBeforeReply); err != nil {
			t.Fatal(err)
		}
		before := r.tl.written()
		r.host.Abort()
		if after := r.tl.written(); !slices.Equal(before, after) {
			t.Fatalf("Abort wrote %v to an adapter whose adapter.start was still out", after[len(before):])
		}
		if k := r.lau.proc(0).(*tapProcess).killed.Load(); k < 1 {
			t.Fatalf("the process was not killed")
		}
		select {
		case <-started:
		case <-time.After(waitLimit):
			t.Fatal("Start did not return after Abort")
		}
		k.Release(fakeadapter.PauseStartBeforeReply)
	})
}

func countOf(list []string, s string) int {
	n := 0
	for _, x := range list {
		if x == s {
			n++
		}
	}
	return n
}

// Start returns when every enabled instance has started or been judged offline, or
// when StartWait has passed on the host's clock.
func TestStartReturnsWhenEveryEnabledInstanceHasStartedOrBeenJudgedOffline(t *testing.T) {
	params := testParams()
	// A long backoff keeps the instance that failed from trying again while the
	// clock moves for StartWait.
	params.RestartBackoffMin = time.Hour
	params.RestartBackoffMax = time.Hour
	// The handshake limit is longer than StartWait: it is StartWait that ends the
	// wait of Start, not the limit of adapter.start.
	params.HandshakeTimeout = 2 * time.Hour
	r := newRig(t, rigOptions{instances: []string{"broken", "slow"}, params: params})
	r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
		if inst.ID == "broken" {
			return nil, errors.New("the program is not there")
		}
		return r.fakeProcess(inst), nil
	}
	k := r.knobsOf("slow")
	if err := k.PauseAt(fakeadapter.PauseStartBeforeReply); err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() { started <- r.host.Start(timeoutCtx(t), r.env(false)) }()

	if err := k.WaitPaused(timeoutCtx(t), fakeadapter.PauseStartBeforeReply); err != nil {
		t.Fatal(err)
	}
	r.waitEvents("gone", 1)
	r.settle()
	// "broken" has failed its first pass and is reported; "slow" is waiting for
	// adapter.start: Start has not returned.
	select {
	case err := <-started:
		t.Fatalf("Start returned (%v) while an instance was neither started nor judged offline", err)
	default:
	}
	if got := r.goneCauses("broken"); !slices.Equal(got, []adapterhost.GoneCause{adapterhost.GoneStartFailed}) {
		t.Fatalf("broken was reported %v", got)
	}
	if got := r.goneCauses("slow"); len(got) != 0 {
		t.Fatalf("slow was reported %v before StartWait passed", got)
	}

	r.advance(params.StartWait - time.Nanosecond)
	select {
	case err := <-started:
		t.Fatalf("Start returned (%v) a nanosecond before StartWait passed", err)
	default:
	}
	r.advance(time.Nanosecond)
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("Start = %v", err)
		}
	case <-time.After(waitLimit):
		t.Fatal("Start did not return after StartWait passed")
	}
	if got := r.goneCauses("slow"); !slices.Equal(got, []adapterhost.GoneCause{adapterhost.GoneStartFailed}) {
		t.Fatalf("slow was reported %v after StartWait, want [start_failed] once", got)
	}
	if got := r.goneCauses("broken"); len(got) != 1 {
		t.Fatalf("broken was reported %v, want once", got)
	}

	// It goes on trying: the answer that comes late is taken on.
	if r.host.Counts("slow").Connected {
		t.Fatal("slow is in service before adapter.start was answered")
	}
	k.Release(fakeadapter.PauseStartBeforeReply)
	r.waitConnected("slow")
	if n := len(r.eng.of("started")); n != 1 {
		t.Fatalf("%d adapter.start results reached the engine, want 1", n)
	}
}

// Start refuses what it cannot start, once.
func TestStartRefusesWhatItCannotStart(t *testing.T) {
	r := newRig(t, rigOptions{})
	good := r.env(false)
	for _, tc := range []struct {
		name string
		env  func(adapterhost.Env) adapterhost.Env
	}{
		{"a home that is not absolute", func(e adapterhost.Env) adapterhost.Env { e.Home = "data"; return e }},
		{"no home", func(e adapterhost.Env) adapterhost.Env { e.Home = ""; return e }},
		{"no source", func(e adapterhost.Env) adapterhost.Env { e.Source = nil; return e }},
		{"no logs", func(e adapterhost.Env) adapterhost.Env { e.Logs = nil; return e }},
		{"no engine", func(e adapterhost.Env) adapterhost.Env { e.Engine = nil; return e }},
	} {
		if err := r.host.Start(timeoutCtx(t), tc.env(good)); err == nil {
			t.Errorf("%s: Start succeeded", tc.name)
		}
	}
	if n := r.lau.launches(); n != 0 {
		t.Fatalf("a refused Start launched %d adapters", n)
	}
	r.start()
	if err := r.host.Start(timeoutCtx(t), good); err == nil {
		t.Fatal("a second Start succeeded")
	}
	if _, err := adapterhost.New(adapterhost.Config{}); err == nil {
		t.Fatal("New without a launcher succeeded")
	}
	neg := adapterhost.Params{PingTimeout: -time.Second}
	if _, err := adapterhost.New(adapterhost.Config{Launcher: r.lau, Params: neg}); err == nil {
		t.Fatal("New with a negative duration succeeded")
	}
}

// Start ends when its context does, and the host keeps running.
func TestStartEndsWithItsContext(t *testing.T) {
	r := newRig(t, rigOptions{})
	k := r.knobsOf("chat")
	if err := k.PauseAt(fakeadapter.PauseStartBeforeReply); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- r.host.Start(ctx, r.env(false)) }()
	if err := k.WaitPaused(timeoutCtx(t), fakeadapter.PauseStartBeforeReply); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-started:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start = %v, want the context's error", err)
		}
	case <-time.After(waitLimit):
		t.Fatal("Start did not return when its context ended")
	}
	// The supervision does not end with the context of Start.
	k.Release(fakeadapter.PauseStartBeforeReply)
	r.waitConnected("chat")
}

// The host sends the lines of the handshake itself, and what the engine and the
// processes see of a closed stream is a closed stream.
var _ io.Closer = (*tapWriter)(nil)

// A launched process is told where its lock is, and the host is told by the same
// definition: LockPath.
func TestTheLockPathAndTheInstanceAreWhatTheLauncherAndTheAdapterAreTold(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.start()
	r.waitConnected("chat")
	inst := r.lau.launched(0)
	wantLock := adapterhost.LockPath(r.home, "chat")
	if inst.LockPath != wantLock {
		t.Fatalf("Instance.LockPath = %q, want %q", inst.LockPath, wantLock)
	}
	wantDir := r.home + string(os.PathSeparator) + "config" + string(os.PathSeparator) + "adapters" + string(os.PathSeparator) + r.opts.ID + string(os.PathSeparator) + "versions" + string(os.PathSeparator) + r.opts.Version
	if inst.Dir != wantDir {
		t.Fatalf("Instance.Dir = %q, want %q", inst.Dir, wantDir)
	}
	if inst.ID != "chat" || inst.Adapter.ID != r.opts.ID || inst.Adapter.Version != r.opts.Version {
		t.Fatalf("Instance = %+v", inst)
	}
	if inst.Stderr == nil {
		t.Fatal("Instance.Stderr is nil")
	}
}

// A package that cannot run is reported with the reason that says so, and the
// other instances go on: an instance with a package that could not be loaded, one
// for another platform or protocol is judged before anything is launched, and a
// launch the system refuses for want of the program or of permission is told by
// what it answers. Any other failure to start stays a start that failed. A new
// package is taken on without the host being restarted.
func TestAPackageThatCannotRunIsReportedWithWhyAndTheOtherInstancesGoOn(t *testing.T) {
	good := fakeadapter.DefaultManifestOptions().Manifest()
	// bad is the adapter of the instance "broken"; edit makes its package wrong.
	bad := func(edit func(*config.Adapter)) config.Adapter {
		a := config.Adapter{ID: "bad", Version: good.Version, Manifest: good}
		a.Manifest.ID = "bad"
		edit(&a)
		return a
	}
	pathErr := func(err error) error { return &fs.PathError{Op: "fork/exec", Path: "adapter", Err: err} }
	for _, tc := range []struct {
		name     string
		adapter  config.Adapter
		launch   error // what the launcher answers for "broken"
		want     adapterhost.GoneCause
		launches int // of "broken" before the package is replaced
	}{
		{"a package that could not be loaded", bad(func(a *config.Adapter) {
			a.Manifest = manifest.Manifest{}
			a.Fault = &config.FileError{Path: "adapters/bad/current.json", Kind: config.KindAdapterCurrent, Reason: config.FileInvalid, Err: errors.New("not json")}
		}), nil, adapterhost.GonePackageInvalid, 0},
		{"a package for another platform", bad(func(a *config.Adapter) {
			a.Manifest.Platforms = []manifest.Platform{{OS: "plan9", Arch: "mips"}}
		}), nil, adapterhost.GonePlatformUnsupported, 0},
		{"a package for protocol versions the node does not speak", bad(func(a *config.Adapter) {
			a.Manifest.Protocol = adapterproto.Range{Min: adapterproto.ProtocolMax + 1, Max: adapterproto.ProtocolMax + 2}
		}), nil, adapterhost.GoneProtocolUnsupported, 0},
		{"a program that is not there", bad(func(*config.Adapter) {}), pathErr(fs.ErrNotExist), adapterhost.GoneProgramMissing, 1},
		{"a program that may not be run", bad(func(*config.Adapter) {}), pathErr(fs.ErrPermission), adapterhost.GoneProgramNotExecutable, 1},
		{"a launch that fails otherwise", bad(func(*config.Adapter) {}), errors.New("out of processes"), adapterhost.GoneStartFailed, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var healed atomic.Bool
			r := newRig(t, rigOptions{instances: []string{"fine", "broken"}, launch: func(r *rig, n int, inst adapterhost.Instance) (adapterhost.Process, error) {
				if inst.ID == "broken" && tc.launch != nil && !healed.Load() {
					return nil, tc.launch
				}
				return r.fakeProcess(inst), nil
			}})
			r.src.setAdapters(config.Adapter{ID: good.ID, Version: good.Version, Manifest: good}, tc.adapter)
			r.src.setInstance("broken", config.Instance{Adapter: "bad", Enabled: true})

			r.start()
			r.waitConnected("fine")
			r.waitEvents("gone", 1)
			if got := r.goneCauses("broken"); !slices.Equal(got, []adapterhost.GoneCause{tc.want}) {
				t.Fatalf("broken was reported %v, want [%s]", got, tc.want)
			}
			if got := r.goneCauses("fine"); len(got) != 0 {
				t.Errorf("fine was reported %v", got)
			}
			if got := launchesOf(r.lau, "broken"); got != tc.launches {
				t.Errorf("broken was launched %d times, want %d", got, tc.launches)
			}

			// The package is replaced by a good one: the next pass takes it on.
			healed.Store(true)
			r.src.setAdapters(config.Adapter{ID: good.ID, Version: good.Version, Manifest: good})
			r.src.setInstance("broken", config.Instance{Adapter: good.ID, Enabled: true})
			r.advance(testParams().RestartBackoffMax, "broken")
			r.waitConnected("broken")
		})
	}
}

func launchesOf(l *launcherDouble, instance string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, in := range l.instances {
		if in.ID == instance {
			n++
		}
	}
	return n
}
