package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/adapter"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver/appservertest"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/declare"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/server"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

const (
	runtimeEP = "11111111111111111111111111111111"
	sessionEP = "22222222222222222222222222222222"
	otherEP   = "33333333333333333333333333333333"
	attempt1  = "6f1c2a4e-8b3d-4c5a-9e7f-0a1b2c3d4e5f"
	attempt2  = "7a2d3b5f-9c4e-4d6b-8f80-1b2c3d4e5f60"
	attempt3  = "8b3e4c60-ad5f-4e7c-9091-2c3d4e5f6071"
	token     = "k3JpQ9vL2mXwR7tYbN5cZ8dHfG1sAeUo0iWxM4yTqBA"
)

// fakeCodex is a fake App Server with the threads and turns the adapter reads
// and writes.
type fakeCodex struct {
	t   *testing.T
	srv *appservertest.Server

	mu       sync.Mutex
	threads  map[string]*fakeThread
	next     int
	signedIn bool
	// lateItem sends the event of the recorded prompt after the answer to the turn
	// start instead of before it.
	lateItem bool
	// slowStart answers the turn start first and, a moment later, records the
	// turn and its prompt and sends every event of the turn, as a server does while the thread's
	// MCP servers are still starting.
	slowStart bool
	// otherRecordForm makes the record (thread/turns/list) omit what a prompt is
	// found by, as a server that changed the form of its record would.
	otherRecordForm bool
	// resumeFailures makes that many thread/resume calls fail with an error that
	// says nothing of the thread, as a server that is not ready does.
	resumeFailures int
	// turnStartGone makes that many turn/start calls answer that the thread was not
	// found, as the server does for a thread it has unloaded.
	turnStartGone int
}

type fakeThread struct {
	id    string
	named string
	turns []*fakeTurn // oldest first
}

type fakeTurn struct {
	id       string
	status   string
	clientID string
	// unrecorded hides the turn from the record (thread/turns/list), which does not
	// hold it until its prompt is written.
	unrecorded bool
}

func newFakeCodex(t *testing.T) *fakeCodex {
	f := &fakeCodex{t: t, srv: appservertest.NewUnix(t), threads: map[string]*fakeThread{}, signedIn: true}
	f.install()
	return f
}

func decode[T any](raw json.RawMessage) T {
	var v T
	_ = json.Unmarshal(raw, &v)
	return v
}

