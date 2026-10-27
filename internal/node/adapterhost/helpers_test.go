package adapterhost_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/logfile"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

var testStart = time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)

// waitLimit is how long a test waits, on the real clock, for something that
// happens on its own. Nothing here waits a fixed time.
const waitLimit = 10 * time.Second

// eventually polls cond on the real clock until it holds or waitLimit passes.
func eventually(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// --- leak guard ---

// hostGoroutines returns the stacks of live goroutines that belong to the host,
// to a peer, to the fake adapter or to the fake runtime.
func hostGoroutines() []string {
	var buf bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
	var found []string
	for _, block := range strings.Split(buf.String(), "\n\n") {
		if strings.Contains(block, "internal/node/adapterhost.") ||
			strings.Contains(block, "sdk/go/peer.(*Peer).") ||
			(strings.Contains(block, "testkit/fakeadapter.") || strings.Contains(block, "testkit/fakeruntime.")) && !strings.Contains(block, "_test.go") {
			found = append(found, block)
		}
	}
	return found
}

func settledGoroutines() []string {
	deadline := time.Now().Add(5 * time.Second)
	for {
		left := hostGoroutines()
		if len(left) == 0 || time.Now().After(deadline) {
			return left
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// leakGuard fails the test if a goroutine of the host, of a peer or of the fakes is
// alive after the test and its cleanups. Register it before anything else: cleanups
// run in reverse order, so it runs last.
func leakGuard(t *testing.T) {
	t.Helper()
	if left := settledGoroutines(); len(left) != 0 {
		t.Fatalf("%d goroutines alive before the test started:\n%s", len(left), strings.Join(left, "\n\n"))
	}
	t.Cleanup(func() {
		if left := settledGoroutines(); len(left) != 0 {
			t.Errorf("%d goroutines leaked:\n%s", len(left), strings.Join(left, "\n\n"))
		}
	})
}

// --- the engine ---

// event is one call the host made on the engine.
type event struct {
	Kind     string // admitted, started, probeTokens, probed, gone, notified, register, unregister, checkpoint, send, recv, ack, status, observed
	Instance string
	Cause    adapterhost.GoneCause
	Method   string               // of a notification
	Params   adapterproto.Message // of a notification, or the parameters of a request
	Admitted adapterreg.Admission
	Info     adapterproto.InitializeResult
	Started  adapterproto.StartResult
	Probed   adapterproto.ProbeResult
}

// engineDouble records every call of the host in order. It implements the
// optional parts of the engine too; basicEngine hides them.
type engineDouble struct {
	mu      sync.Mutex
	events  []event
	changed chan struct{}

	// startParams is what AdapterAdmitted returns; nil makes an empty valid set.
	startParams func(instance string, a adapterreg.Admission) adapterproto.StartParams
	probes      []adapterproto.ToolProbeRequest
	// errs makes the method named by its event kind fail.
	errs map[string]error
	// admitGate, when set, holds AdapterAdmitted until it is closed.
	admitGate chan struct{}
	// notifyGate, when set, holds AdapterNotified until it is closed.
	notifyGate chan struct{}
	// registerGate, when set, holds RegisterDirect until it is closed.
	registerGate chan struct{}

	// onEvent is called with the kind of every call, as it is made.
	onEvent func(kind string)

	onRegister   func(adapterproto.RegisterParams) (adapterproto.RegisterResult, error)
	onUnregister func(adapterproto.UnregisterParams) error
	onCheckpoint func(adapterproto.CheckpointPutParams) error
}

func newEngine() *engineDouble {
	return &engineDouble{changed: make(chan struct{}, 1), errs: map[string]error{}}
}

func (d *engineDouble) record(e event) {
	d.mu.Lock()
	d.events = append(d.events, e)
	hook := d.onEvent
	d.mu.Unlock()
	if hook != nil {
		hook(e.Kind)
	}
	select {
	case d.changed <- struct{}{}:
	default:
	}
}

func (d *engineDouble) snapshot() []event {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]event(nil), d.events...)
}

// of returns the events of one kind, in order.
func (d *engineDouble) of(kind string) []event {
	var out []event
	for _, e := range d.snapshot() {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// kinds returns the kinds of every event, in order.
func (d *engineDouble) kinds() []string {
	var out []string
	for _, e := range d.snapshot() {
		out = append(out, e.Kind)
	}
	return out
}

func (d *engineDouble) failure(kind string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.errs[kind]
}

func (d *engineDouble) AdapterAdmitted(ctx context.Context, instance string, a adapterreg.Admission, info adapterproto.InitializeResult) (adapterproto.StartParams, error) {
	d.record(event{Kind: "admitted", Instance: instance, Admitted: a, Info: info})
	if d.admitGate != nil {
		select {
		case <-d.admitGate:
		case <-ctx.Done():
			return adapterproto.StartParams{}, ctx.Err()
		}
	}
	if err := d.failure("admitted"); err != nil {
		return adapterproto.StartParams{}, err
	}
	if d.startParams != nil {
		return d.startParams(instance, a), nil
	}
	return defaultStartParams(a), nil
}

func defaultStartParams(a adapterreg.Admission) adapterproto.StartParams {
	p := adapterproto.StartParams{
		Node:            "node-1",
		Config:          adapterproto.ConfigValues{},
		Presets:         []adapterproto.PresetConfig{},
		Sessions:        []adapterproto.SessionAttach{},
		DirectEndpoints: []adapterproto.DirectAttach{},
	}
	if a.Declares(adapterreg.KindRuntime) {
		p.RuntimeEndpoint = "rt-1"
	}
	return p
}

func (d *engineDouble) AdapterStarted(_ context.Context, instance string, r adapterproto.StartResult) error {
	d.record(event{Kind: "started", Instance: instance, Started: r})
	return d.failure("started")
}

func (d *engineDouble) ProbeTokens(_ context.Context, instance string) ([]adapterproto.ToolProbeRequest, error) {
	d.record(event{Kind: "probeTokens", Instance: instance})
	if err := d.failure("probeTokens"); err != nil {
		return nil, err
	}
	return d.probes, nil
}

func (d *engineDouble) AdapterProbed(_ context.Context, instance string, r adapterproto.ProbeResult) error {
	d.record(event{Kind: "probed", Instance: instance, Probed: r})
	return d.failure("probed")
}

func (d *engineDouble) AdapterGone(_ context.Context, instance string, cause adapterhost.GoneCause) error {
	d.record(event{Kind: "gone", Instance: instance, Cause: cause})
	return d.failure("gone")
}

func (d *engineDouble) AdapterNotified(ctx context.Context, instance string, n adapterhost.Notification) error {
	d.record(event{Kind: "notified", Instance: instance, Method: n.Method, Params: n.Params})
	if d.notifyGate != nil {
		select {
		case <-d.notifyGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return d.failure("notified")
}

func (d *engineDouble) RegisterDirect(ctx context.Context, instance string, p adapterproto.RegisterParams) (adapterproto.RegisterResult, error) {
	d.record(event{Kind: "register", Instance: instance, Params: p})
	if d.registerGate != nil {
		select {
		case <-d.registerGate:
		case <-ctx.Done():
			return adapterproto.RegisterResult{}, ctx.Err()
		}
	}
	if d.onRegister != nil {
		return d.onRegister(p)
	}
	return adapterproto.RegisterResult{Endpoint: "ep-" + adapterproto.EndpointRef(p.Key)}, nil
}

func (d *engineDouble) UnregisterDirect(_ context.Context, instance string, p adapterproto.UnregisterParams) error {
	d.record(event{Kind: "unregister", Instance: instance, Params: p})
	if d.onUnregister != nil {
		return d.onUnregister(p)
	}
	return nil
}

func (d *engineDouble) CheckpointPut(_ context.Context, instance string, p adapterproto.CheckpointPutParams) error {
	d.record(event{Kind: "checkpoint", Instance: instance, Params: p})
	if d.onCheckpoint != nil {
		return d.onCheckpoint(p)
	}
	return nil
}

func (d *engineDouble) EndpointSend(_ context.Context, instance string, p adapterproto.SendParams) (adapterproto.SendResult, error) {
	d.record(event{Kind: "send", Instance: instance, Params: p})
	return adapterproto.SendResult{MessageID: "m-1"}, nil
}

func (d *engineDouble) EndpointRecv(_ context.Context, instance string, p adapterproto.RecvParams) (adapterproto.RecvResult, error) {
	d.record(event{Kind: "recv", Instance: instance, Params: p})
	return adapterproto.RecvResult{Page: adapterproto.RecvPage{
		Lease:    "0123456789abcdef0123456789abcdef",
		Messages: []adapterproto.PageMessage{{ID: "m-2", From: "bob/chat", Body: "hello"}},
	}}, nil
}

func (d *engineDouble) EndpointAck(_ context.Context, instance string, p adapterproto.AckParams) (adapterproto.AckResult, error) {
	d.record(event{Kind: "ack", Instance: instance, Params: p})
	items := make([]adapterproto.PartAck, len(p.Items))
	for i, it := range p.Items {
		items[i] = adapterproto.PartAck{MessageID: it.MessageID, Part: it.Part, Ack: adapterproto.ItemRecorded}
	}
	return adapterproto.AckResult{Items: items}, nil
}

func (d *engineDouble) EndpointMessageStatus(_ context.Context, instance string, p adapterproto.MessageStatusParams) (adapterproto.MessageStatusResult, error) {
	d.record(event{Kind: "status", Instance: instance, Params: p})
	return adapterproto.MessageStatusResult{Recipients: []adapterproto.RecipientStatus{{To: "bob/chat", State: adapterproto.DeliveryDelivered}}}, nil
}

func (d *engineDouble) RecvObserved(_ context.Context, instance string, p adapterproto.RecvObservedParams) (adapterproto.RecvObservedResult, error) {
	d.record(event{Kind: "observed", Instance: instance, Params: p})
	return adapterproto.RecvObservedResult{Verdict: adapterproto.AckRefused, Reason: adapterproto.AckTruncated, Items: []adapterproto.PartAck{}}, nil
}

// basicEngine is the engine without the optional parts: the host must answer
// their requests as unknown methods.
type basicEngine struct{ adapterhost.Engine }

// --- the configuration, the logs ---

type sourceDouble struct {
	mu        sync.Mutex
	instances map[string]config.Instance
	adapters  []config.Adapter
}

func (s *sourceDouble) Instances() config.Instances {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make(map[string]config.Instance, len(s.instances))
	for k, v := range s.instances {
		items[k] = v
	}
	return config.Instances{Version: 1, Items: items}
}

func (s *sourceDouble) Adapters() []config.Adapter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]config.Adapter(nil), s.adapters...)
}

func (s *sourceDouble) setInstance(name string, in config.Instance) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instances[name] = in
}

// logDouble opens logs on a log handle over an in-memory file system.
type logDouble struct {
	mem     *fsxtest.Mem
	handle  *logfile.Handle
	mu      sync.Mutex
	opened  []string
	failing bool
}

func newLogs(t testing.TB) *logDouble {
	t.Helper()
	mem := fsxtest.NewMem()
	if err := fsx.MakeDir(mem, fsx.Tag{Tx: "setup", Role: "dir"}, "logs"); err != nil {
		t.Fatal(err)
	}
	return &logDouble{mem: mem, handle: logfile.New(mem, logfile.Budget{Total: func() int64 { return 1 << 20 }})}
}

func (l *logDouble) Open(name string) (*logfile.Log, error) {
	l.mu.Lock()
	l.opened = append(l.opened, name)
	failing := l.failing
	l.mu.Unlock()
	if failing {
		return nil, errors.New("the log cannot be opened")
	}
	return l.handle.Open(name)
}

func (l *logDouble) read(name string) string {
	b, err := l.mem.ReadFile("logs/" + name)
	if err != nil {
		return ""
	}
	return string(b)
}

// --- processes ---

// wire records, in one order, what the host writes to adapters and what the test
// and the engine double note, so that a test can check the order of both.
type timeline struct {
	mu    sync.Mutex
	items []string
}

func (w *timeline) add(s string) {
	w.mu.Lock()
	w.items = append(w.items, s)
	w.mu.Unlock()
}

func (w *timeline) snapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.items...)
}

