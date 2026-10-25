package fakeadapter_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// testStart is the origin of every test clock.
var testStart = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

const (
	waitLimit = 10 * time.Second
	leaseA    = "0123456789abcdef0123456789abcdef"
)

// attemptID returns the n-th attempt identifier: a lowercase UUID version 4.
func attemptID(n int) adapterproto.AttemptID {
	return adapterproto.AttemptID(fmt.Sprintf("00000000-0000-4000-8000-%012d", n))
}

// frameLog records every line that crosses one pipe, in order, with the
// direction it travelled.
type frameLog struct {
	mu    sync.Mutex
	lines []loggedLine
}

type loggedLine struct {
	toAdapter bool
	line      []byte
}

func (l *frameLog) add(toAdapter bool, line []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, loggedLine{toAdapter, bytes.Clone(line)})
}

func (l *frameLog) snapshot() []loggedLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]loggedLine(nil), l.lines...)
}

// tap is a WriteCloser that copies every complete line it carries to the log.
type tap struct {
	dst       io.WriteCloser
	log       *frameLog
	toAdapter bool
	buf       []byte
}

func (p *tap) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			break
		}
		p.log.add(p.toAdapter, p.buf[:i])
		p.buf = p.buf[i+1:]
	}
	return p.dst.Write(b)
}

func (p *tap) Close() error { return p.dst.Close() }

// cannedPage is the page the fake agent's recv returns in the rigs.
func cannedPage(lease string) []byte {
	page := adapterproto.RecvPage{
		Lease: adapterproto.LeaseRef(lease),
		Messages: []adapterproto.PageMessage{
			{ID: "m1", From: "alice/agent", Body: "hello from the other node"},
		},
	}
	b, _ := json.Marshal(page)
	return b
}

// pageRunner is the ActionRunner of the rigs: recv returns the canned page and
// every other action succeeds with no output.
type pageRunner struct {
	mu    sync.Mutex
	calls []string
}

func (r *pageRunner) RunAction(_ context.Context, _ fakeruntime.SessionInfo, a fakeruntime.TurnAction) (fakeruntime.ActionResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, a.Kind)
	r.mu.Unlock()
	if a.Kind == fakeruntime.ActionRecv {
		return fakeruntime.ActionResult{Output: cannedPage(leaseA)}, nil
	}
	return fakeruntime.ActionResult{}, nil
}

// rig runs one fake adapter in process against a scripted daemon side: a peer
// that answers what an adapter calls with valid results.
type rig struct {
	t       *testing.T
	ctx     context.Context
	cancel  context.CancelFunc
	clock   *clocktest.Fake
	mem     *ledger.Memory
	knobs   *fakeadapter.Knobs
	runtime *fakeruntime.Runtime
	runner  *pageRunner
	opts    fakeadapter.ManifestOptions
	log     *frameLog
	daemon  *peer.Peer
	served  chan error

	mu       sync.Mutex
	notified []notification
	observed []adapterproto.RecvObservedParams
	requests map[string]int // calls the adapter made, by method
	changed  chan struct{}
}

type notification struct {
	Method string
	Params json.RawMessage
}

type rigOptions struct {
	manifest func(*fakeadapter.ManifestOptions)
	// refuseObserved makes the daemon side refuse every recv.observed.
	refuseObserved bool
}

func newRig(t *testing.T, ro rigOptions) *rig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &rig{
		t: t, ctx: ctx, cancel: cancel,
		clock:    clocktest.NewFake(testStart),
		mem:      ledger.NewMemory(),
		knobs:    fakeadapter.NewKnobs(),
		runner:   &pageRunner{},
		opts:     fakeadapter.DefaultManifestOptions(),
		log:      &frameLog{},
		served:   make(chan error, 1),
		requests: map[string]int{},
		changed:  make(chan struct{}, 1),
	}
	if ro.manifest != nil {
		ro.manifest(&r.opts)
	}
	rt, err := fakeruntime.New(fakeruntime.Config{
		Now:     r.clock.Now,
		Ledgers: r.mem,
		Dir:     t.TempDir(),
		Seed:    7,
		Runner:  r.runner,
	})
	if err != nil {
		t.Fatalf("fakeruntime.New: %v", err)
	}
	r.runtime = rt

	adapterWriter, err := r.mem.Open("adapter/fake", 0, r.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	toAdapterR, toAdapterW := io.Pipe()
	fromAdapterR, fromAdapterW := io.Pipe()
	go func() {
		r.served <- fakeadapter.Serve(ctx, toAdapterR, &tap{dst: fromAdapterW, log: r.log}, fakeadapter.Config{
			InstanceID: "fake",
			Now:        r.clock.Now,
			Ledger:     adapterWriter,
			Runtime:    rt,
			Manifest:   r.opts,
			Knobs:      r.knobs,
			Logger:     slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		})
	}()

	d, err := peer.New(fromAdapterR, &tap{dst: toAdapterW, log: r.log, toAdapter: true}, peer.Config{})
	if err != nil {
		t.Fatal(err)
	}
	r.daemon = d
	r.installDaemonHandlers(ro)
	d.Start()
	t.Cleanup(func() {
		r.cancel()
		_ = d.Close()
		_ = toAdapterW.Close()
		_ = fromAdapterW.Close()
		select {
		case <-r.served:
		case <-time.After(waitLimit):
			t.Errorf("fakeadapter.Serve did not return after its context ended")
		}
		_ = rt.Close()
	})
	return r
}

