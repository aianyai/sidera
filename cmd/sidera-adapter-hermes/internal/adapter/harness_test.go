package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban/kanbantest"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

func TestMain(m *testing.M) {
	kanbantest.MaybeRun()
	os.Exit(m.Run())
}

const lease = "11111111111111111111111111111111"

// fastTiming makes the loops react within milliseconds.
var fastTiming = timing{
	receivePoll: 20 * time.Millisecond,
	idleCheck:   time.Hour,
	retryMin:    40 * time.Millisecond,
	retryMax:    80 * time.Millisecond,
	registerMin: 5 * time.Millisecond,
	registerMax: 20 * time.Millisecond,
}

// daemon is a stand-in for the daemon's side of the protocol: it answers the
// endpoint.* calls of the adapter and records them. A key registers as the
// endpoint "ep-<n>" with n counting its keys, so the same key is the same endpoint.
type daemon struct {
	directAccess bool
	// recvAnswer, if set, answers each endpoint.recv; without it every page is empty.
	recvAnswer func(p adapterproto.RecvParams) adapterproto.RecvPage

	mu          sync.Mutex
	refs        map[string]adapterproto.EndpointRef
	registers   []adapterproto.RegisterParams
	declares    []adapterproto.DeclareParams
	unregisters []adapterproto.UnregisterParams
	recvs       []adapterproto.RecvParams
	acks        []adapterproto.AckParams
	available   []adapterproto.AvailabilityParams
}

func (d *daemon) ref(key string) adapterproto.EndpointRef {
	if d.refs == nil {
		d.refs = map[string]adapterproto.EndpointRef{}
	}
	if r, ok := d.refs[key]; ok {
		return r
	}
	r := adapterproto.EndpointRef(fmt.Sprintf("ep-%d", len(d.refs)+1))
	d.refs[key] = r
	return r
}

func decode[T any](raw json.RawMessage) T {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		panic(err)
	}
	return v
}

// harness runs the real adapter against the stub daemon, over pipes.
type harness struct {
	t      *testing.T
	d      *daemon
	peer   *peer.Peer
	app    *App
	hermes string // the control directory of the fake hermes
	done   chan error

	stopOnce sync.Once
}

type options struct {
	d *daemon
	// stateDir lets a second harness restart on the state of a first.
	stateDir string
	listed   []adapterproto.DirectAttach
	flows    Flows
	// hermesDown starts with the fake Hermes failing.
	hermesDown bool
	// ready is the fake Hermes' answer to listing the ready cards.
	ready string
	now   func() time.Time
}

