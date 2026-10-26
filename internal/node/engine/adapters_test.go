package engine_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// What the adapter host asks of the engine for the life of a connection: the
// admission of an instance (and the runtime endpoint it makes), what
// adapter.start carries, the probe tokens, the state an adapter reports, and the
// checkpoints it asks the node to keep.

// hostDouble stands for the adapter host in the tests of the engine: the engine
// asks it only for the counts of a connection, and the test sets them.
type hostDouble struct {
	mu     sync.Mutex
	counts map[string]adapterhost.ConnCounts
	// stopping are the instances whose process is still ending after a Stop.
	stopping map[string]bool
	asked    atomic.Int64
	// calls are the starts and stops the engine asked for, in order, as
	// "run:<instance>" and "stop:<instance>".
	calls []string
}

var _ engine.Adapters = (*hostDouble)(nil)

func newHostDouble() *hostDouble { return &hostDouble{counts: map[string]adapterhost.ConnCounts{}} }

func (h *hostDouble) set(instance string, c adapterhost.ConnCounts) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.counts[instance] = c
}

func (h *hostDouble) Counts(instance string) adapterhost.ConnCounts {
	h.asked.Add(1)
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.counts[instance]
	c.Calls = slices.Clone(c.Calls)
	return c
}

func (h *hostDouble) EnsureRunning(instance string) { h.record("run:" + instance) }
func (h *hostDouble) Stop(instance string)          { h.record("stop:" + instance) }

func (h *hostDouble) Stopping(instance string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stopping[instance]
}

// setStopping says whether the process of the instance is still ending.
func (h *hostDouble) setStopping(instance string, stopping bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopping == nil {
		h.stopping = map[string]bool{}
	}
	h.stopping[instance] = stopping
}

func (h *hostDouble) record(call string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, call)
}

// asks are the starts and stops the engine asked the host for so far.
func (h *hostDouble) asks() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.calls)
}
func (h *hostDouble) CreateSession(context.Context, string, adapterproto.SessionCreateParams) (adapterproto.SessionCreateResult, error) {
	return adapterproto.SessionCreateResult{}, adapterhost.ErrNotConnected
}
func (h *hostDouble) DeliverPrompt(context.Context, string, adapterproto.PromptDeliverParams) (adapterproto.PromptDeliverResult, error) {
	return adapterproto.PromptDeliverResult{}, adapterhost.ErrNotConnected
}
func (h *hostDouble) Reconcile(context.Context, string, adapterproto.ReconcileParams) (adapterproto.ReconcileResult, error) {
	return adapterproto.ReconcileResult{}, adapterhost.ErrNotConnected
}
func (h *hostDouble) Interrupt(context.Context, string, adapterproto.InterruptParams) (adapterproto.InterruptResult, error) {
	return adapterproto.InterruptResult{}, adapterhost.ErrNotConnected
}
func (h *hostDouble) Close(context.Context, string, adapterproto.CloseParams) (adapterproto.CloseResult, error) {
	return adapterproto.CloseResult{}, adapterhost.ErrNotConnected
}
func (h *hostDouble) NotifyPending(string, adapterproto.PendingParams) {}

// runtimeAdmission is the admission of an adapter that declares a runtime with
// the front ends, in that order, and direct endpoints when direct is above zero.
func runtimeAdmission(t testing.TB, direct int, frontends ...adapterproto.Frontend) adapterreg.Admission {
	t.Helper()
	caps := adapterproto.Capabilities{Runtime: &adapterproto.RuntimeCapabilities{Frontends: frontends}}
	if direct > 0 {
		caps.Direct = &adapterproto.DirectCapabilities{MaxEndpoints: direct}
	}
	adm, err := adapterreg.Admit(caps, mustManifest(t))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	return adm
}

var opCounter atomic.Uint32

// nextOp is an operation identifier no other request of the test used.
func nextOp() admin.OperationID {
	var op admin.OperationID
	binary.BigEndian.PutUint32(op[:4], opCounter.Add(1))
	op[15] = 0xAD
	return op
}

func okResult[T any](T) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }

// addPreset adds a preset to the configuration the engine reads.
func (r *rig) addPreset(name, doc string) {
	r.t.Helper()
	req := configRequest(nextOp(), r.cfg.Presets().Version)
	if _, err := r.cfg.AddPreset(req, name, []byte(doc), okResult[config.Presets]); err != nil {
		r.t.Fatalf("AddPreset(%q): %v", name, err)
	}
}

// enableInstance enables a new instance of the installed adapter, with the
// configuration document when it is not empty.
func (r *rig) enableInstance(name, doc string) {
	r.t.Helper()
	var raw []byte
	if doc != "" {
		raw = []byte(doc)
	}
	req := configRequest(nextOp(), r.cfg.Instances().Version)
	if _, err := r.cfg.EnableAdapter(req, name, "chat", raw, nil, okResult[config.Instances]); err != nil {
		r.t.Fatalf("EnableAdapter(%q): %v", name, err)
	}
}

func (r *rig) removePreset(name string) {
	r.t.Helper()
	req := configRequest(nextOp(), r.cfg.Presets().Version)
	if _, err := r.cfg.RemovePreset(req, name, okResult[config.Presets]); err != nil {
		r.t.Fatalf("RemovePreset(%q): %v", name, err)
	}
}

func (r *rig) disableInstance(name string) {
	r.t.Helper()
	req := configRequest(nextOp(), r.cfg.Instances().Version)
	if _, err := r.cfg.DisableAdapter(req, name, nil, okResult[config.Instances]); err != nil {
		r.t.Fatalf("DisableAdapter(%q): %v", name, err)
	}
}

// totalCommits is how many transactions of every kind have reached their commit.
func (r *rig) totalCommits() int {
	n := 0
	for _, c := range r.w.Coverage() {
		if c.Key.Step == fsxCommit {
			n += c.Hits
		}
	}
	return n
}

// runtimeRecords are the runtime endpoint records on the disk.
func (r *rig) runtimeRecords() []store.EndpointRecord {
	var out []store.EndpointRecord
	for _, ep := range r.scan().Endpoints {
		if ep.Kind == adapterreg.KindRuntime {
			out = append(out, ep)
		}
	}
	return out
}

func mustRef(t testing.TB, ref adapterproto.EndpointRef) ident.EndpointID {
	t.Helper()
	id, err := ident.ParseEndpointID(string(ref))
	if err != nil {
		t.Fatalf("endpoint ref %q: %v", ref, err)
	}
	return id
}

var ctx = context.Background()

const (
	fsxCommit  = fsx.StepCommit
	fsxWrite   = fsx.StepWrite
	fsxSyncDir = fsx.StepSyncDir
)

var (
	errIO       error = syscall.EIO
	errDiskFull       = fsxtest.DiskFull
)

