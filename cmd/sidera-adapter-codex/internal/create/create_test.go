package create

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestDecide(t *testing.T) {
	p := Params{StartTimeout: time.Minute, MaxStarts: 2}
	ready := Observed{Checkpointed: true, Named: true, MCP: MCPReady}
	with := func(o Observed, f func(*Observed)) Observed { f(&o); return o }
	thread := State{ThreadID: "t"}

	for _, tc := range []struct {
		name    string
		st      State
		obs     Observed
		want    Action
		failure adapterproto.CreateFailure
	}{
		{"nothing yet", State{}, Observed{}, StartThread, ""},
		{"thread not stored", thread, Observed{}, PutCheckpoint, ""},
		{"stored but not named", thread, Observed{Checkpointed: true}, NameThread, ""},
		{"named, channel not up", thread, Observed{Checkpointed: true, Named: true}, AwaitMCP, ""},
		{"channel failed to start", thread, with(ready, func(o *Observed) { o.MCP = MCPFailed }), Fail, adapterproto.CreateFailDependencyMissing},
		{"ready to start the turn", thread, ready, StartTurn, ""},
		{"turn running, within the time", thread, with(ready, func(o *Observed) { o.TurnRunning, o.TurnsStarted, o.Waited = true, 1, time.Second }), AwaitPrompt, ""},
		{"turn running at the limit", thread, with(ready, func(o *Observed) { o.TurnRunning, o.TurnsStarted, o.Waited = true, 1, time.Minute }), AwaitPrompt, ""},
		{"turn running past the limit", thread, with(ready, func(o *Observed) { o.TurnRunning, o.TurnsStarted, o.Waited = true, 1, time.Minute+1 }), Indeterminate, ""},
		{"turn ended without the prompt, one more start", thread, with(ready, func(o *Observed) { o.TurnsStarted = 1 }), StartTurn, ""},
		{"turn ended without the prompt, starts used", thread, with(ready, func(o *Observed) { o.TurnsStarted = 2 }), Fail, adapterproto.CreateFailNativeError},
		{"prompt recorded", thread, with(ready, func(o *Observed) { o.PromptRecorded, o.TurnRunning = true, true }), Ready, ""},
		{"prompt recorded after a restart", thread, Observed{Checkpointed: true, PromptRecorded: true}, Ready, ""},
		{"stored failure is final", State{ThreadID: "t", Failed: adapterproto.CreateFailNativeError}, with(ready, func(o *Observed) { o.PromptRecorded = true }), Fail, adapterproto.CreateFailNativeError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.st, tc.obs, p)
			if got.Action != tc.want || got.Failure != tc.failure {
				t.Fatalf("Decide = %+v, want %s %q", got, tc.want, tc.failure)
			}
		})
	}
}

func TestCheckpointRoundTrip(t *testing.T) {
	for _, st := range []State{{}, {ThreadID: "t1"}, {ThreadID: "t1", Failed: adapterproto.CreateFailNativeError}} {
		got, err := ParseCheckpoint(st.Checkpoint())
		if err != nil || got != st {
			t.Errorf("round trip of %+v = %+v, %v", st, got, err)
		}
	}
	if got := string(State{ThreadID: "t1"}.Checkpoint()); got != `{"v":1,"thread_id":"t1"}` {
		t.Errorf("checkpoint = %s", got)
	}
	if st, err := ParseCheckpoint(nil); err != nil || st != (State{}) {
		t.Errorf("empty checkpoint = %+v, %v", st, err)
	}
	for _, bad := range []string{`{"v":2,"thread_id":"t"}`, `[]`, `{"thread_id":"t"}`} {
		if _, err := ParseCheckpoint([]byte(bad)); err == nil {
			t.Errorf("checkpoint %s was accepted", bad)
		}
	}
}