func (f *fakeCodex) install() {
	f.srv.Handle("initialize", func(json.RawMessage) (any, *appserver.RPCError) {
		return map[string]any{"userAgent": "sidera-adapter-codex/0.164.0 (test)", "codexHome": "/h"}, nil
	})
	f.srv.Handle("account/read", func(json.RawMessage) (any, *appserver.RPCError) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.signedIn {
			return map[string]any{"account": nil, "requiresOpenaiAuth": true}, nil
		}
		return map[string]any{"account": map[string]any{"type": "chatgpt"}, "requiresOpenaiAuth": true}, nil
	})
	f.srv.Handle("thread/start", func(json.RawMessage) (any, *appserver.RPCError) {
		f.mu.Lock()
		f.next++
		th := &fakeThread{id: fmt.Sprintf("th-%d", f.next)}
		f.threads[th.id] = th
		f.mu.Unlock()
		// The channel's MCP server reports before the answer is read.
		f.srv.Notify("mcpServer/startupStatus/updated", map[string]any{"threadId": th.id, "name": "sidera", "status": "ready"})
		return map[string]any{"thread": map[string]any{"id": th.id, "status": map[string]string{"type": "idle"}}}, nil
	})
	f.srv.Handle("thread/resume", func(raw json.RawMessage) (any, *appserver.RPCError) {
		p := decode[struct{ ThreadID string }](raw)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.resumeFailures > 0 {
			f.resumeFailures--
			return nil, &appserver.RPCError{Code: -32603, Message: "internal error"}
		}
		th := f.threads[p.ThreadID]
		if th == nil {
			return nil, &appserver.RPCError{Code: -32600, Message: "no rollout found for thread id " + p.ThreadID}
		}
		status := "idle"
		if n := len(th.turns); n > 0 && th.turns[n-1].status == "inProgress" {
			status = "active"
		}
		return map[string]any{"thread": map[string]any{"id": th.id, "status": map[string]string{"type": status}}}, nil
	})
	f.srv.Handle("thread/name/set", func(raw json.RawMessage) (any, *appserver.RPCError) {
		p := decode[struct{ ThreadID, Name string }](raw)
		f.mu.Lock()
		defer f.mu.Unlock()
		if th := f.threads[p.ThreadID]; th != nil {
			th.named = p.Name
		}
		return map[string]any{}, nil
	})
	f.srv.Handle("thread/turns/list", func(raw json.RawMessage) (any, *appserver.RPCError) {
		p := decode[struct{ ThreadID string }](raw)
		f.mu.Lock()
		defer f.mu.Unlock()
		th := f.threads[p.ThreadID]
		if th == nil {
			return nil, &appserver.RPCError{Code: -32600, Message: "no rollout found for thread id " + p.ThreadID}
		}
		data := []any{}
		for i := len(th.turns) - 1; i >= 0; i-- { // newest first
			t := th.turns[i]
			if t.unrecorded {
				continue
			}
			item := map[string]any{"type": "userMessage", "id": "i-" + t.id, "clientId": t.clientID}
			if f.otherRecordForm {
				delete(item, "clientId")
			}
			data = append(data, map[string]any{"id": t.id, "status": t.status, "itemsView": "full", "items": []any{item}})
		}
		return map[string]any{"data": data}, nil
	})
	f.srv.Handle("turn/start", func(raw json.RawMessage) (any, *appserver.RPCError) {
		p := decode[struct {
			ThreadID            string
			ClientUserMessageID string
		}](raw)
		f.mu.Lock()
		th := f.threads[p.ThreadID]
		if th == nil || f.turnStartGone > 0 {
			if th != nil {
				f.turnStartGone--
			}
			f.mu.Unlock()
			return nil, &appserver.RPCError{Code: -32600, Message: "thread not found: " + p.ThreadID}
		}
		f.next++
		turn := &fakeTurn{id: fmt.Sprintf("tu-%d", f.next), status: "inProgress", clientID: p.ClientUserMessageID}
		th.turns = append(th.turns, turn)
		late, slow := f.lateItem, f.slowStart
		turn.unrecorded = slow
		f.mu.Unlock()
		answer := map[string]any{"turn": map[string]any{"id": turn.id, "status": "inProgress", "items": []any{}}}
		started := func() {
			f.srv.Notify("thread/status/changed", map[string]any{"threadId": th.id, "status": map[string]string{"type": "active"}})
			f.srv.Notify("turn/started", map[string]any{"threadId": th.id, "turn": map[string]any{"id": turn.id, "status": "inProgress", "items": []any{}}})
		}
		item := func() {
			f.srv.Notify("item/completed", map[string]any{"threadId": th.id, "turnId": turn.id,
				"item": map[string]any{"type": "userMessage", "id": "i-" + turn.id, "clientId": p.ClientUserMessageID}})
		}
		if slow {
			go func() {
				time.Sleep(300 * time.Millisecond)
				f.mu.Lock()
				turn.unrecorded = false
				f.mu.Unlock()
				started()
				item()
			}()
			return answer, nil
		}
		// The events come before the answer, which the order is not promised to avoid.
		started()
		if late {
			go func() { time.Sleep(100 * time.Millisecond); item() }()
		} else {
			item()
		}
		return answer, nil
	})
	for _, m := range []string{"thread/archive", "thread/unsubscribe"} {
		f.srv.Handle(m, func(raw json.RawMessage) (any, *appserver.RPCError) {
			p := decode[struct{ ThreadID string }](raw)
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.threads[p.ThreadID] == nil {
				return nil, &appserver.RPCError{Code: -32600, Message: "thread not found: " + p.ThreadID}
			}
			return map[string]any{}, nil
		})
	}
}

// end ends a turn as the server does: the record changes, then the events.
func (f *fakeCodex) end(threadID, turnID, status string) {
	f.mu.Lock()
	for _, t := range f.threads[threadID].turns {
		if t.id == turnID {
			t.status = status
		}
	}
	f.mu.Unlock()
	f.srv.Notify("turn/completed", map[string]any{"threadId": threadID, "turn": map[string]any{"id": turnID, "status": status, "items": []any{}}})
	f.srv.Notify("thread/status/changed", map[string]any{"threadId": threadID, "status": map[string]string{"type": "idle"}})
}