func (r *rig) bump() {
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

func (r *rig) installDaemonHandlers(ro rigOptions) {
	count := func(method string) {
		r.mu.Lock()
		r.requests[method]++
		r.mu.Unlock()
		r.bump()
	}
	handle := func(method string, h func(params json.RawMessage) (any, error)) {
		r.daemon.Handle(method, func(_ context.Context, params json.RawMessage) (any, error) {
			count(method)
			return h(params)
		})
	}
	handle(adapterproto.MethodPing, func(json.RawMessage) (any, error) { return adapterproto.Empty{}, nil })
	handle(adapterproto.MethodCheckpointPut, func(json.RawMessage) (any, error) { return adapterproto.Empty{}, nil })
	handle(adapterproto.MethodRecvObserved, func(params json.RawMessage) (any, error) {
		var p adapterproto.RecvObservedParams
		if err := adapterproto.Decode(params, &p); err != nil {
			return nil, peer.NewError(peer.CodeInvalidParams, err.Error())
		}
		r.mu.Lock()
		r.observed = append(r.observed, p)
		r.mu.Unlock()
		if ro.refuseObserved || !strings.Contains(p.Observed, "hello from the other node") {
			return adapterproto.RecvObservedResult{Verdict: adapterproto.AckRefused, Reason: adapterproto.AckTruncated, Items: []adapterproto.PartAck{}}, nil
		}
		return adapterproto.RecvObservedResult{
			Verdict: adapterproto.AckAllowed, Reason: adapterproto.AckSeenInFull,
			Items: []adapterproto.PartAck{{MessageID: "m1", Part: 1, Ack: adapterproto.ItemCompleted}},
		}, nil
	})
	handle(adapterproto.MethodEndpointRegister, func(params json.RawMessage) (any, error) {
		var p adapterproto.RegisterParams
		if err := adapterproto.Decode(params, &p); err != nil {
			return nil, peer.NewError(peer.CodeInvalidParams, err.Error())
		}
		return adapterproto.RegisterResult{Endpoint: adapterproto.EndpointRef("ep-" + p.Key)}, nil
	})
	handle(adapterproto.MethodEndpointUnregister, func(json.RawMessage) (any, error) { return adapterproto.Empty{}, nil })
	handle(adapterproto.MethodEndpointRecv, func(json.RawMessage) (any, error) {
		return adapterproto.RecvResult{Page: adapterproto.RecvPage{
			Lease:    leaseA,
			Messages: []adapterproto.PageMessage{{ID: "m2", From: "bob/chat", Body: "direct hello"}},
		}}, nil
	})
	handle(adapterproto.MethodEndpointAck, func(params json.RawMessage) (any, error) {
		var p adapterproto.AckParams
		if err := adapterproto.Decode(params, &p); err != nil {
			return nil, peer.NewError(peer.CodeInvalidParams, err.Error())
		}
		items := make([]adapterproto.PartAck, len(p.Items))
		for i, it := range p.Items {
			items[i] = adapterproto.PartAck{MessageID: it.MessageID, Part: it.Part, Ack: adapterproto.ItemRecorded}
		}
		return adapterproto.AckResult{Items: items}, nil
	})
	handle(adapterproto.MethodEndpointSend, func(json.RawMessage) (any, error) {
		return adapterproto.SendResult{MessageID: "sent-1"}, nil
	})
	handle(adapterproto.MethodEndpointMessageStatus, func(json.RawMessage) (any, error) {
		return adapterproto.MessageStatusResult{Recipients: []adapterproto.RecipientStatus{{To: "bob/chat", State: adapterproto.DeliveryDelivered}}}, nil
	})
	for _, m := range []string{
		adapterproto.MethodAdapterAvailability, adapterproto.MethodPromptPersisted,
		adapterproto.MethodTurnStarted, adapterproto.MethodTurnEnded, adapterproto.MethodNativeRequestHandled,
	} {
		method := m
		r.daemon.HandleNotification(method, func(_ context.Context, params json.RawMessage) {
			r.mu.Lock()
			r.notified = append(r.notified, notification{method, bytes.Clone(params)})
			r.mu.Unlock()
			r.bump()
		})
	}
}

// call sends a request of the daemon side and returns the raw result.
func (r *rig) call(method string, params any) (json.RawMessage, error) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(r.ctx, waitLimit)
	defer cancel()
	var raw json.RawMessage
	err := r.daemon.Call(ctx, method, params, &raw)
	return raw, err
}

