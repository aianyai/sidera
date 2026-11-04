package runtime

import (
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A turn process outlives the adapter, so that a turn goes on while the daemon or
// the adapter restarts; only an instance that is disabled or removed has nobody to
// take it over, and its running turns are interrupted, the way Ctrl-C does, by the
// identity the adapter recorded. A process that is not the one recorded (its pid
// went to another process) is never signalled.
func TestShutdownInterruptsTheRunningTurnsOfARetiredInstance(t *testing.T) {
	for _, tc := range []struct {
		name    string
		intent  adapterproto.ShutdownIntent
		setup   func(w *world)
		wait    time.Duration // the wait for the turns to end, longWait unless set
		signals int
		wantErr bool
	}{
		{name: "retire", intent: adapterproto.ShutdownRetire, signals: 1,
			setup: func(w *world) { w.onInterrupt = func(w *world, p *fakeProc) { w.exit(p, resultError) } }},
		{name: "restart", intent: adapterproto.ShutdownRestart},
		{name: "stop", intent: adapterproto.ShutdownStop},
		{name: "retire, the pid now belongs to another process", intent: adapterproto.ShutdownRetire,
			setup: func(w *world) {
				// The pid is now another program, started later: not a turn of the session.
				p := w.lastProc()
				w.locked(func() { p.startUnix++; p.args = []string{"/usr/bin/vim"} })
			}},
		{name: "retire, the process ended already", intent: adapterproto.ShutdownRetire,
			setup: func(w *world) { w.setAlive(w.lastProc().pid, false) }},
		{name: "retire, the signal cannot be sent", intent: adapterproto.ShutdownRetire, signals: 1, wantErr: true,
			setup: func(w *world) { w.interruptErr = errors.New("the helper failed") }},
		{name: "retire, the process does not end", intent: adapterproto.ShutdownRetire, signals: 1, wantErr: true, wait: shortWait},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, app := runningTurn(t)
			if tc.setup != nil {
				tc.setup(w)
			}
			if tc.wait != 0 {
				app.o.InterruptWait = tc.wait
			}

			err := app.Shutdown(ctxT(t), adapterproto.ShutdownParams{Intent: tc.intent})
			if (err != nil) != tc.wantErr {
				t.Fatalf("Shutdown: %v, want an error: %v", err, tc.wantErr)
			}
			if got := w.signalled(); len(got) != tc.signals {
				t.Fatalf("signals %v, want %d", got, tc.signals)
			}
		})
	}
}

// A retired instance has no endpoint left that a person could have a session on, but an
// idle session is no reason to signal anything either.
func TestShutdownOfARetiredInstanceSignalsNothingWhenNoTurnRuns(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	openSession(t, w, app)
	if err := app.Shutdown(ctxT(t), adapterproto.ShutdownParams{Intent: adapterproto.ShutdownRetire}); err != nil {
		t.Fatal(err)
	}
	if got := w.signalled(); len(got) != 0 {
		t.Fatalf("signals %v", got)
	}
}
