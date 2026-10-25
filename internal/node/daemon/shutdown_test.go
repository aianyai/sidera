package daemon_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

// The shutdown takes the steps in a fixed order: first the engine
// refuses new requests, then the interfaces stop within a grace, then the adapters,
// then the engine ends its loop, and last the lock is let go and the log closed.
// An adapter must never be stopped while a request that depends on it can still
// come in, and the lock must go last: while it is held no other daemon starts over
// state that this one is still writing.
func TestShutdownRunsInTheOrderOfTheDesign(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	p := newProbe(t, c.Home)
	var phaseAtInterfaces, phaseAtAdapters engine.Phase
	var doneAtAdapters bool
	var d *daemon.Daemon
	c.Attach = p.attachments()
	c.Attach.StopInterfaces = func(ctx context.Context) error {
		phaseAtInterfaces = d.Engine().Phase()
		return p.stop("interfaces")(ctx)
	}
	c.Attach.StopAdapters = func(ctx context.Context) error {
		phaseAtAdapters = d.Engine().Phase()
		select {
		case <-d.Engine().Done():
			doneAtAdapters = true
		default:
		}
		// the management interface no longer answers
		if tryManage(c.Home, "endpoints.list") == 200 {
			t.Error("the management interface still answered when the adapters were stopped")
		}
		return p.stop("adapters")(ctx)
	}
	var err error
	d, err = daemon.Start(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	d.RequestStop("test")
	if err := d.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	want := []string{
		"shutdown 1 begin", "shutdown 1 end", "shutdown 2 begin", "shutdown 2 end", "shutdown 3 begin", "shutdown 3 end",
		"shutdown 4 begin", "shutdown 4 end", "shutdown 5 begin",
	}
	got := steps(logLines(t, c.Home))
	if i := indexOf(got, "shutdown 1 begin"); i < 0 || !equal(got[i:], want) {
		t.Errorf("the steps of the shutdown are\n%s\nwant\n%s", strings.Join(got[max(i, 0):], "\n"), strings.Join(want, "\n"))
	}
	if phaseAtInterfaces != engine.PhaseStopping || phaseAtAdapters != engine.PhaseStopping || doneAtAdapters {
		t.Errorf("the engine was %s when the interfaces stopped, %s (ended: %v) when the adapters did; want it refusing, not yet ended", phaseAtInterfaces, phaseAtAdapters, doneAtAdapters)
	}
	if len(p.stopped) != 2 || !strings.HasPrefix(p.stopped[0], "interfaces at shutdown 2 begin") || !strings.HasPrefix(p.stopped[1], "adapters at shutdown 3 begin") {
		t.Errorf("stops %v; want the interfaces inside step 2 and the adapters inside step 3", p.stopped)
	}
	select {
	case <-d.Engine().Done():
	default:
		t.Error("the engine did not end")
	}
	if err := d.Engine().Err(); err != nil {
		t.Errorf("the engine ended with %v", err)
	}
	// the lock is let go: the same directory starts again
	started(t, c)
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// RequestStop is for any goroutine and any number of times: the shutdown runs
// once.
func TestRequestStopRunsTheShutdownOnce(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	d := started(t, c)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); d.RequestStop("again and again") }()
	}
	wg.Wait()
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := d.Wait(); err != nil {
		t.Errorf("a second Wait: %v", err)
	}
	d.RequestStop("after it ended")
	n := 0
	for _, s := range steps(logLines(t, c.Home)) {
		if s == "shutdown 1 begin" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the shutdown ran %d times", n)
	}
}