func tokenText(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// ---------------------------------------------------------------------------
// The runtime endpoint

func TestAdmissionOfARuntimeAdapterMakesItsRuntimeEndpointOnceAndKeepsItsId(t *testing.T) {
	noLeaks(t)
	rt := runtimeAdmission(t, 0, adapterproto.FrontendCLI)

	t.Run("a new runtime endpoint is open with a token, and its id is kept in config", func(t *testing.T) {
		r := newRig(t, 10)
		e := r.start()
		got, err := admitFull(ctx, e, "chat", rt)
		if err != nil {
			t.Fatal(err)
		}
		if got.RuntimeEndpoint == "" {
			t.Fatal("adapter.start names no runtime endpoint for an adapter that declared a runtime")
		}
		id := mustRef(t, got.RuntimeEndpoint)
		recs := r.runtimeRecords()
		if len(recs) != 1 {
			t.Fatalf("%d runtime records on disk, want 1", len(recs))
		}
		rec := recs[0]
		if rec.ID != id || rec.Instance != "chat" || rec.Status != store.EndpointOpen || len(rec.Token) != store.TokenLen {
			t.Errorf("record %+v, want an open runtime endpoint %s of chat with a %d-byte token", rec, id, store.TokenLen)
		}
		if string(rec.Declaration) != `{}` {
			t.Errorf("declaration %q, want the encoded empty declaration {}", rec.Declaration)
		}
		if kept := r.cfg.EndpointIDs("chat").Runtime; kept != id {
			t.Errorf("id kept in config/ is %s, want %s", kept, id)
		}
		if n := r.commits("runtime-admit"); n != 1 {
			t.Errorf("%d commits of runtime-admit, want 1", n)
		}
	})

	t.Run("an open endpoint is not written again and the id is the same after a restart", func(t *testing.T) {
		r := newRig(t, 10)
		e := r.start()
		first, err := admitFull(ctx, e, "chat", rt)
		if err != nil {
			t.Fatal(err)
		}
		token := r.runtimeRecords()[0].Token
		again, err := admitFull(ctx, e, "chat", rt)
		if err != nil {
			t.Fatal(err)
		}
		if again.RuntimeEndpoint != first.RuntimeEndpoint {
			t.Errorf("a second admission gave %s, want %s", again.RuntimeEndpoint, first.RuntimeEndpoint)
		}
		if n := r.commits("runtime-admit"); n != 1 {
			t.Errorf("%d commits of runtime-admit after two admissions, want 1: an open endpoint is not written", n)
		}
		r.restart()
		e = r.start()
		third, err := admitFull(ctx, e, "chat", rt)
		if err != nil {
			t.Fatal(err)
		}
		if third.RuntimeEndpoint != first.RuntimeEndpoint {
			t.Errorf("after a restart the id is %s, want %s", third.RuntimeEndpoint, first.RuntimeEndpoint)
		}
		if n := r.commits("runtime-admit"); n > 0 {
			t.Errorf("%d commits of runtime-admit after a restart with an open record, want none", n)
		}
		if got := r.runtimeRecords()[0].Token; !bytes.Equal(got, token) {
			t.Error("the token of an open runtime endpoint changed")
		}
	})

	t.Run("a closed endpoint is opened again with a new token and takes a place", func(t *testing.T) {
		r := newRig(t, 10)
		e := r.start()
		first, err := admitFull(ctx, e, "chat", rt)
		if err != nil {
			t.Fatal(err)
		}
		id := mustRef(t, first.RuntimeEndpoint)
		old := r.runtimeRecords()[0]
		r.restart()
		closed := old
		closed.Status, closed.Token = store.EndpointClosed, nil
		r.seedRecord(closed)
		e = r.start()
		if n := openEndpointsOf(t, e); n != 0 {
			t.Fatalf("%d endpoints open with the runtime endpoint closed, want 0", n)
		}
		again, err := admitFull(ctx, e, "chat", rt)
		if err != nil {
			t.Fatal(err)
		}
		if again.RuntimeEndpoint != first.RuntimeEndpoint {
			t.Errorf("the reopened endpoint is %s, want the same id %s", again.RuntimeEndpoint, id)
		}
		rec := r.runtimeRecords()[0]
		if rec.Status != store.EndpointOpen || len(rec.Token) != store.TokenLen || bytes.Equal(rec.Token, old.Token) {
			t.Errorf("reopened record %+v: want open with a token that is not the old one", rec)
		}
		if n := r.commits("runtime-admit"); n != 1 {
			t.Errorf("%d commits of runtime-admit, want 1", n)
		}
		if n := openEndpointsOf(t, e); n != 1 {
			t.Errorf("%d endpoints open after the reopening, want 1", n)
		}
	})

	t.Run("an adapter that declares direct endpoints only makes no runtime endpoint", func(t *testing.T) {
		r := newRig(t, 10)
		e := r.start()
		got, err := admitFull(ctx, e, "chat", r.admit)
		if err != nil {
			t.Fatal(err)
		}
		if got.RuntimeEndpoint != "" {
			t.Errorf("runtime endpoint %q for a direct-only adapter", got.RuntimeEndpoint)
		}
		if n := len(r.runtimeRecords()); n != 0 {
			t.Errorf("%d runtime records, want none", n)
		}
		if n := r.commits("runtime-admit"); n > 0 {
			t.Errorf("%d commits of runtime-admit for a direct-only adapter", n)
		}
		if id := r.cfg.EndpointIDs("chat").Runtime; !id.IsZero() {
			t.Errorf("a runtime id %s was minted for a direct-only adapter", id)
		}
	})

	t.Run("a runtime endpoint is no caller: its token opens nothing and is never handed to the adapter", func(t *testing.T) {
		r := newRig(t, 10)
		e := r.start()
		got, err := admitFull(ctx, e, "chat", rt)
		if err != nil {
			t.Fatal(err)
		}
		token := r.runtimeRecords()[0].Token
		text, _ := json.Marshal(got)
		if bytes.Contains(text, token) || strings.Contains(string(text), tokenText(token)) {
			t.Error("adapter.start carries the token of the runtime endpoint")
		}
		_, err = e.Declare(ctx, engine.ByToken(adapterproto.Token(tokenText(token))), engine.DeclarationPatch{Paused: ptr(true)})
		wantReason(t, err, engine.ReasonUnauthenticated)
	})

	t.Run("a write that fails before its commit point is back pressure and the instance is not admitted", func(t *testing.T) {
		r := newRig(t, 10)
		r.enableInstance("agent", "")
		e := r.start()
		rule := r.faulty.Fail(matchT{Tx: "runtime-admit", Role: "ids", Step: fsxWrite, Hit: 1}, errIO)
		_, err := admitFull(ctx, e, "agent", rt)
		wantReason(t, err, engine.ReasonStorageUnavailable)
		if rule.Hits() == 0 {
			t.Fatal("the fault was not injected")
		}
		if n := len(r.runtimeRecords()); n != 0 {
			t.Errorf("%d runtime records after a failed admission", n)
		}
		v, err := e.Adapter(ctx, "agent")
		if err != nil || v.Admitted {
			t.Errorf("Adapter = %+v, %v: the instance must not be admitted", v, err)
		}
		if _, err := admitFull(ctx, e, "agent", rt); err != nil {
			t.Errorf("the admission is not possible again after the fault: %v", err)
		}
	})

	t.Run("an instance that the configuration does not enable is refused", func(t *testing.T) {
		r := newRig(t, 10)
		e := r.start()
		_, err := admitFull(ctx, e, "nobody", rt)
		wantReason(t, err, engine.ReasonInstanceUnknown)
		if n := len(r.runtimeRecords()); n != 0 {
			t.Errorf("%d runtime records for an instance that is not enabled", n)
		}
	})
}

func openEndpointsOf(t testing.TB, e *engine.Engine) int { return openEndpoints(t, e) }

func TestRuntimeEndpointCountsAgainstTheNodeCap(t *testing.T) {
	noLeaks(t)
	rt := runtimeAdmission(t, 300, adapterproto.FrontendCLI)
	r := newRig(t, 300)
	r.enableInstance("agent", "")
	r.enableInstance("agent2", "")
	e := r.start()
	for i := 0; i < 255; i++ {
		register(t, e, keyName(i))
	}
	got, err := admitFull(ctx, e, "agent", rt)
	if err != nil {
		t.Fatalf("the 256th endpoint, a runtime endpoint, was refused: %v", err)
	}
	if n := openEndpoints(t, e); n != 256 {
		t.Fatalf("%d endpoints open, want 256", n)
	}
	_, err = e.RegisterDirect(ctx, "chat", registerParams("one-too-many"))
	if ref := wantReason(t, err, engine.ReasonEndpointLimit); ref.Limit != engine.LimitNode {
		t.Errorf("limit %v, want the node's", ref.Limit)
	}

	// The runtime endpoint is closed (as the end of the instance's life would
	// leave it), a direct endpoint takes its place, and the reopening is then a
	// new place in the cap and is refused.
	old := r.runtimeRecords()[0]
	r.restart()
	closed := old
	closed.Status, closed.Token = store.EndpointClosed, nil
	r.seedRecord(closed)
	e = r.start()
	if err := admitOn(ctx, e, "chat", r.admit); err != nil { // start admitted it already; a no-op
		t.Fatal(err)
	}
	register(t, e, "fills-the-place")
	if n := openEndpoints(t, e); n != 256 {
		t.Fatalf("%d endpoints open, want 256", n)
	}
	_, err = admitFull(ctx, e, "agent", rt)
	if ref := wantReason(t, err, engine.ReasonEndpointLimit); ref.Limit != engine.LimitNode {
		t.Errorf("reopening with the cap full: limit %v, want the node's", ref.Limit)
	}
	if rec := r.runtimeRecords()[0]; rec.Status != store.EndpointClosed {
		t.Errorf("the record is %v after a refused reopening, want it left closed", rec.Status)
	}
	if v, err := e.Adapter(ctx, "agent"); err != nil || v.Admitted {
		t.Errorf("Adapter(agent) = %+v, %v: an instance refused for the cap is not admitted", v, err)
	}

	// A new runtime endpoint is refused the same way, and no id is kept for it.
	_, err = admitFull(ctx, e, "agent2", rt)
	if ref := wantReason(t, err, engine.ReasonEndpointLimit); ref.Limit != engine.LimitNode {
		t.Errorf("new runtime endpoint with the cap full: limit %v, want the node's", ref.Limit)
	}
	if id := r.cfg.EndpointIDs("agent2").Runtime; !id.IsZero() {
		t.Errorf("an id %s was kept for an endpoint that was refused", id)
	}
	if v, err := e.Adapter(ctx, "agent2"); err != nil || v.Admitted {
		t.Errorf("Adapter(agent2) = %+v, %v: want not admitted", v, err)
	}

	// A place that is freed lets the admission through, with a new token.
	if err := e.UnregisterDirect(ctx, "chat", adapterproto.UnregisterParams{Endpoint: adapterproto.EndpointRef(register(t, e, "fills-the-place").String())}); err != nil {
		t.Fatal(err)
	}
	again, err := admitFull(ctx, e, "agent", rt)
	if err != nil {
		t.Fatalf("admission with a place free: %v", err)
	}
	if again.RuntimeEndpoint != got.RuntimeEndpoint {
		t.Errorf("the reopened endpoint is %s, want %s", again.RuntimeEndpoint, got.RuntimeEndpoint)
	}
	if v, err := e.Adapter(ctx, "agent"); err != nil || !v.Admitted {
		t.Errorf("Adapter(agent) = %+v, %v: want admitted", v, err)
	}
}

// ---------------------------------------------------------------------------
// What adapter.start carries

func rawEq(t testing.TB, what string, got, want map[string]string) {
	t.Helper()
	norm := func(m map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range m {
			var b bytes.Buffer
			if err := json.Compact(&b, []byte(v)); err != nil {
				t.Fatalf("%s: %q is not JSON: %v", what, v, err)
			}
			out[k] = b.String()
		}
		return out
	}
	if g, w := norm(got), norm(want); fmt.Sprint(g) != fmt.Sprint(w) {
		t.Errorf("%s = %v, want %v", what, g, w)
	}
}

func asStrings(v adapterproto.ConfigValues) map[string]string {
	out := map[string]string{}
	for k, raw := range v {
		out[k] = string(raw)
	}
	return out
}

func TestAdmissionReturnsWhatAdapterStartCarries(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	r.enableInstance("agent", `{"base_url":"https://api.example.test","retries":3,"debug":true,"tags":["a","b"]}`)
	r.addPreset("plain", `{"local_sources":"deny"}`)
	r.addPreset("fast", `{"local_sources":"allow","config":{"chat":{"model":"small","retries":2}}}`)
	r.addPreset("deep", `{"local_sources":"deny","config":{"chat":{"effort":"high"},"other":{"x":"not for chat"}}}`)
	e := r.start()
	rt := runtimeAdmission(t, 10, adapterproto.FrontendCLI)

	// The first connection of "agent": nothing registered yet.
	first, err := admitFull(ctx, e, "agent", rt)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"zeta", "alpha", "mid"} {
		if _, err := e.RegisterDirect(ctx, "agent", adapterproto.RegisterParams{Key: key, DisplayName: key}); err != nil {
			t.Fatal(err)
		}
	}
	mid := r.cfg.EndpointIDs("agent").Direct["mid"]
	if err := e.UnregisterDirect(ctx, "agent", adapterproto.UnregisterParams{Endpoint: adapterproto.EndpointRef(mid.String())}); err != nil {
		t.Fatal(err)
	}
	if first.DirectEndpoints == nil || len(first.DirectEndpoints) != 0 {
		t.Errorf("direct endpoints of the first connection = %#v, want an empty list that is not nil", first.DirectEndpoints)
	}

	// The second connection carries everything.
	got, err := admitFull(ctx, e, "agent", rt)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("StartParams.Validate: %v", err)
	}
	ids := r.cfg.EndpointIDs("agent")
	if got.Node != r.node.String() {
		t.Errorf("Node = %q, want the node id %q", got.Node, r.node.String())
	}
	if got.RuntimeEndpoint != adapterproto.EndpointRef(ids.Runtime.String()) || ids.Runtime.IsZero() {
		t.Errorf("RuntimeEndpoint = %q, want the runtime id %q", got.RuntimeEndpoint, ids.Runtime)
	}
	rawEq(t, "Config", asStrings(got.Config), map[string]string{
		"base_url": `"https://api.example.test"`, "retries": `3`, "debug": `true`, "tags": `["a","b"]`,
	})
	wantDirect := []adapterproto.DirectAttach{
		{Endpoint: adapterproto.EndpointRef(ids.Direct["alpha"].String()), Key: "alpha"},
		{Endpoint: adapterproto.EndpointRef(ids.Direct["zeta"].String()), Key: "zeta"},
	}
	if !slices.Equal(got.DirectEndpoints, wantDirect) {
		t.Errorf("DirectEndpoints = %v, want the open ones sorted by key: %v (the closed one, mid, is left out)", got.DirectEndpoints, wantDirect)
	}
	if got.Sessions == nil || len(got.Sessions) != 0 {
		t.Errorf("Sessions = %#v, want an empty list that is not nil", got.Sessions)
	}
	wantPresets := []struct {
		name   string
		values map[string]string
	}{
		{"deep", map[string]string{"effort": `"high"`}},
		{"default", map[string]string{}}, // the preset every node starts with
		{"fast", map[string]string{"model": `"small"`, "retries": `2`}},
		{"plain", map[string]string{}},
	}
	if len(got.Presets) != len(wantPresets) {
		t.Fatalf("%d presets, want %d: %+v", len(got.Presets), len(wantPresets), got.Presets)
	}
	for i, w := range wantPresets {
		p := got.Presets[i]
		if p.Name != w.name {
			t.Errorf("preset %d is %q, want %q (sorted by name)", i, p.Name, w.name)
		}
		if p.Config == nil {
			t.Errorf("preset %q has no config map, want one that is not nil", p.Name)
		}
		rawEq(t, "preset "+w.name, asStrings(p.Config), w.values)
	}

	// An adapter that declares direct endpoints only: no runtime endpoint, no
	// presets (they are the runtime's), its own configuration and endpoints.
	direct, err := admitFull(ctx, e, "chat", r.admit)
	if err != nil {
		t.Fatal(err)
	}
	if err := direct.Validate(); err != nil {
		t.Errorf("StartParams.Validate for a direct-only adapter: %v", err)
	}
	if direct.RuntimeEndpoint != "" {
		t.Errorf("RuntimeEndpoint = %q for a direct-only adapter", direct.RuntimeEndpoint)
	}
	if direct.Presets == nil || len(direct.Presets) != 0 {
		t.Errorf("Presets = %#v for a direct-only adapter, want an empty list that is not nil even though presets exist", direct.Presets)
	}
	if direct.Config == nil || len(direct.Config) != 0 {
		t.Errorf("Config = %#v for an instance with no configuration, want an empty map that is not nil", direct.Config)
	}
	if direct.DirectEndpoints == nil || len(direct.DirectEndpoints) != 0 {
		t.Errorf("DirectEndpoints = %#v, want an empty list that is not nil", direct.DirectEndpoints)
	}
}

