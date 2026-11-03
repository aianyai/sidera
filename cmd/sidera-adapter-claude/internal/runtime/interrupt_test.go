package runtime

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// runningTurn builds an app with a session whose first turn runs, and returns it
// with the app's interrupt wait set short.
func runningTurn(t *testing.T) (*world, *App) {
	t.Helper()
	w := newWorld(t)
	app, _ := newApp(t, w)
	app.o.InterruptWait = 50 * time.Millisecond
	w.onLaunch = claudeWrites(time.Millisecond)
	if _, err := app.CreateSession(ctxT(t), createParams(attempt1, prompt1)); err != nil {
		t.Fatal(err)
	}
	w.waitEvents("started " + prompt1)
	return w, app
}

func interrupt(t *testing.T, app *App) adapterproto.InterruptResult {
	t.Helper()
	res, err := app.Interrupt(ctxT(t), adapterproto.InterruptParams{Endpoint: "session-1"})
	if err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	return res
}

func TestInterrupt(t *testing.T) {
	stage := func(s adapterproto.InterruptStage) adapterproto.InterruptResult {
		return adapterproto.InterruptResult{Stage: s}
	}
	for _, c := range []struct {
		name string
		// setup shapes the world after the session's turn started.
		setup       func(w *world)
		want        adapterproto.InterruptResult
		wantSignals int
		// ended is whether the end of the turn was reported, as interrupted, by the
		// time the answer came.
		ended bool
	}{
		{
			name: "the turn ends within the wait",
			setup: func(w *world) {
				w.onInterrupt = func(w *world, p *fakeProc) { w.exit(p, resultError) }
			},
			want: stage(adapterproto.InterruptStopped), wantSignals: 1, ended: true,
		},
		{
			name:  "the turn does not end within the wait",
			setup: func(w *world) {},
			want:  stage(adapterproto.InterruptRequested), wantSignals: 1,
		},
		{
			name: "the process ended before the monitor saw it",
			setup: func(w *world) {
				w.setAlive(w.lastProc().pid, false)
			},
			want: stage(adapterproto.InterruptNotRunning),
		},
		{
			name: "the process cannot be looked at",
			setup: func(w *world) {
				w.setLookupErr(errors.New("no process table"))
			},
			want: adapterproto.InterruptResult{Stage: adapterproto.InterruptRefused, Reason: adapterproto.InterruptNativeUnreachable},
		},
		{
			name: "the signal cannot be sent",
			setup: func(w *world) {
				w.interruptErr = errors.New("the helper failed")
			},
			want: adapterproto.InterruptResult{Stage: adapterproto.InterruptRefused, Reason: adapterproto.InterruptNativeError}, wantSignals: 1,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, app := runningTurn(t)
			c.setup(w)
			if got := interrupt(t, app); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			if signals := w.signalled(); len(signals) != c.wantSignals {
				t.Fatalf("signals %v", signals)
			}
			if got := slices.Contains(w.eventList(), "ended "+prompt1+" interrupted"); got != c.ended {
				t.Fatalf("the end is reported with the answer: %v, want %v (%q)", got, c.ended, w.eventList())
			}
		})
	}
}

func TestInterruptOfASessionWithoutARunningTurn(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	if _, err := app.Interrupt(ctxT(t), adapterproto.InterruptParams{Endpoint: "session-1"}); !isCode(err, adapterproto.CodeUnknownEndpoint) {
		t.Fatalf("an endpoint the adapter does not hold is unknown: %v", err)
	}
	openSession(t, w, app)
	if got := interrupt(t, app); got.Stage != adapterproto.InterruptNotRunning {
		t.Fatalf("an idle session: %+v", got)
	}
	if signals := w.signalled(); len(signals) != 0 {
		t.Fatalf("signals %v", signals)
	}
}

// A signal that was not sent must not leave the turn marked interrupted: its end
// is then reported for what it was.
func TestInterruptThatCannotSignalLeavesTheTurnAsItWas(t *testing.T) {
	w, app := runningTurn(t)
	w.interruptErr = errors.New("the helper failed")
	if got := interrupt(t, app); got.Stage != adapterproto.InterruptRefused {
		t.Fatalf("got %+v", got)
	}
	w.exit(w.lastProc(), resultOK)
	w.waitEvents("started "+prompt1, "ended "+prompt1+" completed")
}

// A repeated request does not signal again; it waits for the end like the first.
func TestRepeatedInterruptDoesNotSignalAgain(t *testing.T) {
	w, app := runningTurn(t)
	if got := interrupt(t, app); got.Stage != adapterproto.InterruptRequested {
		t.Fatalf("first: %+v", got)
	}
	p := w.lastProc()
	time.AfterFunc(10*time.Millisecond, func() { w.exit(p, resultError) })
	app.o.InterruptWait = 5 * time.Second
	if got := interrupt(t, app); got.Stage != adapterproto.InterruptStopped {
		t.Fatalf("second: %+v", got)
	}
	if signals := w.signalled(); len(signals) != 1 {
		t.Fatalf("signals %v", signals)
	}
	w.waitEvents("started "+prompt1, "ended "+prompt1+" interrupted")
}

// Interrupts of one session are served one at a time, so asks that arrive together
// make one request and all see the turn stopped.
func TestConcurrentInterruptsAreServedOneAtATime(t *testing.T) {
	w, app := runningTurn(t)
	app.o.InterruptWait = 5 * time.Second
	w.onInterrupt = func(w *world, p *fakeProc) {
		time.Sleep(20 * time.Millisecond)
		w.exit(p, resultError)
	}
	var wg sync.WaitGroup
	stages := make([]adapterproto.InterruptStage, 4)
	for i := range stages {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := app.Interrupt(ctxT(t), adapterproto.InterruptParams{Endpoint: "session-1"})
			if err != nil {
				t.Errorf("interrupt: %v", err)
			}
			stages[i] = res.Stage
		}()
	}
	wg.Wait()
	// The first ask stops the turn; the others find it over.
	for i, s := range stages {
		if s != adapterproto.InterruptStopped && s != adapterproto.InterruptNotRunning {
			t.Errorf("ask %d: %s", i, s)
		}
	}
	if signals := w.signalled(); len(signals) != 1 {
		t.Fatalf("signals %v", signals)
	}
}
