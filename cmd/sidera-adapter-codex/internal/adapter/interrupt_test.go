package adapter_test

import (
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/adapter"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// terminalsFake is the part of the App Server that stops turns and lists and ends
// the processes of their commands. Stopping a turn leaves those processes alive, as
// the real server does.
type terminalsFake struct {
	mu        sync.Mutex
	terminals []appserver.Terminal
	// interruptErr is the answer to turn/interrupt when it is an error; endTurn says
	// whether an accepted interrupt ends the turn.
	interruptErr *appserver.RPCError
	endTurn      bool
	// terminateErr is the answer to the request that ends a process.
	terminateErr *appserver.RPCError
	// calls names the requests received, in order; endedBefore holds, for each
	// termination, how many turn.ended reports the daemon had by then.
	calls       []string
	endedBefore []int
}

func (f *terminalsFake) log(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *terminalsFake) logged() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func installTerminals(codex *fakeCodex, h *harness, f *terminalsFake) {
	codex.srv.Handle("turn/interrupt", func(raw json.RawMessage) (any, *appserver.RPCError) {
		p := decode[struct{ ThreadID, TurnID string }](raw)
		f.log("interrupt")
		if f.interruptErr != nil {
			return nil, f.interruptErr
		}
		if f.endTurn {
			go func() {
				time.Sleep(10 * time.Millisecond)
				codex.end(p.ThreadID, p.TurnID, "interrupted")
			}()
		}
		return map[string]any{}, nil
	})
	codex.srv.Handle("thread/backgroundTerminals/list", func(json.RawMessage) (any, *appserver.RPCError) {
		f.log("list")
		f.mu.Lock()
		defer f.mu.Unlock()
		return map[string]any{"data": f.terminals, "nextCursor": nil}, nil
	})
	codex.srv.Handle("thread/backgroundTerminals/terminate", func(raw json.RawMessage) (any, *appserver.RPCError) {
		p := decode[struct{ ProcessID string }](raw)
		f.log("terminate " + p.ProcessID)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.endedBefore = append(f.endedBefore, h.countNotes(adapterproto.MethodTurnEnded))
		if f.terminateErr != nil {
			return nil, f.terminateErr
		}
		return map[string]any{"terminated": true}, nil
	})
	codex.srv.Handle("thread/backgroundTerminals/clean", func(json.RawMessage) (any, *appserver.RPCError) {
		f.log("clean")
		return map[string]any{}, nil
	})
}

// commandStarted tells the adapter that the turn started a command with a process.
func commandStarted(codex *fakeCodex, turn adapterproto.TurnRef, item string) {
	codex.srv.Notify("item/started", map[string]any{"threadId": "th-1", "turnId": string(turn),
		"item": map[string]any{"type": "commandExecution", "id": item, "processId": "p-" + item}})
}

func (h *harness) interrupt() adapterproto.InterruptResult {
	h.t.Helper()
	var res adapterproto.InterruptResult
	h.mustCall(adapterproto.MethodSessionInterrupt, adapterproto.InterruptParams{Endpoint: sessionEP}, &res)
	return res
}

// An interrupt asks the server to stop the turn and answers stopped only when the
// turn is over and the processes of the commands it started are ended, which the
// server does not do itself. Other commands of the thread, started by earlier turns,
// are left running, and the end of the turn is not reported before its commands are
// ended.
func TestInterruptEndsTheCommandsOfTheTurn(t *testing.T) {
	tests := []struct {
		name     string
		commands []string // items of commands the turn started
		fake     *terminalsFake
		want     adapterproto.InterruptResult
		calls    []string
	}{
		{
			name:     "stopped with its commands ended",
			commands: []string{"c1", "c2"},
			fake: &terminalsFake{endTurn: true, terminals: []appserver.Terminal{
				{ItemID: "old", ProcessID: "p-old"}, {ItemID: "c1", ProcessID: "p-c1"}, {ItemID: "c2", ProcessID: "p-c2"}}},
			want:  adapterproto.InterruptResult{Stage: adapterproto.InterruptStopped},
			calls: []string{"interrupt", "list", "terminate p-c1", "terminate p-c2"},
		},
		{
			name: "stopped without any command to end",
			fake: &terminalsFake{endTurn: true, terminals: []appserver.Terminal{{ItemID: "old", ProcessID: "p-old"}}},
			want: adapterproto.InterruptResult{Stage: adapterproto.InterruptStopped}, calls: []string{"interrupt"},
		},
		{
			name:     "a command that could not be ended",
			commands: []string{"c1"},
			fake: &terminalsFake{endTurn: true, terminals: []appserver.Terminal{{ItemID: "c1", ProcessID: "p-c1"}},
				terminateErr: &appserver.RPCError{Code: -32603, Message: "internal error"}},
			want:  adapterproto.InterruptResult{Stage: adapterproto.InterruptRefused, Reason: adapterproto.InterruptNativeError},
			calls: []string{"interrupt", "list", "terminate p-c1"},
		},
		{
			name: "the server refuses",
			fake: &terminalsFake{interruptErr: &appserver.RPCError{Code: -32603, Message: "internal error"}},
			want: adapterproto.InterruptResult{Stage: adapterproto.InterruptRefused, Reason: adapterproto.InterruptNativeError}, calls: []string{"interrupt"},
		},
		{
			name: "the turn ended meanwhile",
			fake: &terminalsFake{interruptErr: &appserver.RPCError{Code: -32600, Message: "no active turn to interrupt"}},
			want: adapterproto.InterruptResult{Stage: adapterproto.InterruptNotRunning}, calls: []string{"interrupt"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			codex := newFakeCodex(t)
			h := newHarnessWith(t, codex, func(o *adapter.Options) { o.InterruptWait = 5 * time.Second })
			h.start()
			h.create(attempt1, sessionEP, "dev")
			started := decode[adapterproto.TurnStartedParams](h.waitNote(adapterproto.MethodTurnStarted, nil))
			f := tc.fake
			installTerminals(codex, h, f)
			for _, c := range tc.commands {
				commandStarted(codex, started.Turn, c)
			}

			if got := h.interrupt(); got != tc.want {
				t.Fatalf("interrupt = %+v, want %+v", got, tc.want)
			}
			if got := f.logged(); !slices.Equal(got, tc.calls) {
				t.Errorf("requests = %v, want %v", got, tc.calls)
			}
			for _, n := range f.endedBefore {
				if n != 0 {
					t.Errorf("the end of the turn was reported before its commands were ended")
				}
			}
			if f.endTurn {
				h.waitNote(adapterproto.MethodTurnEnded, func(r json.RawMessage) bool { return ended(r, adapterproto.TurnInterrupted) })
			}
		})
	}
}

// An interrupt whose turn does not end in time is answered requested, and the
// commands are still ended when the turn does end.
func TestInterruptRequestedEndsTheCommandsWhenTheTurnEnds(t *testing.T) {
	codex := newFakeCodex(t)
	h := newHarnessWith(t, codex, func(o *adapter.Options) { o.InterruptWait = 50 * time.Millisecond })
	h.start()
	h.create(attempt1, sessionEP, "dev")
	started := decode[adapterproto.TurnStartedParams](h.waitNote(adapterproto.MethodTurnStarted, nil))
	f := &terminalsFake{terminals: []appserver.Terminal{{ItemID: "c1", ProcessID: "p-c1"}}}
	installTerminals(codex, h, f)
	commandStarted(codex, started.Turn, "c1")

	if got := h.interrupt(); got.Stage != adapterproto.InterruptRequested {
		t.Fatalf("interrupt = %+v", got)
	}
	// Asked again while the turn runs, the server is not asked again.
	if got := h.interrupt(); got.Stage != adapterproto.InterruptRequested {
		t.Fatalf("second interrupt = %+v", got)
	}
	codex.end("th-1", string(started.Turn), "interrupted")
	h.waitNote(adapterproto.MethodTurnEnded, func(r json.RawMessage) bool { return ended(r, adapterproto.TurnInterrupted) })
	if got, want := f.logged(), []string{"interrupt", "list", "terminate p-c1"}; !slices.Equal(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
}

// A turn the adapter found running rather than saw starting has commands it was not
// told of, and none can be told from the thread's other commands: all of them end.
func TestInterruptOfAFoundTurnEndsEveryCommandOfTheThread(t *testing.T) {
	codex := newFakeCodex(t)
	codex.seed("th-1", &fakeTurn{id: "tu-1", status: "inProgress", clientID: attempt1})
	h := newHarnessWith(t, codex, func(o *adapter.Options) { o.InterruptWait = 5 * time.Second })
	access := adapterproto.ToolAccess{Env: map[string]string{"SIDERA_TOKEN": token, "SIDERA_HOME": dataDir}}
	h.start(adapterproto.SessionAttach{Endpoint: sessionEP, Preset: "dev", NativeRef: "th-1", ToolAccess: &access})
	f := &terminalsFake{endTurn: true}
	installTerminals(codex, h, f)

	if got := h.interrupt(); got.Stage != adapterproto.InterruptStopped {
		t.Fatalf("interrupt = %+v", got)
	}
	if got, want := f.logged(), []string{"interrupt", "clean"}; !slices.Equal(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
}

// With no turn running the server is not asked, and a session the adapter does not
// hold is unknown.
func TestInterruptWithoutATurn(t *testing.T) {
	codex := newFakeCodex(t)
	h := newHarness(t, codex)
	h.idleSession(codex)
	f := &terminalsFake{}
	installTerminals(codex, h, f)

	if got := h.interrupt(); got.Stage != adapterproto.InterruptNotRunning {
		t.Fatalf("interrupt = %+v", got)
	}
	if got := f.logged(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
	var res adapterproto.InterruptResult
	if code := codeOf(t, h.call(adapterproto.MethodSessionInterrupt, adapterproto.InterruptParams{Endpoint: otherEP}, &res)); code != adapterproto.CodeUnknownEndpoint {
		t.Fatalf("unknown endpoint: code %d", code)
	}
}