// requestsWritten returns the methods of the lines of the timeline written by the host.
func (w *timeline) written() []string {
	var out []string
	for _, s := range w.snapshot() {
		if m, ok := strings.CutPrefix(s, "wire "); ok {
			out = append(out, m)
		}
	}
	return out
}

// tapWriter copies every complete line the host writes into the timeline.
type tapWriter struct {
	dst io.WriteCloser
	tl  *timeline
	buf []byte
}

func (w *tapWriter) Write(b []byte) (int, error) {
	w.buf = append(w.buf, b...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		var msg struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(w.buf[:i], &msg) == nil && msg.Method != "" {
			w.tl.add("wire " + msg.Method)
		}
		w.buf = w.buf[i+1:]
	}
	return w.dst.Write(b)
}

func (w *tapWriter) Close() error { return w.dst.Close() }

// tapProcess wraps a process so that what the host writes to it is on the timeline.
type tapProcess struct {
	adapterhost.Process
	tl     *timeline
	killed atomic.Int32
}

func (p *tapProcess) Stream() (io.Reader, io.WriteCloser) {
	r, w := p.Process.Stream()
	return r, &tapWriter{dst: w, tl: p.tl}
}

func (p *tapProcess) Kill() error {
	p.killed.Add(1)
	return p.Process.Kill()
}

