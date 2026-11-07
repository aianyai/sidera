package engine_test

import (
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// runtimeAndDirectAdmission is the admission of an adapter with a runtime that
// takes attached sessions and with direct endpoints.
func runtimeAndDirectAdmission(t testing.TB) adapterreg.Admission {
	t.Helper()
	caps := adapterproto.Capabilities{
		Runtime: &adapterproto.RuntimeCapabilities{Frontends: []adapterproto.Frontend{adapterproto.FrontendCLI}, Attach: true},
		Direct:  &adapterproto.DirectCapabilities{MaxEndpoints: 5},
	}
	adm, err := adapterreg.Admit(caps, mustManifest(t))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	return adm
}

// directoryAdapterRig is a node whose adapter is connected and has a runtime and direct
// endpoints. It has not been probed when probed is false.
func directoryAdapterRig(t *testing.T, probed bool) (*engine.Engine, *hostDouble) {
	t.Helper()
	noLeaks(t)
	d := newHostDouble()
	r := newRig(t, 10, withAdapters(d))
	r.admit = runtimeAndDirectAdmission(t)
	e, _, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	e.SetTransport(transport.NewLoopback(r.node, e, nil))
	e.Start()
	r.eng = e
	t.Cleanup(func() { r.stop(e) })
	if err := e.RecoverDelivery(ctx); err != nil {
		t.Fatal(err)
	}
	e.Serve()
	if err := admitOn(ctx, e, "chat", r.admit); err != nil {
		t.Fatal(err)
	}
	started(t, e, "chat")
	if probed {
		if err := e.AdapterProbed(ctx, "chat", probeResult("claude", "1.0.0")); err != nil {
			t.Fatal(err)
		}
	}
	return e, d
}

// adapterEntries lists the directory as caller sees it, by endpoint.
func adapterEntries(t testing.TB, e *engine.Engine, caller engine.Caller) map[ident.EndpointID]*transport.DirectoryAdapter {
	t.Helper()
	page, err := e.Directory(ctx, caller, engine.DirectoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[ident.EndpointID]*transport.DirectoryAdapter{}
	for _, ep := range page.Endpoints {
		out[ep.Address.Endpoint] = ep.Adapter
	}
	return out
}

func wantAdapter(t testing.TB, what string, got *transport.DirectoryAdapter, want transport.DirectoryAdapter) {
	t.Helper()
	if got == nil || !reflect.DeepEqual(*got, want) {
		t.Errorf("%s: adapter = %+v, want %+v", what, got, want)
	}
}

// An entry names the instance that serves the endpoint and the adapter it runs
// as the configuration says. The adapter's version comes with its handshake and
// the runtime with its first probe that passes; each is absent before. A direct
// endpoint has no runtime. A runtime and a session endpoint carry the runtime.
func TestDirectoryEntriesNameTheAdapterThatServesThem(t *testing.T) {
	e, d := directoryAdapterRig(t, true)
	d.answerAttach(attachedAnswer, nil)
	reader := register(t, e, "reader")
	caller := engineCaller(reader)
	view, err := e.Adapter(ctx, "chat")
	if err != nil {
		t.Fatal(err)
	}
	runtimeID, err := ident.ParseEndpointID(string(view.RuntimeEndpoint))
	if err != nil {
		t.Fatal(err)
	}
	attached, err := e.AttachSession(ctx, attachChat("native-a"))
	if err != nil {
		t.Fatal(err)
	}
	session := attached.Endpoint.Endpoint

	got := adapterEntries(t, e, caller)
	rt := &adapterproto.RuntimeInfo{Name: "claude", Version: "1.0.0"}
	wantAdapter(t, "direct", got[reader], transport.DirectoryAdapter{Instance: "chat", ID: "chat", Version: "1.0.0"})
	wantAdapter(t, "runtime", got[runtimeID], transport.DirectoryAdapter{Instance: "chat", ID: "chat", Version: "1.0.0", Runtime: rt})
	wantAdapter(t, "session", got[session], transport.DirectoryAdapter{Instance: "chat", ID: "chat", Version: "1.0.0", Runtime: rt})

	// A later probe that finds a newer runtime replaces it.
	if err := e.AdapterProbed(ctx, "chat", probeResult("claude", "2.0.0")); err != nil {
		t.Fatal(err)
	}
	got = adapterEntries(t, e, caller)
	if a := got[runtimeID]; a == nil || a.Runtime == nil || a.Runtime.Version != "2.0.0" {
		t.Errorf("runtime after a newer probe = %+v, want version 2.0.0", a)
	}
}

// Before the adapter has connected since the node started, its entries name the
// instance and the adapter of the configuration and nothing more: no version, and
// no runtime, since nothing has been probed.
func TestDirectoryEntriesBeforeTheAdapterHasConnected(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	old := r.start()
	reader := register(t, old, "reader")
	r.restart()
	e, _, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	e.SetTransport(transport.NewLoopback(r.node, e, nil))
	e.Start()
	r.eng = e
	t.Cleanup(func() { r.stop(e) })
	if err := e.RecoverDelivery(ctx); err != nil {
		t.Fatal(err)
	}
	e.Serve()
	wantAdapter(t, "direct before admission", adapterEntries(t, e, engineCaller(reader))[reader], transport.DirectoryAdapter{Instance: "chat", ID: "chat"})
}

// A runtime that has not passed a probe is not named: the runtime endpoint is
// listed with its instance and adapter, and without a runtime, until the probe
// passes.
func TestDirectoryRuntimeIsAbsentUntilAProbePasses(t *testing.T) {
	e, _ := directoryAdapterRig(t, false)
	reader := register(t, e, "reader")
	view, err := e.Adapter(ctx, "chat")
	if err != nil {
		t.Fatal(err)
	}
	runtimeID, err := ident.ParseEndpointID(string(view.RuntimeEndpoint))
	if err != nil {
		t.Fatal(err)
	}
	wantAdapter(t, "before the probe", adapterEntries(t, e, engineCaller(reader))[runtimeID], transport.DirectoryAdapter{Instance: "chat", ID: "chat", Version: "1.0.0"})
	if err := e.AdapterProbed(ctx, "chat", probeResult("claude", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	wantAdapter(t, "after the probe", adapterEntries(t, e, engineCaller(reader))[runtimeID], transport.DirectoryAdapter{Instance: "chat", ID: "chat", Version: "1.0.0", Runtime: &adapterproto.RuntimeInfo{Name: "claude", Version: "1.0.0"}})
}

// An endpoint its owner hides from the directory is not listed, so neither is the
// adapter behind it: the adapter information is part of the entry and reaches an
// asker on no other path.
func TestDirectoryHidesTheAdapterOfAHiddenEndpoint(t *testing.T) {
	e, _ := directoryAdapterRig(t, true)
	reader, hidden := register(t, e, "reader"), register(t, e, "hidden")
	if _, err := e.Declare(ctx, engineCaller(hidden), engine.DeclarationPatch{HiddenFromDirectory: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	got := adapterEntries(t, e, engineCaller(reader))
	if adapter, listed := got[hidden]; listed {
		t.Errorf("the hidden endpoint is listed with adapter %+v", adapter)
	}
	if got[reader] == nil {
		t.Errorf("the visible endpoint is not listed with its adapter: %+v", got)
	}
}
