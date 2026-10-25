package engine_test

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

// The loop runs the commands in the order they entered its inbox, whoever sent
// them and however long each takes.
func TestCommandsRunInTheOrderTheyEnteredTheInbox(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	const n = 12
	release := engine.StallLoop(t, e)
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			_, err := e.RaiseAlert(context.Background(), "ordered", "test", fmt.Sprintf("alert %02d", i))
			done <- err
		}()
		engine.WaitInbox(t, e, i+1)
	}
	release()
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Fatalf("RaiseAlert: %v", err)
		}
	}
	alerts, err := e.Alerts(context.Background())
	if err != nil || len(alerts) != n {
		t.Fatalf("Alerts = %d, %v; want %d", len(alerts), err, n)
	}
	for i, a := range alerts {
		if want := fmt.Sprintf("alert %02d", i); a.Detail != want {
			t.Errorf("alert %d is %q, want %q: the loop did not keep the order of its inbox", i, a.Detail, want)
		}
	}
}

// An outside caller does not wait for ever for room in the inbox; the host of
// the adapters, whose order has to be kept and whose back pressure is the
// buffer behind it, does.
func TestWhenTheInboxIsFullAnOutsideCallerIsRefusedAndAnInternalOneWaits(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10, withParams(engine.Params{InboxLen: 2, SubmitWait: 50 * time.Millisecond}))
	e := r.start()
	release := engine.StallLoop(t, e)
	pending := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := e.Endpoints(context.Background()); pending <- err }()
		engine.WaitInbox(t, e, i+1)
	}

	start := time.Now()
	_, err := e.Endpoints(context.Background())
	wantReason(t, err, engine.ReasonLoopBusy)
	if waited := time.Since(start); waited < 40*time.Millisecond {
		t.Errorf("refused after %v, want it to wait the SubmitWait for room first", waited)
	}

	internal := make(chan error, 1)
	go func() { internal <- e.AdapterAdmitted(context.Background(), "chat", r.admit) }()
	select {
	case err := <-internal:
		t.Fatalf("the internal submitter returned %v while the inbox was full", err)
	case <-time.After(200 * time.Millisecond):
	}
	release()
	if err := <-internal; err != nil {
		t.Errorf("the internal submitter: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := <-pending; err != nil {
			t.Errorf("a queued command: %v", err)
		}
	}
}

// A command that has entered the inbox is run even when its caller has gone: the
// loop does not take back what it was handed. The caller finds out only that it
// did not hear the answer.
func TestACommandAbandonedByItsCallerIsStillRun(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	release := engine.StallLoop(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() { _, err := e.RaiseAlert(ctx, "abandoned", "test", "the caller left"); got <- err }()
	engine.WaitInbox(t, e, 1)
	cancel()
	if err := <-got; !errors.Is(err, context.Canceled) {
		t.Errorf("error %v, want the context's", err)
	}
	release()
	settle(t, e)
	alerts, err := e.Alerts(context.Background())
	if err != nil || len(alerts) != 1 || alerts[0].Detail != "the caller left" {
		t.Errorf("Alerts = %+v, %v; want the alert of the abandoned command", alerts, err)
	}
}

func TestACancelledContextIsRefusedBeforeAnythingIsSubmitted(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.RaiseAlert(ctx, "never", "test", "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("error %v, want the context's", err)
	}
	if alerts, _ := e.Alerts(context.Background()); len(alerts) != 0 {
		t.Errorf("a command of a cancelled context ran: %+v", alerts)
	}
}

// A write that failed before its commit point was not acknowledged and changed
// nothing: the caller is told to back off, with the cause, and memory shows what
// the disk shows.
func TestAFailedWriteIsBackpressureAndMemoryKeepsWhatTheDiskHolds(t *testing.T) {
	noLeaks(t)
	for _, tc := range []struct {
		name string
		err  error
		want engine.Reason
	}{
		{"no space", syscall.ENOSPC, engine.ReasonStorageFull},
		{"an I/O error", syscall.EIO, engine.ReasonStorageUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, 10)
			e := r.start()
			id := register(t, e, "a")
			rule := r.faulty.Fail(failMatch("endpoint-update", fsx.StepWrite), tc.err)
			_, err := e.Declare(context.Background(), engineCaller(id), engine.DeclarationPatch{Paused: ptr(true)})
			wantReason(t, err, tc.want)
			if rule.Hits() == 0 {
				t.Fatal("the fault was not injected")
			}
			if d := declared(t, e, id); d.Paused {
				t.Error("memory shows a declaration the disk does not hold")
			}
			for _, ep := range r.scan().Endpoints {
				if ep.ID == id && string(ep.Declaration) != `{"display_name":"Name of a"}` {
					t.Errorf("the record on disk is %s", ep.Declaration)
				}
			}
			if e.Phase() != engine.PhaseRunning {
				t.Errorf("phase %v after a failed write, want the engine to go on", e.Phase())
			}
		})
	}
}