// launcherDouble hands out the processes the host asks for.
type launcherDouble struct {
	mu        sync.Mutex
	instances []adapterhost.Instance
	procs     []adapterhost.Process
	// launch makes the n-th process (1 is the first launch).
	launch func(n int, inst adapterhost.Instance) (adapterhost.Process, error)
}

func (l *launcherDouble) Launch(_ context.Context, inst adapterhost.Instance) (adapterhost.Process, error) {
	l.mu.Lock()
	l.instances = append(l.instances, inst)
	n := len(l.instances)
	l.mu.Unlock()
	p, err := l.launch(n, inst)
	if err == nil {
		l.mu.Lock()
		l.procs = append(l.procs, p)
		l.mu.Unlock()
	}
	return p, err
}

func (l *launcherDouble) launches() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.instances)
}

func (l *launcherDouble) launched(i int) adapterhost.Instance {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.instances[i]
}

func (l *launcherDouble) proc(i int) adapterhost.Process {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.procs[i]
}

// pipeProcess runs an adapter in the test process over two pipes. serve gets the
// adapter's ends and returns when the adapter is over.
type pipeProcess struct {
	stdout *io.PipeReader // the adapter's output, which the host reads
	stdin  *io.PipeWriter // the adapter's input, which the host writes
	inR    *io.PipeReader
	outW   *io.PipeWriter

	done      chan struct{}
	serveDone chan struct{} // closed when the adapter has seen its stream end
	ended     sync.Once
	kills     atomic.Int32
	stubborn  bool          // Kill does not end the process until let go
	letGo     chan struct{} // closed by release
	relOnce   sync.Once
	killedCh  chan struct{}
}