// ---------------------------------------------------------------------------
// Probe tokens

func TestProbeTokensAreOnePerPresetAndFrontEndSingleUseAndFiveMinutes(t *testing.T) {
	noLeaks(t)
	rt := runtimeAdmission(t, 0, adapterproto.FrontendMCP, adapterproto.FrontendCLI)

	t.Run("one request per preset and front end, presets by name, front ends as declared", func(t *testing.T) {
		r := newRig(t, 10)
		r.addPreset("zz", `{"local_sources":"allow"}`)
		r.addPreset("aa", `{"local_sources":"deny"}`)
		e := r.start()
		if err := admitOn(ctx, e, "chat", rt); err != nil {
			t.Fatal(err)
		}
		r.clk.Advance(2 * time.Minute)
		reqs, err := e.ProbeTokens(ctx, "chat")
		if err != nil {
			t.Fatal(err)
		}
		type pair struct {
			preset string
			front  adapterproto.Frontend
		}
		want := []pair{{"aa", "mcp"}, {"aa", "cli"}, {"default", "mcp"}, {"default", "cli"}, {"zz", "mcp"}, {"zz", "cli"}}
		if len(reqs) != len(want) {
			t.Fatalf("%d requests, want %d: %+v", len(reqs), len(want), reqs)
		}
		seen := map[adapterproto.Token]bool{}
		for i, q := range reqs {
			if q.Preset != want[i].preset || q.Frontend != want[i].front {
				t.Errorf("request %d is (%s, %s), want (%s, %s)", i, q.Preset, q.Frontend, want[i].preset, want[i].front)
			}
			if err := q.Validate(); err != nil {
				t.Errorf("request %d does not validate: %v", i, err)
			}
			if seen[q.Token] {
				t.Errorf("request %d repeats a token", i)
			}
			seen[q.Token] = true
			wantEnv := map[string]string{"SIDERA_PROBE_TOKEN": string(q.Token), "SIDERA_HOME": testHome}
			if fmt.Sprint(q.Env) != fmt.Sprint(wantEnv) {
				t.Errorf("request %d env = %v, want %v", i, q.Env, wantEnv)
			}
		}
		if err := (adapterproto.ProbeParams{ToolProbes: reqs}).Validate(); err != nil {
			t.Errorf("the requests are not valid adapter.probe parameters: %v", err)
		}

		// Valid for five minutes on the injected clock, from the moment of issue.
		v, err := e.Adapter(ctx, "chat")
		if err != nil {
			t.Fatal(err)
		}
		if len(v.ProbeTokens) != len(want) {
			t.Fatalf("the engine holds %d probe tokens, want %d", len(v.ProbeTokens), len(want))
		}
		for i, p := range v.ProbeTokens {
			if p.Preset != want[i].preset || p.Frontend != want[i].front || !p.ExpiresAt.Equal(t0.Add(7*time.Minute)) {
				t.Errorf("probe token %d = %+v, want (%s, %s) expiring at %v", i, p, want[i].preset, want[i].front, t0.Add(7*time.Minute))
			}
		}
		if engine.ProbeTokenLifetime != 5*time.Minute {
			t.Errorf("ProbeTokenLifetime = %v, want 5 minutes", engine.ProbeTokenLifetime)
		}
		if engine.ProbeTokenEnv != "SIDERA_PROBE_TOKEN" || engine.HomeEnv != "SIDERA_HOME" {
			t.Errorf("env names %q and %q", engine.ProbeTokenEnv, engine.HomeEnv)
		}

		// Tokens are fresh at every call, are kept in memory only and are never
		// logged.
		again, err := e.ProbeTokens(ctx, "chat")
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range again {
			if seen[q.Token] {
				t.Error("a second call gave a token the first call gave")
			}
		}
		settle(t, e)
		for _, q := range append(reqs, again...) {
			for name, content := range r.snapshot() {
				if strings.Contains(content, string(q.Token)) {
					t.Errorf("a probe token is on disk in %s", name)
				}
			}
			for _, m := range r.log.Messages() {
				if strings.Contains(m, string(q.Token)) {
					t.Error("a probe token was logged")
				}
			}
		}
	})

	t.Run("a direct-only adapter gets an empty list that is not nil", func(t *testing.T) {
		r := newRig(t, 10)
		r.addPreset("aa", `{"local_sources":"allow"}`)
		e := r.start()
		reqs, err := e.ProbeTokens(ctx, "chat")
		if err != nil {
			t.Fatal(err)
		}
		if reqs == nil || len(reqs) != 0 {
			t.Errorf("requests = %#v, want an empty list that is not nil", reqs)
		}
	})

	t.Run("a runtime with no preset gets an empty list that is not nil", func(t *testing.T) {
		r := newRig(t, 10)
		r.removePreset("default")
		e := r.start()
		if err := admitOn(ctx, e, "chat", rt); err != nil {
			t.Fatal(err)
		}
		reqs, err := e.ProbeTokens(ctx, "chat")
		if err != nil {
			t.Fatal(err)
		}
		if reqs == nil || len(reqs) != 0 {
			t.Errorf("requests = %#v, want an empty list that is not nil", reqs)
		}
	})

	t.Run("an instance that was not admitted is unknown", func(t *testing.T) {
		r := newRig(t, 10)
		r.enableInstance("agent", "")
		e := r.start()
		_, err := e.ProbeTokens(ctx, "agent")
		wantReason(t, err, engine.ReasonInstanceUnknown)
		_, err = e.ProbeTokens(ctx, "nobody")
		wantReason(t, err, engine.ReasonInstanceUnknown)
	})

	t.Run("without a data directory the tokens cannot be made and the error says why", func(t *testing.T) {
		r := newRig(t, 10, withoutHome())
		r.addPreset("aa", `{"local_sources":"allow"}`)
		e := r.start()
		if err := admitOn(ctx, e, "chat", rt); err != nil {
			t.Fatal(err)
		}
		_, err := e.ProbeTokens(ctx, "chat")
		if err == nil || !strings.Contains(err.Error(), "Home") {
			t.Errorf("error %v, want one that names Deps.Home", err)
		}
		if v, _ := e.Adapter(ctx, "chat"); len(v.ProbeTokens) != 0 {
			t.Errorf("%d probe tokens kept after a failed call", len(v.ProbeTokens))
		}
	})

	t.Run("at most 256 requests", func(t *testing.T) {
		r := newRig(t, 10)
		for i := 0; i < 127; i++ { // with the preset every node starts with, 128
			r.addPreset(fmt.Sprintf("p%03d", i), `{"local_sources":"allow"}`)
		}
		e := r.start()
		if err := admitOn(ctx, e, "chat", rt); err != nil {
			t.Fatal(err)
		}
		reqs, err := e.ProbeTokens(ctx, "chat")
		if err != nil || len(reqs) != 256 {
			t.Fatalf("%d requests, %v; want exactly 256 to be allowed", len(reqs), err)
		}
		r.addPreset("p128", `{"local_sources":"allow"}`)
		_, err = e.ProbeTokens(ctx, "chat")
		if err == nil || !strings.Contains(err.Error(), "256") {
			t.Errorf("258 requests: error %v, want one that says at most 256", err)
		}
	})
}

// ---------------------------------------------------------------------------
// The tested runtime versions