// A failure after the commit point leaves the disk and memory possibly apart,
// so the loop stops for good: the command that met it is told the outcome is not
// known, every later command is refused, and the process is to exit and rescan.
func TestAFatalWriteStopsTheLoopAndRefusesEveryLaterCommand(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	id := register(t, e, "a")
	rule := r.faulty.Fail(failMatch("endpoint-update", fsx.StepSyncDir), syscall.EIO)
	_, err := e.Declare(context.Background(), engineCaller(id), engine.DeclarationPatch{Paused: ptr(true)})
	wantReason(t, err, engine.ReasonStorageFatal)
	if rule.Hits() == 0 {
		t.Fatal("the fault was not injected")
	}
	select {
	case <-e.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the engine did not stop")
	}
	if !txn.IsFatal(e.Err()) {
		t.Errorf("Err = %v, want the fatal storage error", e.Err())
	}
	if e.Phase() != engine.PhaseFailed {
		t.Errorf("phase %v, want failed", e.Phase())
	}
	_, err = e.Endpoints(context.Background())
	wantReason(t, err, engine.ReasonShuttingDown)
	_, err = e.RaiseAlert(context.Background(), "late", "x", "x")
	wantReason(t, err, engine.ReasonShuttingDown)
}

// A start and a stop, with and without work, leave no goroutine behind and move
// through the phases in order.
func TestTheEngineMovesThroughItsPhasesAndStopsEveryGoroutine(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e, _, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	if e.Phase() != engine.PhaseOpened {
		t.Errorf("phase %v after Open, want opened", e.Phase())
	}
	e.Start()
	if e.Phase() != engine.PhaseRecovering {
		t.Errorf("phase %v after Start, want recovering", e.Phase())
	}
	if err := e.RecoverDelivery(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Serve()
	if e.Phase() != engine.PhaseRunning {
		t.Errorf("phase %v after Serve, want running", e.Phase())
	}
	e.BeginShutdown()
	if e.Phase() != engine.PhaseStopping {
		t.Errorf("phase %v after BeginShutdown, want stopping", e.Phase())
	}
	_, err = e.Endpoints(context.Background())
	wantReason(t, err, engine.ReasonShuttingDown)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := e.Stop(ctx); err != nil {
		t.Errorf("a second Stop: %v", err)
	}
	select {
	case <-e.Done():
	default:
		t.Error("Done is not closed after Stop")
	}
	if e.Err() != nil {
		t.Errorf("Err = %v after an orderly stop", e.Err())
	}
	if e.Phase() != engine.PhaseStopped {
		t.Errorf("phase %v after Stop, want stopped", e.Phase())
	}
}

// Every command reads the clock once and judges it against the high water mark:
// a wall clock set back beyond the tolerance pauses what depends on deadlines
// until the owner clears it, and moving the clock on does not clear it.
func TestAWallClockSetBackPausesNewWorkUntilTheOwnerClearsIt(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	ctx := context.Background()
	if err := engine.AdmitNewWork(ctx, e, 1); err != nil {
		t.Fatalf("before the clock moved: %v", err)
	}
	r.clk.Advance(time.Hour)
	if err := engine.AdmitNewWork(ctx, e, 1); err != nil { // raises the mark to t0+1h
		t.Fatal(err)
	}
	r.clk.Set(t0.Add(time.Hour - 1*time.Minute)) // within the tolerance
	if err := engine.AdmitNewWork(ctx, e, 1); err != nil {
		t.Errorf("a clock one minute behind the mark paused the engine: %v", err)
	}
	r.clk.Set(t0.Add(30 * time.Minute)) // thirty minutes behind
	wantReason(t, engine.AdmitNewWork(ctx, e, 1), engine.ReasonClockRollback)
	r.clk.Set(t0.Add(3 * time.Hour))
	wantReason(t, engine.AdmitNewWork(ctx, e, 1), engine.ReasonClockRollback)
	if err := engine.ClearRollback(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := engine.AdmitNewWork(ctx, e, 1); err != nil {
		t.Errorf("after the owner cleared the pause: %v", err)
	}
}

func TestAPauseThatWasPersistedHoldsFromTheStart(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	r.paused = true
	e := r.start()
	wantReason(t, engine.AdmitNewWork(context.Background(), e, 1), engine.ReasonClockRollback)
}

// Work the engine owns is counted: it is not quiet while a worker runs.
func TestQuiesceWaitsForTheWorkersTheEngineOwns(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	block, started := make(chan struct{}), make(chan struct{})
	engine.SpawnWorker(e, "test", func(ctx context.Context) {
		close(started)
		select {
		case <-block:
		case <-ctx.Done():
		}
	})
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	rep, err := e.Quiesce(ctx, engine.Expect{})
	if err == nil || rep.Satisfied || rep.Workers != 1 {
		t.Errorf("Quiesce = %+v, %v; want it unsatisfied with one worker", rep, err)
	}
	close(block)
	settle(t, e)
}

// A worker the engine owns ends when the engine stops, whatever it waits for.
func TestStopEndsTheWorkersTheEngineOwns(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	started := make(chan struct{})
	engine.SpawnWorker(e, "test", func(ctx context.Context) {
		close(started)
		<-ctx.Done()
	})
	<-started
}

func failMatch(tx string, step fsx.Step) matchT { return matchT{Tx: tx, Step: step} }

var _ store.EndpointStatus
