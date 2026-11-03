package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/runtime/proc"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/turn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A wait for an end that is expected to come is long, so that how soon it comes
// decides nothing; a wait for one that must not come is short.
const (
	longWait  = 5 * time.Second
	shortWait = 50 * time.Millisecond
)

// runningTurn builds an app with a session whose first turn runs, and returns it
// with the app's interrupt and close waits long.
func runningTurn(t *testing.T) (*world, *App) {
	t.Helper()
	w := newWorld(t)
	app, _ := newApp(t, w)
	app.o.InterruptWait, app.o.CloseWait = longWait, longWait
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

// storedTurn is the turn of the session's last stored checkpoint.
func (w *world) storedTurn(t *testing.T) *turn.State {
	t.Helper()
	raw, ok := w.checkpoint("session/session-1")
	if !ok {
		t.Fatal("no checkpoint of the session")
	}
	var c sessionCheckpoint
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c.Turn
}

// waitFor polls until cond holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func closeSession(t *testing.T, app *App) adapterproto.CloseResult {
	t.Helper()
	res, err := app.CloseSession(ctxT(t), adapterproto.CloseParams{Endpoint: "session-1"})
	if err != nil {
		t.Fatalf("close: %v", err)
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
		wait        time.Duration // longWait unless set
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
			wait:  shortWait,
			want:  stage(adapterproto.InterruptRequested), wantSignals: 1,
		},
		{
			name: "the process cannot be looked at after the signal",
			setup: func(w *world) {
				w.onInterrupt = func(w *world, p *fakeProc) { w.setLookupErr(errors.New("no process table")) }
			},
			want: stage(adapterproto.InterruptRequested), wantSignals: 1,
		},
		{
			name: "the process ended before the signal reached it",
			setup: func(w *world) {
				w.interruptErr = fmt.Errorf("signal: %w", proc.ErrNotRunning)
			},
			want: stage(adapterproto.InterruptNotRunning), wantSignals: 1,
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
			if c.wait != 0 {
				app.o.InterruptWait = c.wait
			}
			if got := interrupt(t, app); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			if signals := w.signalled(); len(signals) != c.wantSignals {
				t.Fatalf("signals %v", signals)
			}
			if got := slices.Contains(w.eventList(), "ended "+prompt1+" interrupted"); got != c.ended {
				t.Fatalf("the end is reported with the answer: %v, want %v (%q)", got, c.ended, w.eventList())
			}
			if !c.ended && w.storedTurn(t).Interrupted && c.want.Stage != adapterproto.InterruptRequested {
				t.Fatal("an interrupt that signalled nothing left the turn marked")
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
// is then reported for what it was, and the next ask signals, because no signal
// was delivered.
func TestInterruptThatCannotSignalLeavesTheTurnAsItWas(t *testing.T) {
	w, app := runningTurn(t)
	w.interruptErr = errors.New("the helper failed")
	if got := interrupt(t, app); got.Stage != adapterproto.InterruptRefused {
		t.Fatalf("got %+v", got)
	}
	if w.storedTurn(t).Interrupted {
		t.Fatal("the refused interrupt left the stored turn marked")
	}
	w.locked(func() { w.interruptErr = nil })
	app.o.InterruptWait = shortWait
	if got := interrupt(t, app); got.Stage != adapterproto.InterruptRequested {
		t.Fatalf("second: %+v", got)
	}
	if signals := w.signalled(); len(signals) != 2 {
		t.Fatalf("the second ask did not signal: %v", signals)
	}
	w.exit(w.lastProc(), resultError)
	w.waitEvents("started "+prompt1, "ended "+prompt1+" interrupted")
}

// A turn whose process ended on its own, with the signal on its way, ends as it
// ended: the interrupt did not stop it.
func TestTurnThatEndedBeforeTheSignalIsReportedAsItEnded(t *testing.T) {
	w, app := runningTurn(t)
	w.interruptErr = fmt.Errorf("signal: %w", proc.ErrNotRunning)
	if got := interrupt(t, app); got.Stage != adapterproto.InterruptNotRunning {
		t.Fatalf("got %+v", got)
	}
	w.exit(w.lastProc(), resultOK)
	w.waitEvents("started "+prompt1, "ended "+prompt1+" completed")
}

// A process whose prompt is not in the session record yet has no turn: it is not
// signalled, as a signal would end it without a trace of the prompt.
func TestInterruptBeforeTheTurnStarted(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	openSession(t, w, app)
	w.onLaunch = nil // the process never writes the prompt
	res, err := app.DeliverPrompt(ctxT(t), adapterproto.PromptDeliverParams{Endpoint: "session-1", Attempt: attempt2, Text: "more"})
	if err != nil || res.Stage != adapterproto.PromptAccepted {
		t.Fatalf("deliver: %+v %v", res, err)
	}
	if got := interrupt(t, app); got.Stage != adapterproto.InterruptNotRunning {
		t.Fatalf("got %+v", got)
	}
	if signals := w.signalled(); len(signals) != 0 {
		t.Fatalf("signals %v", signals)
	}
	if w.storedTurn(t).Interrupted {
		t.Fatal("the turn is marked")
	}
}

// The mark is stored before the signal is sent, so that a restart between the two
// still reports the end of the turn as interrupted; the same holds for a close.
func TestTurnIsMarkedInTheCheckpointBeforeTheSignal(t *testing.T) {
	for name, stop := range map[string]func(*testing.T, *App){
		"interrupt": func(t *testing.T, app *App) { interrupt(t, app) },
		"close":     func(t *testing.T, app *App) { closeSession(t, app) },
	} {
		t.Run(name, func(t *testing.T) {
			w, app := runningTurn(t)
			marked := make(chan bool, 1)
			w.atSignal = func(w *world) { marked <- w.storedTurn(t).Interrupted }
			w.onInterrupt = func(w *world, p *fakeProc) { w.exit(p, resultError) }
			stop(t, app)
			if !<-marked {
				t.Fatal("the checkpoint did not hold the mark when the signal was sent")
			}
		})
	}
}

// A repeated request does not signal again; it waits for the end like the first.
func TestRepeatedInterruptDoesNotSignalAgain(t *testing.T) {
	w, app := runningTurn(t)
	app.o.InterruptWait = shortWait
	if got := interrupt(t, app); got.Stage != adapterproto.InterruptRequested {
		t.Fatalf("first: %+v", got)
	}
	p := w.lastProc()
	time.AfterFunc(10*time.Millisecond, func() { w.exit(p, resultError) })
	app.o.InterruptWait = longWait
	if got := interrupt(t, app); got.Stage != adapterproto.InterruptStopped {
		t.Fatalf("second: %+v", got)
	}
	if signals := w.signalled(); len(signals) != 1 {
		t.Fatalf("signals %v", signals)
	}
	w.waitEvents("started "+prompt1, "ended "+prompt1+" interrupted")
}

// A close that cannot signal does not count as a signal: the interrupt after it
// signals, and a turn that is not interrupted ends as it ended.
func TestInterruptAfterACloseThatCouldNotSignal(t *testing.T) {
	w, app := runningTurn(t)
	w.interruptErr = errors.New("the helper failed")
	if res := closeSession(t, app); res != unreachable {
		t.Fatalf("close: %+v", res)
	}
	if w.storedTurn(t).Interrupted {
		t.Fatal("the failed close left the turn marked")
	}
	w.locked(func() { w.interruptErr = nil })
	app.o.InterruptWait = shortWait
	if got := interrupt(t, app); got.Stage != adapterproto.InterruptRequested {
		t.Fatalf("interrupt: %+v", got)
	}
	if signals := w.signalled(); len(signals) != 2 {
		t.Fatalf("the interrupt did not signal: %v", signals)
	}
	w.exit(w.lastProc(), resultError)
	w.waitEvents("started "+prompt1, "ended "+prompt1+" interrupted")
}

// A close does not signal a process an interrupt already signalled.
func TestCloseAfterAnInterruptDoesNotSignalAgain(t *testing.T) {
	w, app := runningTurn(t)
	app.o.InterruptWait = shortWait
	if got := interrupt(t, app); got.Stage != adapterproto.InterruptRequested {
		t.Fatalf("interrupt: %+v", got)
	}
	p := w.lastProc()
	time.AfterFunc(10*time.Millisecond, func() { w.exit(p, resultError) })
	if res := closeSession(t, app); res != closed {
		t.Fatalf("close: %+v", res)
	}
	if signals := w.signalled(); len(signals) != 1 {
		t.Fatalf("signals %v", signals)
	}
	w.waitEvents("started "+prompt1, "ended "+prompt1+" interrupted")
}

// Interrupts of one session are served one at a time: while the first ask's signal
// is on its way, a second ask waits for it, and neither sees the other half done.
func TestConcurrentInterruptsAreServedOneAtATime(t *testing.T) {
	for _, c := range []struct {
		name    string
		fail    bool
		want    []adapterproto.InterruptStage // the answers of the first ask, the second one
		signals int
	}{
		{"the first signal is delivered", false, []adapterproto.InterruptStage{adapterproto.InterruptStopped, adapterproto.InterruptNotRunning}, 1},
		{"the first signal fails", true, []adapterproto.InterruptStage{adapterproto.InterruptRefused, adapterproto.InterruptRefused}, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, app := runningTurn(t)
			entered, release, second := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var n int
			w.atSignal = func(w *world) {
				w.mu.Lock()
				n++
				k := n
				w.mu.Unlock()
				switch k {
				case 1:
					close(entered)
					<-release
				case 2:
					close(second)
				}
			}
			if c.fail {
				w.interruptErr = errors.New("the helper failed")
			} else {
				w.onInterrupt = func(w *world, p *fakeProc) { w.exit(p, resultError) }
			}
			stages := make([]adapterproto.InterruptStage, 2)
			var wg sync.WaitGroup
			ask := func(i int) {
				defer wg.Done()
				res, err := app.Interrupt(ctxT(t), adapterproto.InterruptParams{Endpoint: "session-1"})
				if err != nil {
					t.Errorf("interrupt: %v", err)
				}
				stages[i] = res.Stage
			}
			wg.Add(2)
			go ask(0)
			<-entered
			go ask(1)
			// A second signal while the first is on its way is the failure; the
			// grace only bounds how long the test looks for it.
			select {
			case <-second:
				t.Error("the second ask signalled while the first one's signal was on its way")
			case <-time.After(shortWait):
			}
			close(release)
			wg.Wait()
			if !slices.Equal(stages, c.want) {
				t.Errorf("answers %v, want %v", stages, c.want)
			}
			if signals := w.signalled(); len(signals) != c.signals {
				t.Errorf("signals %v", signals)
			}
		})
	}
}