func probeResult(name, version string) adapterproto.ProbeResult {
	r := adapterproto.ProbeResult{
		Auth: adapterproto.AuthValid, Dependencies: []adapterproto.Dependency{},
		Presets: []adapterproto.PresetStatus{}, ToolProbes: []adapterproto.ToolProbeResult{},
	}
	if name != "" {
		r.Runtime = &adapterproto.RuntimeInfo{Name: name, Version: version}
	}
	return r
}

func testedRanges(rs ...manifest.TestedRange) func([]config.Adapter) []config.Adapter {
	return func(in []config.Adapter) []config.Adapter {
		out := slices.Clone(in)
		for i := range out {
			out[i].Manifest.Tested = rs
		}
		return out
	}
}

func versionUntestedAlerts(t testing.TB, e *engine.Engine) []store.AlertRecord {
	t.Helper()
	all, err := e.Alerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AlertRecord
	for _, a := range all {
		if a.Kind == "version_untested" {
			out = append(out, a)
		}
	}
	return out
}

func TestVersionUntestedAlertIsRaisedOnceForARuntimeOutsideTheTestedRanges(t *testing.T) {
	noLeaks(t)
	rt := runtimeAdmission(t, 0, adapterproto.FrontendCLI)
	tested := withInstalled(testedRanges(manifest.TestedRange{Runtime: "codex", Min: "1.2.0", Max: "1.4.0"}))
	const outsideDetail = "runtime codex 1.5.0 is outside the tested ranges of adapter chat 1.0.0"

	for _, tc := range []struct {
		name      string
		opts      []rigOption
		result    adapterproto.ProbeResult
		wantAlert string // the detail, empty for none
		untested  bool
	}{
		{"a version above the tested range", []rigOption{tested}, probeResult("codex", "1.5.0"), outsideDetail, true},
		{"a version below the tested range", []rigOption{tested}, probeResult("codex", "1.1.9"), "runtime codex 1.1.9 is outside the tested ranges of adapter chat 1.0.0", true},
		{"the lowest tested version", []rigOption{tested}, probeResult("codex", "1.2.0"), "", false},
		{"the highest tested version", []rigOption{tested}, probeResult("codex", "1.4.0"), "", false},
		{"a version inside the range", []rigOption{tested}, probeResult("codex", "1.3.0"), "", false},
		{"another runtime with a version inside the range", []rigOption{tested}, probeResult("other", "1.3.0"), "runtime other 1.3.0 is outside the tested ranges of adapter chat 1.0.0", true},
		{"no runtime in the result makes no claim", []rigOption{tested}, probeResult("", ""), "", false},
		{"a manifest without tested ranges contains no version", nil, probeResult("codex", "1.3.0"), "runtime codex 1.3.0 is outside the tested ranges of adapter chat 1.0.0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, 10, tc.opts...)
			e := r.start()
			if err := admitOn(ctx, e, "chat", rt); err != nil {
				t.Fatal(err)
			}
			if err := e.AdapterProbed(ctx, "chat", tc.result); err != nil {
				t.Fatalf("AdapterProbed: %v (the adapter keeps running)", err)
			}
			alerts := versionUntestedAlerts(t, e)
			if tc.wantAlert == "" {
				if len(alerts) != 0 {
					t.Errorf("alerts %+v, want none", alerts)
				}
			} else {
				if len(alerts) != 1 {
					t.Fatalf("%d version_untested alerts, want 1: %+v", len(alerts), alerts)
				}
				if a := alerts[0]; a.Subject != "chat" || a.Detail != tc.wantAlert {
					t.Errorf("alert subject %q detail %q, want subject chat and detail %q", a.Subject, a.Detail, tc.wantAlert)
				}
			}
			v, err := e.Adapter(ctx, "chat")
			if err != nil {
				t.Fatal(err)
			}
			if !v.Probed || v.VersionUntested != tc.untested {
				t.Errorf("view: probed %v, untested %v; want true, %v", v.Probed, v.VersionUntested, tc.untested)
			}
			if (v.Runtime == nil) != (tc.result.Runtime == nil) || (v.Runtime != nil && *v.Runtime != *tc.result.Runtime) {
				t.Errorf("view runtime %+v, want %+v", v.Runtime, tc.result.Runtime)
			}
		})
	}

	t.Run("the same fact is raised once, on this engine and after a restart, and a new fact is raised again", func(t *testing.T) {
		r := newRig(t, 10, tested)
		e := r.start()
		if err := admitOn(ctx, e, "chat", rt); err != nil {
			t.Fatal(err)
		}
		probe := func(version string) {
			t.Helper()
			if err := e.AdapterProbed(ctx, "chat", probeResult("codex", version)); err != nil {
				t.Fatal(err)
			}
		}
		probe("1.5.0")
		probe("1.5.0")
		if n := len(versionUntestedAlerts(t, e)); n != 1 {
			t.Fatalf("%d alerts after probing twice, want 1", n)
		}
		r.restart()
		e = r.start()
		if err := admitOn(ctx, e, "chat", rt); err != nil {
			t.Fatal(err)
		}
		probe("1.5.0")
		if n := len(versionUntestedAlerts(t, e)); n != 1 {
			t.Fatalf("%d alerts after a restart and the same probe, want 1: alerts are persistent", n)
		}
		probe("1.6.0")
		alerts := versionUntestedAlerts(t, e)
		if len(alerts) != 2 {
			t.Fatalf("%d alerts after a probe of another version, want 2", len(alerts))
		}
		if alerts[1].Detail != "runtime codex 1.6.0 is outside the tested ranges of adapter chat 1.0.0" {
			t.Errorf("second alert detail %q", alerts[1].Detail)
		}
		probe("1.3.0")
		v, _ := e.Adapter(ctx, "chat")
		if v.VersionUntested {
			t.Error("a later probe inside the range still says untested")
		}
	})

	t.Run("an instance that was not admitted is unknown", func(t *testing.T) {
		r := newRig(t, 10, tested)
		r.enableInstance("agent", "")
		e := r.start()
		wantReason(t, e.AdapterProbed(ctx, "agent", probeResult("codex", "1.5.0")), engine.ReasonInstanceUnknown)
		if n := len(versionUntestedAlerts(t, e)); n != 0 {
			t.Errorf("%d alerts for an instance that is not admitted", n)
		}
	})
}

// ---------------------------------------------------------------------------
// What an adapter reports becomes handoff's signals

func started(t testing.TB, e *engine.Engine, instance string) {
	t.Helper()
	if err := e.AdapterStarted(ctx, instance, adapterproto.StartResult{Sessions: []adapterproto.SessionState{}}); err != nil {
		t.Fatalf("AdapterStarted(%s): %v", instance, err)
	}
}

func gone(t testing.TB, e *engine.Engine, instance string) {
	t.Helper()
	if err := e.AdapterGone(ctx, instance, adapterhost.GoneConnectionEnded); err != nil {
		t.Fatalf("AdapterGone(%s): %v", instance, err)
	}
}