// seed adds a thread that exists in the server's record, as one left by an earlier
// run does, with the turns given (oldest first).
func (f *fakeCodex) seed(id string, turns ...*fakeTurn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.threads[id] = &fakeThread{id: id, named: "seeded", turns: turns}
}

// endQuietly changes the record of a turn without any event, as happens while the
// adapter is not connected.
func (f *fakeCodex) endQuietly(threadID, turnID, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.threads[threadID].turns {
		if t.id == turnID {
			t.status = status
		}
	}
}

func (f *fakeCodex) callsOf(method string) []appservertest.Call {
	var out []appservertest.Call
	for _, c := range f.srv.Calls() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

type fakeManager struct {
	ep  appserver.Endpoint
	err error
}

func (m *fakeManager) Connect(context.Context) (appserver.Endpoint, error) { return m.ep, m.err }

// harness is the daemon's end of a served adapter, with a fake App Server behind
// the adapter.
type harness struct {
	t      *testing.T
	daemon *peer.Peer
	codex  *fakeCodex
	mgr    *fakeManager
	dir    string

	mu          sync.Mutex
	notes       []note
	checkpoints []adapterproto.CheckpointPutParams
	changed     chan struct{}
}

type note struct {
	method string
	raw    json.RawMessage
}

func newHarness(t *testing.T, codex *fakeCodex) *harness { return newHarnessWith(t, codex, nil) }

// newHarnessWith lets a caller replace what the harness gives the adapter.
func newHarnessWith(t *testing.T, codex *fakeCodex, mutate func(*adapter.Options)) *harness {
	t.Helper()
	dir, err := os.MkdirTemp("", "h")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	h := &harness{t: t, codex: codex, dir: dir, mgr: &fakeManager{}, changed: make(chan struct{}, 1)}
	if codex != nil {
		h.mgr.ep = codex.srv.Endpoint
	}
	opts := adapter.Options{
		NewServer:    func(server.Config) adapter.ServerManager { return h.mgr },
		CodexVersion: func(context.Context, string) (string, error) { return "0.164.0", nil },
		LookPath:     func(name string) (string, error) { return filepath.Join(dir, "bin", name), nil },
		MCPWait:      300 * time.Millisecond,
		PollEvery:    30 * time.Millisecond,
		ReconnectMin: 20 * time.Millisecond, ReconnectMax: 100 * time.Millisecond,
	}
	if mutate != nil {
		mutate(&opts)
	}
	app := adapter.New(opts)
	aR, dW := io.Pipe()
	dR, aW := io.Pipe()
	d, err := peer.New(dR, dW, peer.Config{})
	if err != nil {
		t.Fatal(err)
	}
	d.Handle(adapterproto.MethodCheckpointPut, func(_ context.Context, raw json.RawMessage) (any, error) {
		var p adapterproto.CheckpointPutParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		h.mu.Lock()
		h.checkpoints = append(h.checkpoints, p)
		h.mu.Unlock()
		return adapterproto.Empty{}, nil
	})
	for _, m := range []string{
		adapterproto.MethodAdapterAvailability, adapterproto.MethodTurnStarted, adapterproto.MethodTurnEnded,
		adapterproto.MethodPromptPersisted, adapterproto.MethodNativeRequestHandled,
	} {
		d.HandleNotification(m, func(_ context.Context, raw json.RawMessage) {
			h.mu.Lock()
			h.notes = append(h.notes, note{method: m, raw: raw})
			h.mu.Unlock()
			select {
			case h.changed <- struct{}{}:
			default:
			}
		})
	}
	d.Start()
	h.daemon = d
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- adapterkit.Serve(ctx, aR, aW, adapterkit.Config{Adapter: declare.Info(), Capabilities: declare.Capabilities(), App: app})
	}()
	t.Cleanup(func() {
		_ = h.daemon.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return")
		}
		cancel()
	})
	var res adapterproto.InitializeResult
	h.call(adapterproto.MethodInitialize, adapterproto.InitializeParams{
		Protocol: adapterproto.SupportedRange(), Daemon: adapterproto.DaemonInfo{Version: "test"},
		Instance: adapterproto.InstanceInfo{ID: "codex", LockPath: filepath.Join(dir, "lock"), StateDir: dir},
	}, &res)
	return h
}