func newPipeProcess(serve func(r io.Reader, w io.WriteCloser), stubborn bool) *pipeProcess {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &pipeProcess{
		stdout: outR, stdin: inW, inR: inR, outW: outW,
		done: make(chan struct{}), serveDone: make(chan struct{}), stubborn: stubborn, letGo: make(chan struct{}), killedCh: make(chan struct{}),
	}
	go func() {
		serve(inR, outW)
		close(p.serveDone)
		if p.stubborn {
			// A process that cannot be ended outlives its stream until let go.
			<-p.letGo
		}
		p.end()
		close(p.done)
	}()
	return p
}

func (p *pipeProcess) end() {
	p.ended.Do(func() {
		_ = p.outW.Close()
		_ = p.inR.Close()
		_ = p.stdout.Close()
		_ = p.stdin.Close()
	})
}

func (p *pipeProcess) Stream() (io.Reader, io.WriteCloser) { return p.stdout, p.stdin }
func (p *pipeProcess) PID() int                            { return 0 }

func (p *pipeProcess) Kill() error {
	if p.kills.Add(1) == 1 {
		close(p.killedCh)
	}
	if !p.stubborn {
		p.end()
	}
	return nil
}

func (p *pipeProcess) Wait() error { <-p.done; return nil }

// release lets a stubborn process end.
func (p *pipeProcess) release() { p.relOnce.Do(func() { close(p.letGo) }) }