func available(t testing.TB, e *engine.Engine, instance string, cs ...adapterproto.Availability) {
	t.Helper()
	if cs == nil {
		cs = []adapterproto.Availability{}
	}
	n := adapterhost.Notification{Method: adapterproto.MethodAdapterAvailability, Params: adapterproto.AvailabilityParams{Conditions: cs}}
	if err := e.AdapterNotified(ctx, instance, n); err != nil {
		t.Fatalf("AdapterNotified(availability): %v", err)
	}
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func TestAvailabilitySnapshotsBecomeHandoffSignals(t *testing.T) {
	noLeaks(t)
	const ep = adapterproto.EndpointRef("ep-a")
	auth := adapterproto.ConditionRuntimeAuth
	native := adapterproto.ConditionNativeUnavailable
	dep := adapterproto.ConditionDependencyMissing

	for _, tc := range []struct {
		name     string
		instance string
		endpoint adapterproto.EndpointRef
		setup    func(t *testing.T, r *rig, e *engine.Engine)
		want     handoff.Signals
	}{
		{"an enabled instance that never connected is offline with an unknown start", "agent", ep,
			func(*testing.T, *rig, *engine.Engine) {}, handoff.Signals{AdapterOffline: true}},
		{"an admitted instance that has not answered adapter.start is offline with an unknown start", "chat", ep,
			func(*testing.T, *rig, *engine.Engine) {}, handoff.Signals{AdapterOffline: true}},
		{"a started instance with no snapshot has no signal", "chat", ep,
			func(t *testing.T, _ *rig, e *engine.Engine) { started(t, e, "chat") }, handoff.Signals{}},
		{"runtime_auth for every endpoint is invalid authentication", "chat", ep,
			func(t *testing.T, _ *rig, e *engine.Engine) {
				started(t, e, "chat")
				available(t, e, "chat", adapterproto.Availability{Condition: auth})
			}, handoff.Signals{AuthInvalid: true}},
		{"native_unavailable for the endpoint asked about", "chat", ep,
			func(t *testing.T, _ *rig, e *engine.Engine) {
				started(t, e, "chat")
				available(t, e, "chat", adapterproto.Availability{Condition: native, Endpoints: []adapterproto.EndpointRef{ep}})
			}, handoff.Signals{ServiceUnavailable: true}},
		{"a condition of another endpoint says nothing about this one", "chat", ep,
			func(t *testing.T, _ *rig, e *engine.Engine) {
				started(t, e, "chat")
				available(t, e, "chat", adapterproto.Availability{Condition: native, Endpoints: []adapterproto.EndpointRef{"ep-b"}})
			}, handoff.Signals{}},
		{"dependency_missing is service unavailable", "chat", ep,
			func(t *testing.T, _ *rig, e *engine.Engine) {
				started(t, e, "chat")
				available(t, e, "chat", adapterproto.Availability{Condition: dep})
			}, handoff.Signals{ServiceUnavailable: true}},
		{"a start before the moment of receipt is accepted", "chat", ep,
			func(t *testing.T, r *rig, e *engine.Engine) {
				started(t, e, "chat")
				r.clk.Advance(10 * time.Minute)
				available(t, e, "chat", adapterproto.Availability{Condition: auth, SinceMS: ms(t0.Add(5 * time.Minute))})
			}, handoff.Signals{AuthInvalid: true, Since: t0.Add(5 * time.Minute)}},
		{"a start equal to the moment of receipt is accepted", "chat", ep,
			func(t *testing.T, r *rig, e *engine.Engine) {
				started(t, e, "chat")
				r.clk.Advance(10 * time.Minute)
				available(t, e, "chat", adapterproto.Availability{Condition: auth, SinceMS: ms(t0.Add(10 * time.Minute))})
			}, handoff.Signals{AuthInvalid: true, Since: t0.Add(10 * time.Minute)}},
		{"a start after the moment of receipt is unknown", "chat", ep,
			func(t *testing.T, r *rig, e *engine.Engine) {
				started(t, e, "chat")
				r.clk.Advance(10 * time.Minute)
				available(t, e, "chat", adapterproto.Availability{Condition: auth, SinceMS: ms(t0.Add(10*time.Minute)) + 1})
			}, handoff.Signals{AuthInvalid: true}},
		{"authentication wins the start, and it is the earliest start of its conditions", "chat", ep,
			func(t *testing.T, _ *rig, e *engine.Engine) {
				started(t, e, "chat")
				available(t, e, "chat",
					adapterproto.Availability{Condition: auth, SinceMS: ms(t0.Add(-time.Hour))},
					adapterproto.Availability{Condition: auth, SinceMS: ms(t0.Add(-30 * time.Minute))},
					adapterproto.Availability{Condition: native, SinceMS: ms(t0.Add(-3 * time.Hour))})
			}, handoff.Signals{AuthInvalid: true, ServiceUnavailable: true, Since: t0.Add(-time.Hour)}},
		{"one condition without a start makes the start unknown", "chat", ep,
			func(t *testing.T, _ *rig, e *engine.Engine) {
				started(t, e, "chat")
				available(t, e, "chat",
					adapterproto.Availability{Condition: auth, SinceMS: ms(t0.Add(-time.Hour))},
					adapterproto.Availability{Condition: auth})
			}, handoff.Signals{AuthInvalid: true}},
		{"an empty snapshot clears the earlier one", "chat", ep,
			func(t *testing.T, _ *rig, e *engine.Engine) {
				started(t, e, "chat")
				available(t, e, "chat", adapterproto.Availability{Condition: auth})
				available(t, e, "chat")
			}, handoff.Signals{}},
		{"a later snapshot replaces the earlier one", "chat", ep,
			func(t *testing.T, _ *rig, e *engine.Engine) {
				started(t, e, "chat")
				available(t, e, "chat", adapterproto.Availability{Condition: auth})
				available(t, e, "chat", adapterproto.Availability{Condition: native})
			}, handoff.Signals{ServiceUnavailable: true}},
		{"an instance that is gone is offline since the clock of the report", "chat", ep,
			func(t *testing.T, r *rig, e *engine.Engine) {
				started(t, e, "chat")
				available(t, e, "chat", adapterproto.Availability{Condition: auth})
				r.clk.Advance(3 * time.Minute)
				gone(t, e, "chat")
			}, handoff.Signals{AdapterOffline: true, AuthInvalid: true, Since: t0.Add(3 * time.Minute)}},
		{"a second report that it is gone keeps the earlier time", "chat", ep,
			func(t *testing.T, r *rig, e *engine.Engine) {
				started(t, e, "chat")
				r.clk.Advance(3 * time.Minute)
				gone(t, e, "chat")
				r.clk.Advance(4 * time.Minute)
				gone(t, e, "chat")
			}, handoff.Signals{AdapterOffline: true, Since: t0.Add(3 * time.Minute)}},
		{"an instance that started again is online", "chat", ep,
			func(t *testing.T, r *rig, e *engine.Engine) {
				started(t, e, "chat")
				r.clk.Advance(3 * time.Minute)
				gone(t, e, "chat")
				started(t, e, "chat")
			}, handoff.Signals{}},
		{"going away again after a start is a new time", "chat", ep,
			func(t *testing.T, r *rig, e *engine.Engine) {
				started(t, e, "chat")
				r.clk.Advance(time.Minute)
				gone(t, e, "chat")
				started(t, e, "chat")
				r.clk.Advance(8 * time.Minute)
				gone(t, e, "chat")
			}, handoff.Signals{AdapterOffline: true, Since: t0.Add(9 * time.Minute)}},
		{"an instance that never answered adapter.start and is gone has no known start", "chat", ep,
			func(t *testing.T, r *rig, e *engine.Engine) {
				r.clk.Advance(time.Minute)
				gone(t, e, "chat")
			}, handoff.Signals{AdapterOffline: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, 10)
			r.enableInstance("agent", "")
			e := r.start()
			tc.setup(t, r, e)
			got, err := e.AdapterSignals(ctx, tc.instance, tc.endpoint)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("signals %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAnInstanceTheConfigurationDoesNotEnableIsUnknownToReadsAndToGone(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	r.enableInstance("agent", "")
	r.enableInstance("off", "")
	r.disableInstance("off")
	e := r.start()
	for _, inst := range []string{"nobody", "off"} {
		_, err := e.AdapterSignals(ctx, inst, "ep")
		wantReason(t, err, engine.ReasonInstanceUnknown)
		_, err = e.Adapter(ctx, inst)
		wantReason(t, err, engine.ReasonInstanceUnknown)
		wantReason(t, e.AdapterGone(ctx, inst, adapterhost.GoneStartFailed), engine.ReasonInstanceUnknown)
	}
	// An enabled instance that was never admitted: reads answer, a report that it
	// is gone is taken (the host reports a start that failed before admission),
	// and what needs an admitted instance refuses.
	if v, err := e.Adapter(ctx, "agent"); err != nil || v.Admitted || v.Online {
		t.Errorf("Adapter(agent) = %+v, %v; want a view of an instance that is not admitted and not online", v, err)
	}
	if err := e.AdapterGone(ctx, "agent", adapterhost.GoneStartFailed); err != nil {
		t.Errorf("AdapterGone for an enabled instance that was never admitted: %v", err)
	}
	wantReason(t, e.AdapterStarted(ctx, "agent", adapterproto.StartResult{Sessions: []adapterproto.SessionState{}}), engine.ReasonInstanceUnknown)
	n := adapterhost.Notification{Method: adapterproto.MethodTurnEnded, Params: adapterproto.TurnEndedParams{Endpoint: "x"}}
	wantReason(t, e.AdapterNotified(ctx, "agent", n), engine.ReasonInstanceUnknown)
}

func TestAnAdapterThatIsGoneKeepsItsEndpointsItsRecordsAndItsAdmission(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	id := register(t, e, "a")
	started(t, e, "chat")
	turn := adapterhost.Notification{Method: adapterproto.MethodTurnStarted, Params: adapterproto.TurnStartedParams{Endpoint: "sess-1", Turn: "t1", AtMS: 1}}
	if err := e.AdapterNotified(ctx, "chat", turn); err != nil {
		t.Fatal(err)
	}
	before := r.snapshot()
	r.clk.Advance(time.Minute)
	gone(t, e, "chat")
	settle(t, e)
	v, err := e.Adapter(ctx, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Admitted || v.Online || !v.OfflineSince.Equal(t0.Add(time.Minute)) {
		t.Errorf("view %+v: want admitted, not online, offline since %v", v, t0.Add(time.Minute))
	}
	started(t, e, "chat")
	if v, _ = e.Adapter(ctx, "chat"); !v.Online || !v.OfflineSince.IsZero() {
		t.Errorf("view %+v after the adapter started again: want online and no offline time", v)
	}
	gone(t, e, "chat")
	if len(v.Busy) != 0 {
		t.Errorf("busy %v after the adapter is gone, want the busy states unknown (cleared)", v.Busy)
	}
	if after := r.snapshot(); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Error("going away wrote to the disk")
	}
	if n := openEndpoints(t, e); n != 1 {
		t.Errorf("%d endpoints open, want the one registered", n)
	}
	if again := register(t, e, "a"); again != id {
		t.Errorf("registering again gave %s, want %s: the admission stays", again, id)
	}
}

// ---------------------------------------------------------------------------
// Notifications

func TestNotificationsAboutUnknownEndpointsAreAcceptedAndSetNothingOnDisk(t *testing.T) {
	noLeaks(t)
	const ghost = adapterproto.EndpointRef("ghost-session")
	turnStarted := adapterproto.TurnStartedParams{Endpoint: ghost, Turn: "t1", AtMS: 1}
	turnEnded := adapterproto.TurnEndedParams{Endpoint: ghost, Turn: "t1", Final: adapterproto.TurnCompleted, AtMS: 2}
	persisted := adapterproto.PromptPersistedParams{Endpoint: ghost, Attempt: "0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6"}
	handledAt := func(ep adapterproto.EndpointRef, d adapterproto.Disposition) adapterproto.NativeRequestHandledParams {
		return adapterproto.NativeRequestHandledParams{Endpoint: ep, Method: "item/approve", Disposition: d, AtMS: 3}
	}
	handled := func(d adapterproto.Disposition) adapterproto.NativeRequestHandledParams { return handledAt(ghost, d) }
	note := func(method string, p adapterproto.Message) adapterhost.Notification {
		return adapterhost.Notification{Method: method, Params: p}
	}
	refs := func(rs ...adapterproto.EndpointRef) []adapterproto.EndpointRef { return rs }

	r := newRig(t, 10)
	e := r.start()
	started(t, e, "chat")
	settle(t, e)
	diskBefore, commitsBefore := r.snapshot(), r.totalCommits()
	view := func() engine.AdapterView {
		t.Helper()
		v, err := e.Adapter(ctx, "chat")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	for _, step := range []struct {
		name     string
		n        adapterhost.Notification
		busy     []adapterproto.EndpointRef
		rejected []adapterproto.EndpointRef
	}{
		{"a turn starts on an endpoint the engine does not know", note(adapterproto.MethodTurnStarted, turnStarted), refs(ghost), nil},
		{"a prompt persisted is accepted and changes nothing", note(adapterproto.MethodPromptPersisted, persisted), refs(ghost), nil},
		{"a turn ends", note(adapterproto.MethodTurnEnded, turnEnded), nil, nil},
		{"the same turn started again, as a pointer to its parameters", note(adapterproto.MethodTurnStarted, &turnStarted), refs(ghost), nil},
		{"the same turn ended again, as a pointer to its parameters", note(adapterproto.MethodTurnEnded, &turnEnded), nil, nil},
		{"a request answered by the preset changes nothing", note(adapterproto.MethodNativeRequestHandled, handled(adapterproto.DispositionPreset)), nil, nil},
		{"a request answered late changes nothing", note(adapterproto.MethodNativeRequestHandled, handled(adapterproto.DispositionLate)), nil, nil},
		{"a request the preset rejects is remembered", note(adapterproto.MethodNativeRequestHandled, handled(adapterproto.DispositionRejectedByPreset)), nil, refs(ghost)},
		{"a request of an unknown method that was refused is remembered for its endpoint", note(adapterproto.MethodNativeRequestHandled, handledAt("ghost-2", adapterproto.DispositionRejectedUnknown)), nil, refs("ghost-2", ghost)},
	} {
		if err := e.AdapterNotified(ctx, "chat", step.n); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		v := view()
		if !slices.Equal(v.Busy, step.busy) || !slices.Equal(v.ServerRequestRejected, step.rejected) {
			t.Errorf("%s: busy %v, rejected %v; want %v, %v", step.name, v.Busy, v.ServerRequestRejected, step.busy, step.rejected)
		}
	}
	if after := r.snapshot(); fmt.Sprint(after) != fmt.Sprint(diskBefore) {
		t.Error("a notification wrote to the disk")
	}
	if n := r.totalCommits(); n != commitsBefore {
		t.Errorf("%d transactions committed by notifications", n-commitsBefore)
	}
	if n := openEndpoints(t, e); n != 0 {
		t.Errorf("%d endpoints open: a notification made an endpoint known", n)
	}

	// Several endpoints: the refs of the running turns, sorted.
	for _, ref := range []adapterproto.EndpointRef{"zz", "aa"} {
		p := turnStarted
		p.Endpoint = ref
		if err := e.AdapterNotified(ctx, "chat", note(adapterproto.MethodTurnStarted, p)); err != nil {
			t.Fatal(err)
		}
	}
	if got := view().Busy; !slices.Equal(got, refs("aa", "zz")) {
		t.Errorf("busy = %v, want [aa zz]", got)
	}

	// What is not a notification of the adapter is an error and not a panic.
	for _, bad := range []adapterhost.Notification{
		note("endpoint.register", adapterproto.RegisterParams{Key: "k", DisplayName: "k"}),
		note("anything", adapterproto.Empty{}),
		{Method: "nothing"},
	} {
		if err := e.AdapterNotified(ctx, "chat", bad); err == nil {
			t.Errorf("notification %q with %T parameters was accepted", bad.Method, bad.Params)
		}
	}

	// Going away makes the busy states unknown.
	gone(t, e, "chat")
	if got := view().Busy; len(got) != 0 {
		t.Errorf("busy = %v after the adapter is gone, want none", got)
	}

	// An instance that was not admitted cannot notify.
	r.enableInstance("agent", "")
	wantReason(t, e.AdapterNotified(ctx, "agent", note(adapterproto.MethodTurnStarted, turnStarted)), engine.ReasonInstanceUnknown)
}

func TestAdapterStartedTakesTheBusyStateOfTheSessionsItKnowsAndIgnoresTheRest(t *testing.T) {
	noLeaks(t)
	session, idle, other := mustEndpoint(t, 80), mustEndpoint(t, 81), mustEndpoint(t, 82)
	r := newRig(t, 10)
	r.enableInstance("agent", "")
	seed := func(id ident.EndpointID, inst string, status store.EndpointStatus, token byte) {
		rec := store.EndpointRecord{ID: id, Kind: sessionKind, Instance: inst, Status: status, Session: &store.SessionInfo{Preset: "default"}}
		if status != store.EndpointClosed {
			rec.Token = bytes.Repeat([]byte{token}, store.TokenLen)
		}
		r.seedRecord(rec)
	}
	seed(session, "chat", store.EndpointOpen, 0x5a)
	seed(idle, "chat", store.EndpointOpen, 0x5b)
	seed(other, "agent", store.EndpointOpen, 0x5c)
	e := r.start()
	before := len(r.log.Messages())
	ref := func(id ident.EndpointID) adapterproto.EndpointRef { return adapterproto.EndpointRef(id.String()) }
	err := e.AdapterStarted(ctx, "chat", adapterproto.StartResult{Sessions: []adapterproto.SessionState{
		{Endpoint: ref(session), State: adapterproto.AttachAttached, TurnActive: true},
		{Endpoint: ref(idle), State: adapterproto.AttachAttached, TurnActive: false},
		{Endpoint: ref(other), State: adapterproto.AttachAttached, TurnActive: true}, // a session of another instance
		{Endpoint: "never-heard-of", State: adapterproto.AttachAttached, TurnActive: true},
	}})
	if err != nil {
		t.Fatalf("AdapterStarted: %v (a session the engine does not know is not an error)", err)
	}
	v, err := e.Adapter(ctx, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Online || !slices.Equal(v.Busy, []adapterproto.EndpointRef{ref(session)}) {
		t.Errorf("online %v, busy %v; want online with only the known session that reported turn_active busy", v.Online, v.Busy)
	}
	msgs := r.log.Messages()
	if len(msgs) <= before {
		t.Error("the sessions the engine does not know were not logged")
	}
	for _, m := range msgs {
		if strings.Contains(m, "never-heard-of") {
			t.Error("the log carries the reference an adapter reported")
		}
	}

	// A second connection that reports the session idle makes it idle.
	err = e.AdapterStarted(ctx, "chat", adapterproto.StartResult{Sessions: []adapterproto.SessionState{
		{Endpoint: ref(session), State: adapterproto.AttachAttached, TurnActive: false},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ = e.Adapter(ctx, "chat"); len(v.Busy) != 0 {
		t.Errorf("busy %v after the adapter said the turn is over", v.Busy)
	}
}

// What was known of an earlier connection is not carried into the next one: the
// host reports nothing when it stops an instance, so a connection can follow
// another with no AdapterGone between. The busy state is replaced by what the new
// adapter.start result lists, not merged into the old one, and the availability
// snapshot is cleared until the new adapter sends one.
func TestANewConnectionStartsFromWhatItReportsAndNotFromTheOneBefore(t *testing.T) {
	noLeaks(t)
	session, left := mustEndpoint(t, 80), mustEndpoint(t, 81)
	r := newRig(t, 10)
	for i, id := range []ident.EndpointID{session, left} {
		r.seedRecord(store.EndpointRecord{ID: id, Kind: sessionKind, Instance: "chat", Status: store.EndpointOpen,
			Token: bytes.Repeat([]byte{0x5a + byte(i)}, store.TokenLen), Session: &store.SessionInfo{Preset: "default"}})
	}
	e := r.start()
	ref := func(id ident.EndpointID) adapterproto.EndpointRef { return adapterproto.EndpointRef(id.String()) }
	startWith := func(busy ...ident.EndpointID) {
		t.Helper()
		var res adapterproto.StartResult
		res.Sessions = []adapterproto.SessionState{}
		for _, id := range busy {
			res.Sessions = append(res.Sessions, adapterproto.SessionState{Endpoint: ref(id), State: adapterproto.AttachAttached, TurnActive: true})
		}
		if err := e.AdapterStarted(ctx, "chat", res); err != nil {
			t.Fatal(err)
		}
	}
	view := func() engine.AdapterView {
		t.Helper()
		v, err := e.Adapter(ctx, "chat")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	// The first connection: two sessions busy and a snapshot of availability.
	startWith(session, left)
	available(t, e, "chat", adapterproto.Availability{Condition: adapterproto.ConditionRuntimeAuth, SinceMS: ms(t0)})
	if v := view(); len(v.Busy) != 2 || len(v.Conditions) != 1 {
		t.Fatalf("the first connection: busy %v, conditions %v", v.Busy, v.Conditions)
	}

	// The next one reports one of them. The other is idle, not remembered as busy.
	startWith(session)
	if v := view(); !slices.Equal(v.Busy, []adapterproto.EndpointRef{ref(session)}) {
		t.Errorf("busy %v after a start that lists only %s, want only that one", v.Busy, ref(session))
	}

	// A new admission, with no report of the end of the connection before it: the
	// snapshot and the busy states of the connection before are gone until the
	// adapter reports them again.
	if err := admitOn(ctx, e, "chat", r.admit); err != nil {
		t.Fatal(err)
	}
	v := view()
	if len(v.Conditions) != 0 || !v.ConditionsReceived.IsZero() || len(v.Busy) != 0 {
		t.Errorf("after a new admission: conditions %v (received %v), busy %v; want none of them", v.Conditions, v.ConditionsReceived, v.Busy)
	}
	sig, err := e.AdapterSignals(ctx, "chat", ref(session))
	if err != nil {
		t.Fatal(err)
	}
	if sig.AuthInvalid {
		t.Error("the signals of an endpoint still carry the availability of the connection before")
	}
}

// ---------------------------------------------------------------------------
// Checkpoints

const attemptA = "0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6"

func checkpoints(t testing.TB, r *rig) map[store.CheckpointKey]string {
	t.Helper()
	out := map[store.CheckpointKey]string{}
	for _, c := range r.scan().Checkpoints {
		out[c.Key] = string(c.State)
	}
	return out
}

// seedCreations puts onto the disk a session of the instance for each attempt, as
// a creation that has ended leaves it: the open endpoint and the control record
// whose intent names the attempt. An adapter may put the checkpoint of an attempt
// only when the node knows the attempt as one of a session of the instance.
func (r *rig) seedCreations(instance string, attempts ...string) {
	r.t.Helper()
	for _, a := range attempts {
		i := r.creations
		r.creations++
		session := mustEndpoint(r.t, byte(120+i))
		rec := sessionCtlRecord(r.t, 70+i, session)
		rec.Intent.Attempt = adapterproto.AttemptID(a)
		r.seedRecord(store.EndpointRecord{ID: session, Kind: sessionKind, Instance: instance, Status: store.EndpointOpen, Token: bytes.Repeat([]byte{byte(10 + i)}, store.TokenLen), Session: &store.SessionInfo{Preset: "default"}, CreatedBy: strings.TrimSuffix(rec.Name(), ".rec")})
		r.seedRecord(rec)
	}
}

func attemptPut(id, state string) adapterproto.CheckpointPutParams {
	return adapterproto.CheckpointPutParams{Scope: adapterproto.ScopeAttempt, ID: id, State: json.RawMessage(state)}
}

func TestCheckpointPutIsOnDiskBeforeItIsAnsweredAndALaterPutReplacesIt(t *testing.T) {
	noLeaks(t)
	key := store.CheckpointKey{Scope: "attempt", ID: attemptA}

	t.Run("the state is stored compact, on disk when the answer comes, and a later put replaces it", func(t *testing.T) {
		const other = "11111111-2222-4333-8444-555555555555"
		r := newRig(t, 10)
		r.seedCreations("chat", attemptA, other)
		e := r.start()
		if err := e.CheckpointPut(ctx, "chat", attemptPut(attemptA, "{ \"a\": 1,\n \"b\": [1, 2] }")); err != nil {
			t.Fatal(err)
		}
		if got := checkpoints(t, r); len(got) != 1 || got[key] != `{"a":1,"b":[1,2]}` {
			t.Fatalf("on disk: %v, want the compact state under (attempt, %s)", got, attemptA)
		}
		if err := e.CheckpointPut(ctx, "chat", attemptPut(attemptA, `{"a":2}`)); err != nil {
			t.Fatal(err)
		}
		if got := checkpoints(t, r); len(got) != 1 || got[key] != `{"a":2}` {
			t.Fatalf("on disk after a second put: %v, want the state replaced", got)
		}
		if err := e.CheckpointPut(ctx, "chat", attemptPut(other, `{"b":1}`)); err != nil {
			t.Fatal(err)
		}
		if got := checkpoints(t, r); len(got) != 2 {
			t.Fatalf("%d checkpoints, want one per (scope, id): %v", len(got), got)
		}
		if diff, err := engine.ViewDiffersFromDisk(ctx, e, r.scan()); err != nil || len(diff) != 0 {
			t.Errorf("the view differs from the disk: %v, %v", diff, err)
		}
		r.restart()
		e = r.start()
		if got := checkpoints(t, r); got[key] != `{"a":2}` {
			t.Errorf("after a restart: %v", got)
		}
		if diff, err := engine.ViewDiffersFromDisk(ctx, e, r.scan()); err != nil || len(diff) != 0 {
			t.Errorf("after a restart the view differs from the disk: %v, %v", diff, err)
		}
	})

	t.Run("the limit is 16 KiB of compact JSON", func(t *testing.T) {
		const limit = 16 * 1024
		fill := func(total int) string { // an object of exactly total bytes
			const frame = len(`{"k":""}`)
			return `{"k":"` + strings.Repeat("x", total-frame) + `"}`
		}
		r := newRig(t, 10)
		r.seedCreations("chat", attemptA)
		e := r.start()
		if err := e.CheckpointPut(ctx, "chat", attemptPut(attemptA, fill(limit))); err != nil {
			t.Fatalf("a state of exactly %d bytes: %v", limit, err)
		}
		if got := checkpoints(t, r)[key]; len(got) != limit {
			t.Fatalf("stored %d bytes, want %d", len(got), limit)
		}
		ref := wantReason(t, e.CheckpointPut(ctx, "chat", attemptPut(attemptA, fill(limit+1))), engine.ReasonInvalidArgument)
		if ref.Field != "state" {
			t.Errorf("field %q, want state", ref.Field)
		}
		var ve *adapterproto.ValidationError
		if !errors.As(ref, &ve) || !ve.Beyond {
			t.Errorf("the refusal does not carry the validation error that says the state is beyond the limit: %v", ref)
		}
		if got := checkpoints(t, r)[key]; len(got) != limit {
			t.Errorf("the refused put changed the stored state (%d bytes)", len(got))
		}
		// The limit is of the compact form: spaces do not count.
		padded := `{"k":` + strings.Repeat(" ", 2*limit) + `"v"}`
		if err := e.CheckpointPut(ctx, "chat", attemptPut(attemptA, padded)); err != nil {
			t.Errorf("a small state with plenty of white space: %v", err)
		}
		if got := checkpoints(t, r)[key]; got != `{"k":"v"}` {
			t.Errorf("stored %q", got)
		}
	})

	t.Run("what the protocol refuses is invalid_argument with the field", func(t *testing.T) {
		r := newRig(t, 10)
		e := r.start()
		for _, tc := range []struct {
			name  string
			p     adapterproto.CheckpointPutParams
			field string
		}{
			{"a scope that is not one", adapterproto.CheckpointPutParams{Scope: "everything", ID: attemptA, State: json.RawMessage(`{}`)}, "scope"},
			{"an attempt id that is not an attempt id", attemptPut("not-a-uuid", `{}`), "id"},
			{"a session id that is empty", adapterproto.CheckpointPutParams{Scope: adapterproto.ScopeSession, ID: "", State: json.RawMessage(`{}`)}, "id"},
			{"a state that is not an object", attemptPut(attemptA, `[1]`), "state"},
			{"a state that is missing", adapterproto.CheckpointPutParams{Scope: adapterproto.ScopeAttempt, ID: attemptA}, "state"},
		} {
			ref := wantReason(t, e.CheckpointPut(ctx, "chat", tc.p), engine.ReasonInvalidArgument)
			if ref.Field != tc.field {
				t.Errorf("%s: field %q, want %q", tc.name, ref.Field, tc.field)
			}
		}
		if got := checkpoints(t, r); len(got) != 0 {
			t.Errorf("refused puts left %v on disk", got)
		}
	})

	t.Run("a session checkpoint needs a session endpoint of the instance that is open or being created", func(t *testing.T) {
		// The adapter keeps the state of a session while it creates it, before the
		// node has been told that the session exists, so an endpoint being created
		// takes a put as an open one does. The records below are seeded onto the disk,
		// as a creation would leave them.
		open, closed, foreign, creating := mustEndpoint(t, 80), mustEndpoint(t, 81), mustEndpoint(t, 82), mustEndpoint(t, 83)
		r := newRig(t, 10)
		r.enableInstance("agent", "")
		r.seedRecord(store.EndpointRecord{ID: open, Kind: sessionKind, Instance: "chat", Status: store.EndpointOpen, Token: bytes.Repeat([]byte{1}, store.TokenLen), Session: &store.SessionInfo{Preset: "default"}})
		r.seedRecord(store.EndpointRecord{ID: closed, Kind: sessionKind, Instance: "chat", Status: store.EndpointClosed, Session: &store.SessionInfo{Preset: "default"}})
		r.seedRecord(store.EndpointRecord{ID: foreign, Kind: sessionKind, Instance: "agent", Status: store.EndpointOpen, Token: bytes.Repeat([]byte{2}, store.TokenLen), Session: &store.SessionInfo{Preset: "default"}})
		// A session endpoint that is being created is made with the control record that
		// holds the intent, and is garbage without it.
		making := sessionCtlRecord(t, 50, creating)
		making.Intent.State, making.Intent.NativeRef = store.IntentCreating, ""
		r.seedRecord(making)
		r.seedRecord(store.EndpointRecord{ID: creating, Kind: sessionKind, Instance: "chat", Status: store.EndpointCreating, Token: bytes.Repeat([]byte{3}, store.TokenLen), Session: &store.SessionInfo{Preset: "default"}, CreatedBy: strings.TrimSuffix(making.Name(), ".rec")})
		e := r.start()
		direct := register(t, e, "a")
		put := func(id ident.EndpointID) error {
			return e.CheckpointPut(ctx, "chat", adapterproto.CheckpointPutParams{Scope: adapterproto.ScopeSession, ID: id.String(), State: json.RawMessage(`{"n":1}`)})
		}
		for name, id := range map[string]ident.EndpointID{
			"a direct endpoint":                 direct,
			"a closed session endpoint":         closed,
			"a session endpoint of another one": foreign,
			"an endpoint that does not exist":   mustEndpoint(t, 99),
		} {
			wantReason(t, put(id), engine.ReasonEndpointUnknown)
			if got := checkpoints(t, r); len(got) != 0 {
				t.Errorf("%s: a refused put left %v on disk", name, got)
			}
		}
		if err := put(open); err != nil {
			t.Fatalf("an open session endpoint of the instance: %v", err)
		}
		if got := checkpoints(t, r); got[store.CheckpointKey{Scope: "session", ID: open.String()}] != `{"n":1}` {
			t.Errorf("on disk: %v", got)
		}
		if err := put(creating); err != nil {
			t.Fatalf("a session endpoint of the instance whose creation has not ended: %v", err)
		}
	})

	t.Run("an attempt checkpoint needs an attempt of a session of the instance", func(t *testing.T) {
		// The attempts below are the creation attempt of a session of the instance
		// and the first prompt that follows from it; the others belong to a session
		// of another instance, to a session that is closed, or to nobody. A put for
		// any of them would write the state that the creation of another instance
		// reads back.
		const (
			mine, foreign, closedOne, nobody = "21111111-2222-4333-8444-555555555555", "31111111-2222-4333-8444-555555555555", "41111111-2222-4333-8444-555555555555", "51111111-2222-4333-8444-555555555555"
		)
		r := newRig(t, 10)
		r.enableInstance("agent", "")
		r.seedCreations("chat", mine)
		r.seedCreations("agent", foreign)
		closedSession := mustEndpoint(t, 140)
		closedCtl := sessionCtlRecord(t, 90, closedSession)
		closedCtl.Intent.Attempt = closedOne
		r.seedRecord(store.EndpointRecord{ID: closedSession, Kind: sessionKind, Instance: "chat", Status: store.EndpointClosed, Session: &store.SessionInfo{Preset: "default"}, CreatedBy: strings.TrimSuffix(closedCtl.Name(), ".rec")})
		r.seedRecord(closedCtl)
		e := r.start()
		for name, id := range map[string]string{
			"an attempt of a session of another instance": foreign,
			"an attempt of a closed session":              closedOne,
			"an attempt that belongs to nobody":           nobody,
		} {
			wantReason(t, e.CheckpointPut(ctx, "chat", attemptPut(id, `{"n":1}`)), engine.ReasonEndpointUnknown)
			if got := checkpoints(t, r); len(got) != 0 {
				t.Errorf("%s: a refused put left %v on disk", name, got)
			}
		}
		if err := e.CheckpointPut(ctx, "chat", attemptPut(mine, `{"n":1}`)); err != nil {
			t.Fatalf("the creation attempt of a session of the instance: %v", err)
		}
		if err := e.CheckpointPut(ctx, "agent", attemptPut(mine, `{"n":2}`)); err == nil {
			t.Error("another instance put the checkpoint of an attempt of this one")
		}
		first := engine.FirstPromptAttempt(adapterproto.AttemptID(mine))
		if err := e.CheckpointPut(ctx, "chat", attemptPut(string(first), `{"n":3}`)); err != nil {
			t.Fatalf("the first prompt of that session: %v", err)
		}
		got := checkpoints(t, r)
		if len(got) != 2 || got[store.CheckpointKey{Scope: "attempt", ID: mine}] != `{"n":1}` || got[store.CheckpointKey{Scope: "attempt", ID: string(first)}] != `{"n":3}` {
			t.Errorf("on disk: %v, want the two accepted puts only", got)
		}
	})

	t.Run("an instance that was not admitted is unknown", func(t *testing.T) {
		r := newRig(t, 10)
		r.enableInstance("agent", "")
		e := r.start()
		wantReason(t, e.CheckpointPut(ctx, "agent", attemptPut(attemptA, `{}`)), engine.ReasonInstanceUnknown)
		wantReason(t, e.CheckpointPut(ctx, "nobody", attemptPut(attemptA, `{}`)), engine.ReasonInstanceUnknown)
		if got := checkpoints(t, r); len(got) != 0 {
			t.Errorf("refused puts left %v on disk", got)
		}
	})

	t.Run("it is taken while the engine recovers and after it serves", func(t *testing.T) {
		r := newRig(t, 10)
		r.seedCreations("chat", attemptA)
		e, _, err := r.open()
		if err != nil {
			t.Fatal(err)
		}
		e.Start()
		t.Cleanup(func() { r.stop2(e) })
		if err := admitOn(ctx, e, "chat", r.admit); err != nil {
			t.Fatal(err)
		}
		if e.Phase() != engine.PhaseRecovering {
			t.Fatalf("phase %v, want recovering", e.Phase())
		}
		wait, cancel := context.WithTimeout(ctx, 5*time.Second) // a put that waits for the running phase would never return
		defer cancel()
		if err := e.CheckpointPut(wait, "chat", attemptPut(attemptA, `{"during":"recovery"}`)); err != nil {
			t.Fatalf("while recovering: %v", err)
		}
		if got := checkpoints(t, r)[key]; got != `{"during":"recovery"}` {
			t.Errorf("on disk: %q", got)
		}
		if err := e.RecoverDelivery(ctx); err != nil {
			t.Fatal(err)
		}
		e.Serve()
		if err := e.CheckpointPut(ctx, "chat", attemptPut(attemptA, `{"after":"serve"}`)); err != nil {
			t.Fatalf("after Serve: %v", err)
		}
		if got := checkpoints(t, r)[key]; got != `{"after":"serve"}` {
			t.Errorf("on disk: %q", got)
		}
	})

	t.Run("a write that fails before its commit point is back pressure and leaves the view and the disk as they were", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			err  error
			want engine.Reason
		}{
			{"no space", errDiskFull, engine.ReasonStorageFull},
			{"an I/O error", errIO, engine.ReasonStorageUnavailable},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r := newRig(t, 10)
				r.seedCreations("chat", attemptA)
				e := r.start()
				if err := e.CheckpointPut(ctx, "chat", attemptPut(attemptA, `{"old":true}`)); err != nil {
					t.Fatal(err)
				}
				rule := r.faulty.Fail(failMatch("checkpoint-put", fsxWrite), tc.err)
				wantReason(t, e.CheckpointPut(ctx, "chat", attemptPut(attemptA, `{"new":true}`)), tc.want)
				if rule.Hits() == 0 {
					t.Fatal("the fault was not injected")
				}
				if got := checkpoints(t, r)[key]; got != `{"old":true}` {
					t.Errorf("on disk: %q, want the old state", got)
				}
				if diff, err := engine.ViewDiffersFromDisk(ctx, e, r.scan()); err != nil || len(diff) != 0 {
					t.Errorf("the view differs from the disk: %v, %v", diff, err)
				}
				if e.Phase() != engine.PhaseRunning {
					t.Errorf("phase %v, want the engine to go on", e.Phase())
				}
			})
		}
	})

	t.Run("a failure after the commit point stops the loop as for every write", func(t *testing.T) {
		r := newRig(t, 10)
		r.seedCreations("chat", attemptA)
		e := r.start()
		rule := r.faulty.Fail(failMatch("checkpoint-put", fsxSyncDir), errIO)
		wantReason(t, e.CheckpointPut(ctx, "chat", attemptPut(attemptA, `{"a":1}`)), engine.ReasonStorageFatal)
		if rule.Hits() == 0 {
			t.Fatal("the fault was not injected")
		}
		select {
		case <-e.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the engine did not stop")
		}
		if e.Phase() != engine.PhaseFailed {
			t.Errorf("phase %v, want failed", e.Phase())
		}
	})
}

// ---------------------------------------------------------------------------
// Quiet

func TestQuiesceJudgesTheAdapterHostsCounts(t *testing.T) {
	noLeaks(t)
	quiet := adapterhost.ConnCounts{Connected: true, Handled: 4}
	with := func(f func(*adapterhost.ConnCounts)) adapterhost.ConnCounts {
		c := quiet
		f(&c)
		return c
	}
	expect4 := engine.AdapterExpect{Notifications: 4}
	for _, tc := range []struct {
		name   string
		counts adapterhost.ConnCounts
		expect engine.AdapterExpect
		holds  bool
	}{
		{"the handled count equals what the adapter sent", quiet, expect4, true},
		{"fewer handled than sent", with(func(c *adapterhost.ConnCounts) { c.Handled = 3 }), expect4, false},
		{"more handled than sent", with(func(c *adapterhost.ConnCounts) { c.Handled = 5 }), expect4, false},
		{"a notification is buffered", with(func(c *adapterhost.ConnCounts) { c.Buffered = 1 }), expect4, false},
		{"a request of the adapter is being answered", with(func(c *adapterhost.ConnCounts) { c.Requests = 1 }), expect4, false},
		{"the host is working", with(func(c *adapterhost.ConnCounts) { c.Working = true }), expect4, false},
		{"held calls equal in another order", with(func(c *adapterhost.ConnCounts) { c.Calls = []string{"prompt.deliver", "session.create"} }),
			engine.AdapterExpect{Notifications: 4, Held: []string{"session.create", "prompt.deliver"}}, true},
		{"held calls with a repeated method", with(func(c *adapterhost.ConnCounts) { c.Calls = []string{"a", "b", "a"} }),
			engine.AdapterExpect{Notifications: 4, Held: []string{"a", "a", "b"}}, true},
		{"an extra call", with(func(c *adapterhost.ConnCounts) { c.Calls = []string{"session.create", "prompt.deliver"} }),
			engine.AdapterExpect{Notifications: 4, Held: []string{"session.create"}}, false},
		{"a call that is missing", with(func(c *adapterhost.ConnCounts) { c.Calls = []string{"session.create"} }),
			engine.AdapterExpect{Notifications: 4, Held: []string{"session.create", "prompt.deliver"}}, false},
		{"a call where none is held", with(func(c *adapterhost.ConnCounts) { c.Calls = []string{"session.create"} }), expect4, false},
		{"the same number of calls, not the same multiset", with(func(c *adapterhost.ConnCounts) { c.Calls = []string{"a", "b", "b"} }),
			engine.AdapterExpect{Notifications: 4, Held: []string{"a", "a", "b"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := newHostDouble()
			host.set("chat", tc.counts)
			r := newRig(t, 10, withAdapters(host))
			e := r.start()
			qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if !tc.holds {
				cancel()
				qctx, cancel = context.WithTimeout(ctx, 300*time.Millisecond)
			}
			defer cancel()
			rep, err := e.Quiesce(qctx, engine.Expect{Adapters: map[string]engine.AdapterExpect{"chat": tc.expect}})
			if tc.holds {
				if err != nil || !rep.Satisfied || rep.AdaptersPending != 0 {
					t.Fatalf("Quiesce = %+v, %v; want quiet", rep, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Quiesce returned quiet (%+v) with counts %+v against %+v", rep, tc.counts, tc.expect)
			}
			if rep.Satisfied || rep.AdaptersPending != 1 {
				t.Errorf("report %+v, want not satisfied with one instance pending", rep)
			}
			if !strings.Contains(err.Error(), "chat") {
				t.Errorf("error %q does not say which instance does not hold", err)
			}
			if host.asked.Load() == 0 {
				t.Error("the host was never asked for its counts")
			}
		})
	}

	t.Run("an instance that is not named is not judged", func(t *testing.T) {
		host := newHostDouble()
		host.set("other", adapterhost.ConnCounts{Handled: 9, Buffered: 3, Requests: 2, Working: true, Calls: []string{"x"}})
		r := newRig(t, 10, withAdapters(host))
		e := r.start()
		rep, err := e.Quiesce(ctx, engine.Expect{Adapters: map[string]engine.AdapterExpect{"chat": {}}})
		if err != nil || !rep.Satisfied {
			t.Fatalf("Quiesce = %+v, %v; want quiet", rep, err)
		}
		rep, err = e.Quiesce(ctx, engine.Expect{})
		if err != nil || !rep.Satisfied {
			t.Fatalf("Quiesce with no expectation = %+v, %v; want quiet", rep, err)
		}
	})

	t.Run("a host whose counts settle later lets Quiesce return", func(t *testing.T) {
		host := newHostDouble()
		host.set("chat", adapterhost.ConnCounts{Handled: 1, Buffered: 3})
		r := newRig(t, 10, withAdapters(host))
		e := r.start()
		go func() {
			time.Sleep(50 * time.Millisecond)
			host.set("chat", adapterhost.ConnCounts{Handled: 4})
		}()
		qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		rep, err := e.Quiesce(qctx, engine.Expect{Adapters: map[string]engine.AdapterExpect{"chat": {Notifications: 4}}})
		if err != nil || !rep.Satisfied {
			t.Fatalf("Quiesce = %+v, %v; want quiet once the counts settle", rep, err)
		}
	})

	t.Run("expectations about adapters without a host are an error", func(t *testing.T) {
		r := newRig(t, 10)
		e := r.start()
		_, err := e.Quiesce(ctx, engine.Expect{Adapters: map[string]engine.AdapterExpect{"chat": {}}})
		if err == nil || !strings.Contains(err.Error(), "adapter host") {
			t.Errorf("error %v, want one that says the engine has no adapter host", err)
		}
	})

	t.Run("a report can be compared with the zero report", func(t *testing.T) {
		r := newRig(t, 10)
		e := r.start()
		rep, err := e.Quiesce(ctx, engine.Expect{})
		if err != nil || rep == (engine.Report{}) {
			t.Errorf("Quiesce = %+v, %v; want a satisfied report that is not the zero one", rep, err)
		}
	})
}