// mustCall is call for a request that must succeed; the result is decoded
// strictly into out.
func (r *rig) mustCall(method string, params any, out adapterproto.Message) {
	r.t.Helper()
	raw, err := r.call(method, params)
	if err != nil {
		r.t.Fatalf("%s: %v", method, err)
	}
	if out != nil {
		if err := adapterproto.Decode(raw, out); err != nil {
			r.t.Fatalf("%s result: %v", method, err)
		}
	}
}

// waitFor polls until cond holds, failing the test after the limit.
func (r *rig) waitFor(what string, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(waitLimit)
	for !cond() {
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out waiting for %s", what)
		}
		select {
		case <-r.changed:
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (r *rig) notifications(method string) []json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []json.RawMessage
	for _, n := range r.notified {
		if n.Method == method {
			out = append(out, n.Params)
		}
	}
	return out
}

func (r *rig) waitNotifications(method string, n int) []json.RawMessage {
	r.t.Helper()
	var got []json.RawMessage
	r.waitFor(fmt.Sprintf("%d %s notifications", n, method), func() bool {
		got = r.notifications(method)
		return len(got) >= n
	})
	return got
}

func (r *rig) observedCalls() []adapterproto.RecvObservedParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]adapterproto.RecvObservedParams(nil), r.observed...)
}

func (r *rig) requestCount(method string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[method]
}

// initialize runs the handshake the daemon starts every connection with.
func (r *rig) initialize() adapterproto.InitializeResult {
	r.t.Helper()
	var res adapterproto.InitializeResult
	r.mustCall(adapterproto.MethodInitialize, adapterproto.InitializeParams{
		Protocol:     adapterproto.SupportedRange(),
		Daemon:       adapterproto.DaemonInfo{Version: "0.1.0"},
		Instance:     adapterproto.InstanceInfo{ID: "fake", LockPath: r.t.TempDir() + "/instance.lock"},
		Capabilities: adapterproto.DaemonCapabilities{},
	}, &res)
	return res
}

// start runs adapter.start with the runtime endpoint and no sessions.
func (r *rig) start(sessions []adapterproto.SessionAttach, direct []adapterproto.DirectAttach) adapterproto.StartResult {
	r.t.Helper()
	if sessions == nil {
		sessions = []adapterproto.SessionAttach{}
	}
	if direct == nil {
		direct = []adapterproto.DirectAttach{}
	}
	var res adapterproto.StartResult
	r.mustCall(adapterproto.MethodAdapterStart, adapterproto.StartParams{
		Node:            "node-1",
		RuntimeEndpoint: "rt-1",
		Config:          adapterproto.ConfigValues{},
		Presets:         []adapterproto.PresetConfig{{Name: "default", Config: adapterproto.ConfigValues{}}},
		Sessions:        sessions,
		DirectEndpoints: direct,
	}, &res)
	return res
}

// createSession asks for a session on endpoint ep through attempt n.
func (r *rig) createSession(n int, ep string) adapterproto.SessionCreateResult {
	r.t.Helper()
	var res adapterproto.SessionCreateResult
	r.mustCall(adapterproto.MethodSessionCreate, r.createParams(n, ep), &res)
	return res
}

func (r *rig) createParams(n int, ep string) adapterproto.SessionCreateParams {
	return adapterproto.SessionCreateParams{
		Attempt:         attemptID(n),
		RuntimeEndpoint: "rt-1",
		SessionEndpoint: adapterproto.EndpointRef(ep),
		Preset:          "default",
		FirstPrompt:     adapterproto.FirstPrompt{Attempt: attemptID(n), Text: "start working"},
		ToolAccess:      adapterproto.ToolAccess{Env: map[string]string{"SIDERA_TOKEN": "t"}},
	}
}

// readLines splits what a pipe carried into its lines, for raw drivers.
func readLine(br *bufio.Reader) ([]byte, error) {
	line, err := br.ReadBytes('\n')
	return bytes.TrimSuffix(line, []byte("\n")), err
}

// testWriter sends log output to the test log.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