// killed reports how many times Kill was called.
func (p *pipeProcess) killed() int { return int(p.kills.Load()) }

// --- a scripted adapter on the peer ---

// scripted is an adapter written on the peer, for the cases the fake adapter
// cannot do: a wrong answer, a request before admission, an invalid notification.
type scripted struct {
	t    testing.TB
	opts fakeadapter.ManifestOptions
	p    *peer.Peer

	// Behaviour, set in the configure function before the adapter starts.
	// initialize replaces the answer to initialize.
	initialize func(adapterproto.InitializeParams) (any, error)
	// ping decides what the n-th ping (from 1) is answered with. It runs in the
	// handler: it may block, and it may ignore the cancellation of its context.
	ping func(ctx context.Context, n int) error
	// handlers replace the answer to the method.
	handlers map[string]peer.RequestHandler
	// caps, when set, is what initialize declares instead of the options'.
	caps *adapterproto.Capabilities

	mu       sync.Mutex
	requests []string // the methods the host sent, in order
	params   map[string][]json.RawMessage
	pings    int
}

func (a *scripted) received() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.requests...)
}

func (a *scripted) count(method string) int {
	n := 0
	for _, m := range a.received() {
		if m == method {
			n++
		}
	}
	return n
}

func (a *scripted) pingCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pings
}

func (a *scripted) paramsOf(method string) []json.RawMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]json.RawMessage(nil), a.params[method]...)
}

// call is a request of the adapter to the host.
func (a *scripted) call(method string, params, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	return a.p.Call(ctx, method, params, result)
}

// notify is a notification of the adapter to the host.
func (a *scripted) notify(method string, params any) {
	a.t.Helper()
	if err := a.p.Notify(context.Background(), method, params); err != nil {
		a.t.Errorf("notify %s: %v", method, err)
	}
}

func (a *scripted) serve(r io.Reader, w io.WriteCloser) {
	p, err := peer.New(r, w, peer.Config{})
	if err != nil {
		a.t.Errorf("peer.New: %v", err)
		return
	}
	a.p = p
	handle := func(method string, h peer.RequestHandler) {
		if o, ok := a.handlers[method]; ok {
			h = o
		}
		p.Handle(method, func(ctx context.Context, params json.RawMessage) (any, error) {
			a.mu.Lock()
			a.requests = append(a.requests, method)
			if a.params == nil {
				a.params = map[string][]json.RawMessage{}
			}
			a.params[method] = append(a.params[method], append(json.RawMessage(nil), params...))
			a.mu.Unlock()
			return h(ctx, params)
		})
	}
	handle(adapterproto.MethodInitialize, func(_ context.Context, raw json.RawMessage) (any, error) {
		var ip adapterproto.InitializeParams
		if err := adapterproto.Decode(raw, &ip); err != nil {
			return nil, peer.NewError(peer.CodeInvalidParams, err.Error())
		}
		if a.initialize != nil {
			return a.initialize(ip)
		}
		return a.initializeResult(), nil
	})
	handle(adapterproto.MethodPing, func(ctx context.Context, _ json.RawMessage) (any, error) {
		a.mu.Lock()
		a.pings++
		n := a.pings
		a.mu.Unlock()
		if a.ping != nil {
			if err := a.ping(ctx, n); err != nil {
				return nil, err
			}
		}
		return adapterproto.Empty{}, nil
	})
	handle(adapterproto.MethodShutdown, func(context.Context, json.RawMessage) (any, error) { return adapterproto.Empty{}, nil })
	handle(adapterproto.MethodAdapterStart, func(context.Context, json.RawMessage) (any, error) {
		return adapterproto.StartResult{Sessions: []adapterproto.SessionState{}}, nil
	})
	handle(adapterproto.MethodAdapterProbe, func(context.Context, json.RawMessage) (any, error) {
		return adapterproto.ProbeResult{
			Auth: adapterproto.AuthValid, Dependencies: []adapterproto.Dependency{}, Presets: []adapterproto.PresetStatus{}, ToolProbes: []adapterproto.ToolProbeResult{},
		}, nil
	})
	for _, m := range []string{
		adapterproto.MethodSessionCreate, adapterproto.MethodPromptDeliver, adapterproto.MethodPromptReconcile,
		adapterproto.MethodSessionInterrupt, adapterproto.MethodSessionClose,
	} {
		method := m
		if _, ok := a.handlers[method]; ok {
			handle(method, nil)
		}
	}
	p.Start()
	<-p.Done()
	_ = p.Close()
}