func (h *harness) call(method string, params, result any) error {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return h.daemon.Call(ctx, method, params, result)
}

func (h *harness) mustCall(method string, params, result any) {
	h.t.Helper()
	if err := h.call(method, params, result); err != nil {
		h.t.Fatalf("%s: %v", method, err)
	}
}

func presetConfig(t *testing.T, kv map[string]any) adapterproto.ConfigValues {
	v := adapterproto.ConfigValues{}
	for k, x := range kv {
		b, _ := json.Marshal(x)
		v[k] = b
	}
	return v
}

// start serves adapter.start with one preset called dev and the sessions given.
func (h *harness) start(sessions ...adapterproto.SessionAttach) adapterproto.StartResult {
	h.t.Helper()
	return h.startWith([]adapterproto.PresetConfig{
		{Name: "dev", Config: presetConfig(h.t, map[string]any{"cwd": h.dir})},
		{Name: "yolo", Config: presetConfig(h.t, map[string]any{"cwd": h.dir, "approvals": "approve", "sandbox": "danger-full-access"})},
	}, sessions...)
}

// startWith serves adapter.start with the presets and sessions given.
func (h *harness) startWith(presets []adapterproto.PresetConfig, sessions ...adapterproto.SessionAttach) adapterproto.StartResult {
	h.t.Helper()
	var res adapterproto.StartResult
	h.mustCall(adapterproto.MethodAdapterStart, adapterproto.StartParams{
		Node: "node", RuntimeEndpoint: runtimeEP, Config: adapterproto.ConfigValues{}, Presets: presets,
		Sessions: append([]adapterproto.SessionAttach{}, sessions...), DirectEndpoints: []adapterproto.DirectAttach{},
	}, &res)
	return res
}

func createParams(attempt, endpoint, preset string) adapterproto.SessionCreateParams {
	return adapterproto.SessionCreateParams{
		Attempt: adapterproto.AttemptID(attempt), RuntimeEndpoint: runtimeEP, SessionEndpoint: adapterproto.EndpointRef(endpoint), Preset: preset,
		FirstPrompt: adapterproto.FirstPrompt{Attempt: adapterproto.AttemptID(attempt), Text: "hello"},
		ToolAccess:  adapterproto.ToolAccess{Env: map[string]string{"SIDERA_TOKEN": token, "SIDERA_HOME": "/home/sidera"}},
	}
}

// waitNote waits for a notification that satisfies ok, among those not seen by an
// earlier call that asked for the same method.
func (h *harness) waitNote(method string, ok func(json.RawMessage) bool) json.RawMessage {
	h.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		h.mu.Lock()
		for _, n := range h.notes {
			if n.method == method && (ok == nil || ok(n.raw)) {
				h.mu.Unlock()
				return n.raw
			}
		}
		h.mu.Unlock()
		select {
		case <-h.changed:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			h.t.Fatalf("no %s notification; saw %v", method, h.noteNames())
		}
	}
}

func (h *harness) noteNames() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, n := range h.notes {
		out = append(out, n.method+" "+string(n.raw))
	}
	return out
}

func (h *harness) countNotes(method string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, x := range h.notes {
		if x.method == method {
			n++
		}
	}
	return n
}

func codeOf(t *testing.T, err error) int {
	t.Helper()
	var pe *peer.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not an error object", err)
	}
	return pe.Code
}

// create runs a session.create and requires it to answer created.
func (h *harness) create(attempt, endpoint, preset string) adapterproto.SessionCreateResult {
	h.t.Helper()
	var res adapterproto.SessionCreateResult
	h.mustCall(adapterproto.MethodSessionCreate, createParams(attempt, endpoint, preset), &res)
	return res
}

// lastAvailability returns the newest availability snapshot.
func (h *harness) lastAvailability() json.RawMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.notes) - 1; i >= 0; i-- {
		if h.notes[i].method == adapterproto.MethodAdapterAvailability {
			return h.notes[i].raw
		}
	}
	return nil
}