func start(t *testing.T, o options) *harness {
	t.Helper()
	d := o.d
	if o.stateDir == "" {
		o.stateDir = t.TempDir()
	}
	hermesCtl := t.TempDir()
	if o.hermesDown {
		if err := os.WriteFile(filepath.Join(hermesCtl, kanbantest.DownFile), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if o.ready != "" {
		if err := os.WriteFile(filepath.Join(hermesCtl, kanbantest.ReadyFile), []byte(o.ready), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(kanbantest.ControlEnv, hermesCtl)
	exe := kanbantest.Executable(t, kanbantest.ModeBoard)

	aR, dW := io.Pipe()
	dR, aW := io.Pipe()
	p, err := peer.New(dR, dW, peer.Config{})
	if err != nil {
		t.Fatal(err)
	}
	p.Handle(adapterproto.MethodEndpointRegister, func(_ context.Context, raw json.RawMessage) (any, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		reg := decode[adapterproto.RegisterParams](raw)
		d.registers = append(d.registers, reg)
		return adapterproto.RegisterResult{Endpoint: d.ref(reg.Key)}, nil
	})
	p.Handle(adapterproto.MethodEndpointDeclare, func(_ context.Context, raw json.RawMessage) (any, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		dec := decode[adapterproto.DeclareParams](raw)
		d.declares = append(d.declares, dec)
		return adapterproto.DeclareResult{Declaration: adapterproto.Declaration{AcceptFrom: []adapterproto.Address{}, HiddenFromDirectory: true}}, nil
	})
	p.Handle(adapterproto.MethodEndpointUnregister, func(_ context.Context, raw json.RawMessage) (any, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.unregisters = append(d.unregisters, decode[adapterproto.UnregisterParams](raw))
		return adapterproto.Empty{}, nil
	})
	p.Handle(adapterproto.MethodEndpointRecv, func(_ context.Context, raw json.RawMessage) (any, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		rp := decode[adapterproto.RecvParams](raw)
		d.recvs = append(d.recvs, rp)
		page := adapterproto.RecvPage{Lease: lease, Messages: []adapterproto.PageMessage{}}
		if d.recvAnswer != nil {
			page = d.recvAnswer(rp)
		}
		return adapterproto.RecvResult{Page: page}, nil
	})
	p.Handle(adapterproto.MethodEndpointAck, func(_ context.Context, raw json.RawMessage) (any, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		ack := decode[adapterproto.AckParams](raw)
		d.acks = append(d.acks, ack)
		var res adapterproto.AckResult
		for _, it := range ack.Items {
			res.Items = append(res.Items, adapterproto.PartAck{MessageID: it.MessageID, Part: it.Part, Ack: adapterproto.ItemRecorded})
		}
		return res, nil
	})
	p.HandleNotification(adapterproto.MethodAdapterAvailability, func(_ context.Context, raw json.RawMessage) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.available = append(d.available, decode[adapterproto.AvailabilityParams](raw))
	})
	p.Start()

	flows := o.flows
	if flows == nil {
		flows = LogOnly
	}
	app := New(slog.New(slog.DiscardHandler), flows)
	app.tm = fastTiming
	if o.now != nil {
		app.now = o.now
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, d: d, peer: p, app: app, hermes: hermesCtl, done: make(chan error, 1)}
	go func() { h.done <- serveApp(ctx, aR, aW, slog.New(slog.DiscardHandler), app) }()
	t.Cleanup(func() {
		h.stop()
		cancel()
	})

	call := func(method string, params, result any) {
		t.Helper()
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		if err := p.Call(cctx, method, params, result); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
	}
	var init adapterproto.InitializeResult
	call(adapterproto.MethodInitialize, adapterproto.InitializeParams{
		Protocol: adapterproto.SupportedRange(), Daemon: adapterproto.DaemonInfo{Version: "test"},
		Instance:     adapterproto.InstanceInfo{ID: AdapterID, LockPath: filepath.Join(t.TempDir(), "adapter.lock"), StateDir: o.stateDir},
		Capabilities: adapterproto.DaemonCapabilities{DirectEndpointAccess: d.directAccess},
	}, &init)
	if init.Capabilities.Direct == nil || init.Capabilities.Runtime != nil {
		t.Fatalf("capabilities %+v, want direct and no runtime", init.Capabilities)
	}
	listed := o.listed
	if listed == nil {
		listed = []adapterproto.DirectAttach{}
	}
	call(adapterproto.MethodAdapterStart, adapterproto.StartParams{
		Node: "node-1", Config: configValues(exe, o.stateDir), Presets: []adapterproto.PresetConfig{},
		Sessions: []adapterproto.SessionAttach{}, DirectEndpoints: listed,
	}, &adapterproto.StartResult{})
	return h
}

func configValues(hermes, home string) adapterproto.ConfigValues {
	v := adapterproto.ConfigValues{}
	for k, x := range map[string]any{
		"hermes_path":      hermes,
		"hermes_home":      home,
		"board":            "lane",
		"home_profile":     "default",
		"home_platform":    "feishu",
		"home_chat_id":     "oc_1",
		"aliases":          []string{"codex=nodeb/codex;modes=message"},
		"endpoint_idle":    "1h",
		"poll_interval":    "1s",
		"max_endpoints":    4,
		"display_name":     "Hermes (test)",
		"no_reply_timeout": "1h",
	} {
		raw, _ := json.Marshal(x)
		v[k] = raw
	}
	return v
}

// stop ends the connection as the daemon does and waits for the adapter to end.
func (h *harness) stop() {
	h.stopOnce.Do(func() {
		_ = h.peer.Close()
		select {
		case <-h.done:
		case <-time.After(10 * time.Second):
			h.t.Error("the adapter did not stop after the daemon closed the stream")
		}
	})
}

// pending tells the adapter, as the daemon does, that messages wait for the endpoint.
func (h *harness) pending(ref adapterproto.EndpointRef) {
	h.t.Helper()
	if err := h.peer.Notify(context.Background(), adapterproto.MethodEndpointPending, adapterproto.PendingParams{Endpoint: ref, Count: 1}); err != nil {
		h.t.Fatal(err)
	}
}

// eventually waits until cond holds, which it is asked about with the daemon locked.
func (h *harness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.d.mu.Lock()
		ok := cond()
		h.d.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s", what)
}

// instanceRef waits for the instance endpoint to be registered and returns it.
func (h *harness) instanceRef() adapterproto.EndpointRef {
	h.t.Helper()
	h.eventually("the instance endpoint to be registered", func() bool { return len(h.d.registers) > 0 })
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	return h.d.refs[InstanceKey]
}