func (a *scripted) initializeResult() adapterproto.InitializeResult {
	caps := a.opts.Capabilities()
	if a.caps != nil {
		caps = *a.caps
	}
	return adapterproto.InitializeResult{
		ProtocolVersion: 1,
		Adapter:         adapterproto.AdapterInfo{ID: a.opts.ID, Version: a.opts.Version},
		Capabilities:    caps,
	}
}

// --- the rig ---

type rigOptions struct {
	params adapterhost.Params
	// instances are the names of the enabled instances; "chat" when empty.
	instances []string
	// manifest changes the declaration of the adapter.
	manifest func(*fakeadapter.ManifestOptions)
	// launch replaces the launcher's behaviour.
	launch func(r *rig, n int, inst adapterhost.Instance) (adapterhost.Process, error)
	// basic hides the optional parts of the engine.
	basic bool
}

type rig struct {
	t       *testing.T
	clk     *clocktest.Fake
	home    string
	eng     *engineDouble
	src     *sourceDouble
	logs    *logDouble
	lau     *launcherDouble
	host    *adapterhost.Host
	opts    fakeadapter.ManifestOptions
	names   []string
	tl      *timeline
	runtime *fakeruntime.Runtime
	mem     *ledger.Memory
	records *logRecords

	knobsMu sync.Mutex
	knobs   map[string]*fakeadapter.Knobs
}

// logRecords collects what the host logs.
type logRecords struct {
	mu   sync.Mutex
	msgs []string
}

func (l *logRecords) Enabled(context.Context, slog.Level) bool { return true }
func (l *logRecords) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
		return true
	})
	l.mu.Lock()
	l.msgs = append(l.msgs, b.String())
	l.mu.Unlock()
	return nil
}
func (l *logRecords) WithAttrs(attrs []slog.Attr) slog.Handler { return &withAttrs{l, attrs} }
func (l *logRecords) WithGroup(string) slog.Handler            { return l }

type withAttrs struct {
	*logRecords
	attrs []slog.Attr
}

func (w *withAttrs) Handle(ctx context.Context, r slog.Record) error {
	r = r.Clone()
	r.AddAttrs(w.attrs...)
	return w.logRecords.Handle(ctx, r)
}
func (w *withAttrs) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &withAttrs{w.logRecords, append(append([]slog.Attr(nil), w.attrs...), attrs...)}
}

