package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// What the adapter does with the App Server when it is told to shut down depends
// on why. A restart keeps the server, so that a turn goes on while the adapter is
// replaced. A stop ends it only when no session uses it. A retire, the instance
// being disabled or removed, ends it whatever it is doing and deletes the working
// directory it ran in, and only after the server is gone.
func TestShutdownEndsTheServerByIntent(t *testing.T) {
	const terminate, removeDir = "terminate", "remove-workdir"
	for _, tc := range []struct {
		intent adapterproto.ShutdownIntent
		busy   bool
		want   []string // what was done to the server and its directory
	}{
		{adapterproto.ShutdownRestart, false, nil},
		{adapterproto.ShutdownRestart, true, nil},
		{adapterproto.ShutdownStop, false, []string{terminate}},
		{adapterproto.ShutdownStop, true, nil},
		{adapterproto.ShutdownRetire, false, []string{terminate, removeDir}},
		{adapterproto.ShutdownRetire, true, []string{terminate, removeDir}},
	} {
		name := string(tc.intent) + "/idle"
		if tc.busy {
			name = string(tc.intent) + "/running a turn"
		}
		t.Run(name, func(t *testing.T) {
			codex := newFakeCodex(t)
			h := newHarness(t, codex)
			h.start()
			h.create(attempt1, sessionEP, "dev")
			started := decode[adapterproto.TurnStartedParams](h.waitNote(adapterproto.MethodTurnStarted, nil))
			if !tc.busy {
				codex.end("th-1", string(started.Turn), "completed")
				h.waitNote(adapterproto.MethodTurnEnded, nil)
			}

			h.mustCall(adapterproto.MethodShutdown, adapterproto.ShutdownParams{Intent: tc.intent}, &adapterproto.Empty{})
			if got := h.mgr.history(); !slices.Equal(got, tc.want) {
				t.Fatalf("after shutdown %s the server saw %v, want %v", tc.intent, got, tc.want)
			}
		})
	}
}

// A shutdown that does not say why is the one of a restart, which the daemon sends to
// an adapter it replaces.
func TestShutdownWithoutIntentKeepsTheServer(t *testing.T) {
	h := newHarness(t, newFakeCodex(t))
	h.start()
	h.mustCall(adapterproto.MethodShutdown, adapterproto.Empty{}, &adapterproto.Empty{})
	if got := h.mgr.history(); len(got) != 0 {
		t.Fatalf("the server saw %v, want nothing", got)
	}
}

// An instance that is disabled while its adapter is not running is released by an
// adapter that was initialized and never started: it knows only its state
// directory, and the server it left is found by the state file there.
func TestRetireReleasesTheServerOfAnAdapterThatWasNotStarted(t *testing.T) {
	h := newHarness(t, newFakeCodex(t))
	h.mustCall(adapterproto.MethodShutdown, adapterproto.ShutdownParams{Intent: adapterproto.ShutdownRetire}, &adapterproto.Empty{})
	if got, want := h.mgr.history(), []string{"terminate", "remove-workdir"}; !slices.Equal(got, want) {
		t.Fatalf("the server saw %v, want %v", got, want)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.serverCfgs) != 1 || h.serverCfgs[0].StateDir != h.dir {
		t.Fatalf("the server was looked for with %+v, want the state directory %s", h.serverCfgs, h.dir)
	}
}

// A server that cannot be ended is not hidden: the answer to shutdown is the error,
// which the daemon logs, and its directory stays because it still runs there.
func TestRetireKeepsTheDirectoryOfAServerThatCannotBeEnded(t *testing.T) {
	h := newHarness(t, newFakeCodex(t))
	h.start()
	h.mgr.terminate = func() error { return errors.New("the process table did not answer") }
	err := h.call(adapterproto.MethodShutdown, adapterproto.ShutdownParams{Intent: adapterproto.ShutdownRetire}, &adapterproto.Empty{})
	if err == nil || !strings.Contains(err.Error(), "left running") || !strings.Contains(err.Error(), "process table") {
		t.Fatalf("shutdown answered %v, want the reason the server was left", err)
	}
	if got, want := h.mgr.history(), []string{"terminate"}; !slices.Equal(got, want) {
		t.Fatalf("the server saw %v, want %v", got, want)
	}
}

// A request that is starting the App Server when the adapter is told to shut down
// does not hold the shutdown to its own deadline: the start ends with the adapter,
// so that the answer comes while the daemon still waits for it, and the server
// started is ended behind it.
func TestShutdownDoesNotWaitForARequestStartingTheServer(t *testing.T) {
	h := newHarness(t, newFakeCodex(t))
	h.start()
	// The connection the start opened is closed and the adapter cannot open another
	// by itself, so the request has to start the server. The request is sent once the
	// adapter has seen the connection end: before that it would use the closed
	// connection, fail on it and never try to connect.
	held := h.mgr.refuseConnectsUnlessRequested()
	h.codex.srv.DropConnections()
	h.waitNote(adapterproto.MethodAdapterAvailability, func(r json.RawMessage) bool {
		c := decode[adapterproto.AvailabilityParams](r).Conditions
		return len(c) == 1 && c[0].Condition == adapterproto.ConditionNativeUnavailable
	})

	type created struct {
		res adapterproto.SessionCreateResult
		err error
	}
	answered := make(chan created, 1)
	go func() {
		var c created
		c.err = h.daemon.Call(context.Background(), adapterproto.MethodSessionCreate, createParams(attempt1, sessionEP, "dev"), &c.res)
		answered <- c
	}()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the request never began to connect")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.daemon.Call(ctx, adapterproto.MethodShutdown, adapterproto.ShutdownParams{Intent: adapterproto.ShutdownRetire}, &adapterproto.Empty{}); err != nil {
		t.Fatalf("shutdown: %v, want it answered while a request was starting the server", err)
	}
	if got, want := h.mgr.history(), []string{"terminate", "remove-workdir"}; !slices.Equal(got, want) {
		t.Fatalf("the server saw %v, want %v", got, want)
	}
	select {
	case c := <-answered:
		if c.err != nil || c.res.Reason != adapterproto.CreateFailNativeUnavailable {
			t.Fatalf("the request that was starting the server was answered %+v, %v, want native_unavailable", c.res, c.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request starting the server is still running")
	}
}
