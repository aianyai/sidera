package daemon_test

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/engine"
)

// The instance lock is what keeps a second daemon from starting over state that
// this one is still writing. When the engine has not ended within the grace of the
// shutdown (a task of the loop that never returns) it may still be writing, so the
// lock is not let go: the process exit does it. Wait says so, and the log, which the
// engine goes on using, stays open.
func TestTheLockIsNotLetGoWhileTheEngineHasNotStopped(t *testing.T) {
	noLeaks(t)
	n := newMemNode(t)
	release := make(chan struct{})
	running := make(chan struct{}, 1)
	c := memConfig(n.home, n.clk, n.mem)
	c.ShutdownGrace = 300 * time.Millisecond
	c.Attach.Periodic = []daemon.PeriodicTask{{Name: "stuck", Every: time.Minute, Run: func(engine.TaskInput) error {
		select {
		case running <- struct{}{}:
		default:
		}
		<-release
		return nil
	}}}
	d, err := daemon.Start(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	n.clk.Advance(time.Minute)
	select {
	case <-running:
	case <-time.After(30 * time.Second):
		t.Fatal("the task of the loop never ran")
	}
	d.RequestStop("test")
	select {
	case <-d.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the shutdown did not give up on the engine")
	}
	if err := d.Wait(); !errors.Is(err, daemon.ErrEngineNotStopped) || daemon.ExitCode(err) != 1 {
		t.Errorf("Wait = %v (exit code %d); want ErrEngineNotStopped and exit code 1", err, daemon.ExitCode(err))
	}
	other := func(where string) {
		t.Helper()
		if _, err := daemon.Start(context.Background(), memConfig(n.home, n.clk, n.mem)); !errors.Is(err, daemon.ErrAlreadyRunning) {
			t.Errorf("%s: a second daemon started over a home whose engine may still write: %v", where, err)
		}
	}
	other("while the engine is stuck")
	close(release)
	select {
	case <-d.Engine().Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the engine did not end once its task returned")
	}
	// the lock stays until the process exits: the engine has ended only now, and the
	// daemon has already said that it did not
	other("after the engine has ended")
	log, err := n.mem.ReadFile("logs/daemon.log")
	if err != nil || !strings.Contains(string(log), "the engine has not stopped") {
		t.Errorf("the log does not say that the lock is held: %v\n%s", err, log)
	}
}

// A request to stop that comes from another goroutine while a start fails is read
// in order with the unwinding of the start: the reason is the one that was given,
// and the race detector sees no unsynchronised read of it.
func TestARequestToStopThatRacesWithAFailedStartIsReadInOrder(t *testing.T) {
	for i := 0; i < 8; i++ {
		c, _ := initialized(t)
		var wg sync.WaitGroup
		c.Attach.TrustSync = func(_ context.Context, env *daemon.Env) error {
			wg.Add(1)
			go func() { defer wg.Done(); env.RequestStop("asked by another goroutine") }()
			return errors.New("the trust hook failed")
		}
		if _, err := daemon.Start(context.Background(), c); err == nil {
			t.Fatal("the start went through")
		}
		wg.Wait()
	}
}

// The context of Start ends the start: it stops before the next step, what had
// begun is shut down in the order of the shutdown, the lock is let go, and the
// error says that the start was interrupted and wraps the error of the context. It
// is not a failure of the disk.
func TestACancelledContextStopsTheStartBeforeTheNextStep(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Attach.TrustSync = func(context.Context, *daemon.Env) error { cancel(); return nil }
	_, err := daemon.Start(ctx, c)
	if !errors.Is(err, context.Canceled) || daemon.ExitCode(err) != 1 {
		t.Fatalf("Start = %v (exit code %d); want an error wrapping context.Canceled and exit code 1", err, daemon.ExitCode(err))
	}
	got := steps(logLines(t, c.Home))
	if i := indexOf(got, "startup 7 end"); i < 0 || indexOf(got, "startup 8 begin") >= 0 {
		t.Errorf("the steps of the start are %v; want them to end after step 7", got)
	}
	if indexOf(got, "shutdown 5 begin") < 0 {
		t.Errorf("what had begun was not shut down: %v", got)
	}
	started(t, c) // the lock is free
}

// A signal that comes while the daemon starts is taken from before the first step:
// Run stops the start between two steps and unwinds it, instead of the process
// being ended with the lock held and a socket left behind.
func TestASignalDuringTheStartStopsItBetweenTwoSteps(t *testing.T) {
	// the signal package starts one goroutine for the process, the first time it is
	// used and for good; it is not the daemon's
	_, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt)
	stopSignals()
	noLeaks(t)
	c, _ := initialized(t)
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Skipf("this platform cannot name the process: %v", err)
	}
	var signalErr error
	c.Attach.TrustSync = func(ctx context.Context, _ *daemon.Env) error {
		if signalErr = self.Signal(syscall.SIGTERM); signalErr != nil {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(30 * time.Second):
			return errors.New("the signal was not taken")
		}
		return nil
	}
	err = daemon.Run(context.Background(), c)
	if signalErr != nil {
		t.Skipf("this platform cannot send itself a signal: %v", signalErr)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v; want an error wrapping context.Canceled", err)
	}
	got := steps(logLines(t, c.Home))
	if indexOf(got, "startup 8 begin") >= 0 || indexOf(got, "shutdown 5 begin") < 0 {
		t.Errorf("the start was not stopped between steps 7 and 8 and unwound: %v", got)
	}
}