func (l *logRecords) contains(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// testParams are small parameters for a test that drives the clock: every wait is
// a number a test can name.
func testParams() adapterhost.Params {
	return adapterhost.Params{
		NotificationBuffer: 16,
		PingInterval:       10 * time.Second,
		PingTimeout:        3 * time.Second,
		RestartBackoffMin:  time.Second,
		RestartBackoffMax:  8 * time.Second,
		StableAfter:        time.Hour,
		HandshakeTimeout:   5 * time.Second,
		StartWait:          30 * time.Second,
		ShutdownWait:       2 * time.Second,
	}
}

func newRig(t *testing.T, ro rigOptions) *rig {
	t.Helper()
	leakGuard(t)
	r := &rig{
		t:       t,
		clk:     clocktest.NewFake(testStart),
		home:    t.TempDir(),
		eng:     newEngine(),
		logs:    newLogs(t),
		opts:    fakeadapter.DefaultManifestOptions(),
		tl:      &timeline{},
		mem:     ledger.NewMemory(),
		records: &logRecords{},
		knobs:   map[string]*fakeadapter.Knobs{},
	}
	if ro.manifest != nil {
		ro.manifest(&r.opts)
	}
	r.names = ro.instances
	if len(r.names) == 0 {
		r.names = []string{"chat"}
	}
	rt, err := fakeruntime.New(fakeruntime.Config{
		Now: r.clk.Now, Ledgers: r.mem, Dir: t.TempDir(), Seed: 7,
		FirstTurn: fakeruntime.TurnPlan{ManualStart: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.runtime = rt
	t.Cleanup(func() { _ = rt.Close() })

	r.src = &sourceDouble{
		instances: map[string]config.Instance{},
		adapters:  []config.Adapter{{ID: r.opts.ID, Version: r.opts.Version, Manifest: r.opts.Manifest()}},
	}
	for _, n := range r.names {
		r.src.instances[n] = config.Instance{Adapter: r.opts.ID, Enabled: true}
	}
	r.lau = &launcherDouble{}
	r.lau.launch = func(n int, inst adapterhost.Instance) (adapterhost.Process, error) {
		if ro.launch != nil {
			return ro.launch(r, n, inst)
		}
		return r.fakeProcess(inst), nil
	}
	params := testParams()
	if ro.params != (adapterhost.Params{}) {
		params = ro.params
	}
	h, err := adapterhost.New(adapterhost.Config{Launcher: r.lau, Params: params, Clock: r.clk, Log: slog.New(r.records)})
	if err != nil {
		t.Fatal(err)
	}
	r.host = h
	t.Cleanup(r.host.Abort)
	return r
}

// env returns the Env the host is started with.
func (r *rig) env(basic bool) adapterhost.Env {
	var eng adapterhost.Engine = r.eng
	if basic {
		eng = basicEngine{r.eng}
	}
	return adapterhost.Env{Home: r.home, Source: r.src, Logs: r.logs, Engine: eng}
}

// start starts the host and fails the test if Start fails.
func (r *rig) start() {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	if err := r.host.Start(ctx, r.env(false)); err != nil {
		r.t.Fatalf("Start: %v", err)
	}
}

// knobsOf returns the knobs of the fake adapter of an instance, which stay the same
// when the adapter is started again.
func (r *rig) knobsOf(instance string) *fakeadapter.Knobs {
	r.knobsMu.Lock()
	defer r.knobsMu.Unlock()
	k, ok := r.knobs[instance]
	if !ok {
		k = fakeadapter.NewKnobs()
		r.knobs[instance] = k
	}
	return k
}

// fakeProcess starts a fake adapter for the instance of inst, with what the host
// writes to it on the timeline.
func (r *rig) fakeProcess(inst adapterhost.Instance) adapterhost.Process {
	w, err := r.mem.Open("adapter/"+inst.ID, 0, r.clk.Now)
	if err != nil {
		r.t.Fatal(err)
	}
	p := fakeadapter.StartPipe(context.Background(), fakeadapter.Config{
		InstanceID: inst.ID, Now: r.clk.Now, Ledger: w, Runtime: r.runtime,
		Manifest: r.opts, Knobs: r.knobsOf(inst.ID),
	})
	return &tapProcess{Process: p, tl: r.tl}
}

// scriptedProcess starts a scripted adapter. configure runs before it starts.
func (r *rig) scriptedProcess(configure func(*scripted), stubborn bool) (*pipeProcess, *scripted) {
	a := &scripted{t: r.t, opts: r.opts, handlers: map[string]peer.RequestHandler{}}
	if configure != nil {
		configure(a)
	}
	pp := newPipeProcess(a.serve, stubborn)
	r.t.Cleanup(pp.release)
	return pp, a
}

// settle waits until the host has acted on everything that is due for the
// instances: Working is false for each.
func (r *rig) settle(instances ...string) {
	r.t.Helper()
	if len(instances) == 0 {
		instances = r.names
	}
	for _, in := range instances {
		in := in
		eventually(r.t, "the host to be idle ("+in+")", func() bool { return !r.host.Counts(in).Working })
	}
}

// quiet waits until the host is idle with no request of its own out for the
// instance: it has taken every answer it waited for and acted on it, the next
// ping's interval armed included. settle alone would return while an answer is
// still on its way to a host parked with nothing due.
func (r *rig) quiet(instance string) {
	r.t.Helper()
	eventually(r.t, "the host to be quiet ("+instance+")", func() bool {
		c := r.host.Counts(instance)
		return !c.Working && len(c.Calls) == 0
	})
}

// advance moves the test clock and waits for the host to act on it.
func (r *rig) advance(d time.Duration, instances ...string) {
	r.t.Helper()
	r.clk.Advance(d)
	r.settle(instances...)
}

func (r *rig) waitConnected(instance string) {
	r.t.Helper()
	eventually(r.t, instance+" to be in service", func() bool { return r.host.Counts(instance).Connected })
}

// ready waits until the n-th connection of the instance is in service and the host
// has finished taking it on and is parked on its pings: the clock moved before that
// fires none of the timers the host arms afterwards.
func (r *rig) ready(instance string, n int) {
	r.t.Helper()
	eventually(r.t, fmt.Sprintf("connection %d of %s to be taken on", n, instance), func() bool {
		count := 0
		for _, e := range r.eng.of("probed") {
			if e.Instance == instance {
				count++
			}
		}
		return count >= n && r.host.Counts(instance).Connected
	})
	r.settle(instance)
}

func (r *rig) waitDisconnected(instance string) {
	r.t.Helper()
	eventually(r.t, instance+" to be out of service", func() bool { return !r.host.Counts(instance).Connected })
}

func (r *rig) waitEvents(kind string, n int) []event {
	r.t.Helper()
	eventually(r.t, fmt.Sprintf("%d %s events", n, kind), func() bool { return len(r.eng.of(kind)) >= n })
	return r.eng.of(kind)
}

func (r *rig) waitLaunches(n int) {
	r.t.Helper()
	eventually(r.t, fmt.Sprintf("%d launches", n), func() bool { return r.lau.launches() >= n })
}

// goneCauses returns the causes of the gone events of an instance, in order.
func (r *rig) goneCauses(instance string) []adapterhost.GoneCause {
	var out []adapterhost.GoneCause
	for _, e := range r.eng.of("gone") {
		if e.Instance == instance {
			out = append(out, e.Cause)
		}
	}
	return out
}

// barrier makes the adapter send a notification and waits until the engine has
// seen it: the host reads one adapter's lines in order, so what the adapter wrote
// before has been dealt with by the host's peer by then.
func (a *scripted) barrier(eng *engineDouble) {
	a.t.Helper()
	before := len(eng.of("notified"))
	a.notify(adapterproto.MethodAdapterAvailability, adapterproto.AvailabilityParams{Conditions: []adapterproto.Availability{}})
	eventually(a.t, "the barrier notification", func() bool { return len(eng.of("notified")) > before })
}

// ctx returns a context that ends with the test's wait limit.
func timeoutCtx(t testing.TB) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	t.Cleanup(cancel)
	return ctx
}

// exe is the name of the test binary, for the helper process.
func testBinary() string {
	p, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return p
}

// zeroCounts reports whether nothing is counted.
func zeroCounts(c adapterhost.ConnCounts) bool {
	return !c.Connected && c.Handled == 0 && c.Buffered == 0 && c.Requests == 0 && len(c.Calls) == 0 && !c.Working
}