// The owner stops the daemon through the management interface: the request is
// answered, then the daemon shuts down in the normal way and exits cleanly. The
// stop is an operation like the others, so its result is stored and a resend of it
// gets that result; a daemon that starts again and is sent the same request is
// stopped by it again, for it asks for the stop whether or not it ran.
func TestTheOwnerStopsTheDaemonThroughTheManagementInterface(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	d, err := daemon.Start(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("0", 30) + "7a"
	status, body := manage(t, c.Home, "daemon.stop", map[string]any{"operation_id": id})
	if status != 200 || !strings.Contains(string(body), `"replayed":false`) || !strings.Contains(string(body), `"result":{"stopping":true}`) {
		t.Fatalf("daemon.stop answered %d %s", status, body)
	}
	waitDone(t, d)
	if err := d.Wait(); err != nil {
		t.Fatalf("the daemon stopped with %v", err)
	}
	if got := steps(logLines(t, c.Home)); indexOf(got, "shutdown 5 begin") < 0 {
		t.Errorf("the shutdown did not run to its last step: %v", got)
	}

	// started again, the resend gets the stored result and stops the daemon again
	d2, err := daemon.Start(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	status, body = manage(t, c.Home, "daemon.stop", map[string]any{"operation_id": id})
	if status != 200 || !strings.Contains(string(body), `"replayed":true`) || !strings.Contains(string(body), `"result":{"stopping":true}`) {
		t.Fatalf("the resend answered %d %s", status, body)
	}
	waitDone(t, d2)
	if err := d2.Wait(); err != nil {
		t.Fatalf("the second daemon stopped with %v", err)
	}
}

func waitDone(t testing.TB, d *daemon.Daemon) {
	t.Helper()
	select {
	case <-d.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the daemon did not stop")
	}
}

// Run is the daemon as a process runs it: it starts, and returns when the context
// ends (the signal handler of the program cancels it), after a normal shutdown.
func TestRunReturnsAfterANormalShutdownWhenItsContextEnds(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx, c) }()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if tryManage(c.Home, "endpoints.list") == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the daemon never answered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return")
	}
	if got := steps(logLines(t, c.Home)); indexOf(got, "shutdown 5 begin") < 0 {
		t.Errorf("the shutdown did not run: %v", got)
	}
}

// A failure of the disk that leaves the state of memory and disk in doubt ends the
// daemon: no more is written, the interfaces and adapters are stopped within a
// short time, and the process exits with the code of an I/O error, so that the
// next start scans the disk again.
func TestAFatalStorageErrorEndsTheDaemonWithTheExitCodeOfAnIOError(t *testing.T) {
	noLeaks(t)
	clk := clocktest.NewFake(t0)
	home := newHome(t)
	mem := fsxtest.NewMem()
	mkSocketDirs(t, home)
	if _, err := daemon.Init(context.Background(), memConfig(home, clk, mem)); err != nil {
		t.Fatal(err)
	}
	faulty := fsxtest.NewFaulty(t, mem)
	// the directory of the result of the stop cannot be made durable: after the
	// rename, whether the result is on disk is not known
	rule := faulty.Fail(fsxtest.Match{Tx: "daemon-stop", Step: fsx.StepSyncDir}, errors.New("input/output error"))
	c := memConfig(home, clk, faulty)
	var stoppedAdapters bool
	c.Attach.StopAdapters = func(context.Context) error { stoppedAdapters = true; return nil }
	d, err := daemon.Start(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	status, body := manage(t, home, "daemon.stop", map[string]any{"operation_id": strings.Repeat("0", 30) + "01"})
	if status != 500 || !strings.Contains(string(body), "storage_fatal") {
		t.Errorf("the request that met the failure answered %d %s", status, body)
	}
	waitDone(t, d)
	err = d.Wait()
	var fatal *daemon.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("Wait = %v; want a *daemon.FatalError", err)
	}
	if got := daemon.ExitCode(err); got != daemon.ExitFatal || daemon.ExitFatal != 74 {
		t.Errorf("exit code %d, want 74", got)
	}
	if daemon.ExitCode(nil) != 0 || daemon.ExitCode(errors.New("other")) != 1 {
		t.Error("the exit code of success and of any other failure is 0 and 1")
	}
	if !stoppedAdapters {
		t.Error("the adapters were not stopped after the fatal error")
	}
	if rule.Hits() == 0 {
		t.Fatal("the injected fault was never hit")
	}
	// the lock was let go: the next start scans the disk again
	d2, err := daemon.Start(context.Background(), memConfig(home, clk, mem))
	if err != nil {
		t.Fatalf("the start after a fatal error: %v", err)
	}
	d2.RequestStop("test over")
	if err := d2.Wait(); err != nil {
		t.Fatal(err)
	}
}
