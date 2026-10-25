package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/publication"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The cap of endpoints is a property of the node, not of the adapter that asks:
// the node always has a declaration it can issue, so the 257th endpoint is
// refused whatever instance it comes from and however many the instance may
// still register. The test lives at the layer of the engine's own interface over
// the real store and configuration, because the engine-layer fixture of the
// scenario suite is built on top of this package: it registers the endpoints
// through the entry the adapter host uses, the only one a direct endpoint has.
func TestEndpointBeyondNodeCapRefused(t *testing.T) {
	noLeaks(t)
	r := newRig(t, adapterproto.MaxDirectEndpoints)
	e := r.start()

	ids := map[ident.EndpointID]bool{}
	for i := 0; i < publication.MaxEndpointsPerNode; i++ {
		id := register(t, e, keyName(i))
		if ids[id] {
			t.Fatalf("endpoint %d got the id of an earlier one", i)
		}
		ids[id] = true
	}
	if got := openEndpoints(t, e); got != publication.MaxEndpointsPerNode {
		t.Fatalf("%d endpoints open, want %d", got, publication.MaxEndpointsPerNode)
	}

	_, err := e.RegisterDirect(context.Background(), "chat", adapterproto.RegisterParams{Key: keyName(publication.MaxEndpointsPerNode), DisplayName: "one too many"})
	ref := wantReason(t, err, engine.ReasonEndpointLimit)
	if ref.Limit != engine.LimitNode {
		t.Errorf("refusal limit %s, want node", ref.Limit)
	}
	if got := openEndpoints(t, e); got != publication.MaxEndpointsPerNode {
		t.Errorf("%d endpoints open after the refusal, want %d", got, publication.MaxEndpointsPerNode)
	}
	if got := len(r.cfg.EndpointIDs("chat").Direct); got != publication.MaxEndpointsPerNode {
		t.Errorf("%d stable ids kept after the refusal, want %d: a refused registration keeps nothing", got, publication.MaxEndpointsPerNode)
	}
	sc, err := r.st.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Endpoints) != publication.MaxEndpointsPerNode {
		t.Errorf("%d endpoint records on disk, want %d", len(sc.Endpoints), publication.MaxEndpointsPerNode)
	}

	// An endpoint that is already registered is not a new one: the same key gives
	// the same endpoint at the cap.
	again, err := e.RegisterDirect(context.Background(), "chat", adapterproto.RegisterParams{Key: keyName(0), DisplayName: "again"})
	if err != nil {
		t.Fatalf("registering a key that is registered, at the cap: %v", err)
	}
	if id, _ := ident.ParseEndpointID(string(again.Endpoint)); !ids[id] {
		t.Errorf("the key registered again gave endpoint %s, which is not one of the registered", again.Endpoint)
	}

	// A closed endpoint no longer counts.
	var first ident.EndpointID
	for id := range ids {
		first = id
		break
	}
	if _, err := e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: first}); err != nil {
		t.Fatalf("CloseEndpoint: %v", err)
	}
	settle(t, e)
	if _, err := e.RegisterDirect(context.Background(), "chat", adapterproto.RegisterParams{Key: keyName(publication.MaxEndpointsPerNode), DisplayName: "fits now"}); err != nil {
		t.Errorf("registering after one endpoint closed: %v", err)
	}
}

// The adapter's own limit is judged apart from the node's, and tells which it
// was, so that the adapter does not wait for room that will not come.
func TestInstanceLimitRefusesBeforeTheNodeCap(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 3)
	e := r.start()
	for i := 0; i < 3; i++ {
		register(t, e, keyName(i))
	}
	_, err := e.RegisterDirect(context.Background(), "chat", adapterproto.RegisterParams{Key: keyName(3), DisplayName: "fourth"})
	ref := wantReason(t, err, engine.ReasonEndpointLimit)
	if ref.Limit != engine.LimitInstance {
		t.Errorf("refusal limit %s, want instance", ref.Limit)
	}
	if got := openEndpoints(t, e); got != 3 {
		t.Errorf("%d endpoints open, want 3", got)
	}
}

// The same key always gets the same endpoint: the promise of the adapter
// protocol, kept across a restart because the id is kept in config/.
func TestSameKeyGivesTheSameEndpointAcrossARestart(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	a := register(t, e, "notes")
	if b := register(t, e, "notes"); a != b {
		t.Fatalf("registered twice: %s then %s", a, b)
	}
	if c := register(t, e, "other"); c == a {
		t.Fatalf("two keys share endpoint %s", a)
	}
	eps, err := e.Endpoints(context.Background())
	if err != nil || len(eps) != 2 {
		t.Fatalf("Endpoints = %d, %v; want 2", len(eps), err)
	}

	r.restart()
	e = r.start()
	if got := openEndpoints(t, e); got != 2 {
		t.Errorf("%d endpoints open after a restart, want 2", got)
	}
	if b := register(t, e, "notes"); a != b {
		t.Errorf("after a restart the key gave %s, want %s", b, a)
	}
}

func TestRegisterNeedsAnAdmittedInstanceThatDeclaresDirectEndpoints(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()

	_, err := e.RegisterDirect(context.Background(), "nobody", adapterproto.RegisterParams{Key: "k", DisplayName: "k"})
	wantReason(t, err, engine.ReasonInstanceUnknown)

	runtimeOnly := adapterproto.Capabilities{Runtime: &adapterproto.RuntimeCapabilities{Frontends: []adapterproto.Frontend{"cli"}}}
	adm, aerr := adapterreg.Admit(runtimeOnly, mustManifest(t))
	if aerr != nil {
		t.Skipf("a runtime-only admission cannot be built here: %v", aerr)
	}
	if err := e.AdapterAdmitted(context.Background(), "chat", adm); err != nil {
		t.Fatal(err)
	}
	_, err = e.RegisterDirect(context.Background(), "chat", adapterproto.RegisterParams{Key: "k", DisplayName: "k"})
	wantReason(t, err, engine.ReasonWrongEndpointKind)
}

func TestRegisterRefusesWhatTheProtocolRefuses(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	for _, tc := range []struct {
		name  string
		p     adapterproto.RegisterParams
		field string
	}{
		{"empty key", adapterproto.RegisterParams{DisplayName: "x"}, "key"},
		{"empty display name", adapterproto.RegisterParams{Key: "k"}, "display_name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.RegisterDirect(context.Background(), "chat", tc.p)
			ref := wantReason(t, err, engine.ReasonInvalidArgument)
			if ref.Field != tc.field {
				t.Errorf("field %q, want %q", ref.Field, tc.field)
			}
		})
	}
	if got := openEndpoints(t, e); got != 0 {
		t.Errorf("%d endpoints after refused registrations", got)
	}
}

// The system endpoint is the node's own sender of feedback: it is not an
// endpoint anyone registers, it takes no place in the cap and no one closes it.
func TestSystemEndpointIsNotRegisteredCountedOrClosable(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	sys := e.SystemAddress()
	if sys.Node != r.node || !sys.Endpoint.IsSystem() {
		t.Fatalf("SystemAddress = %s, want the system endpoint of node %s", sys, r.node)
	}
	register(t, e, "a")
	eps, err := e.Endpoints(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range eps {
		if ep.ID.IsSystem() {
			t.Errorf("the list of endpoints holds the system endpoint")
		}
	}
	_, err = e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: ident.SystemEndpoint()})
	wantReason(t, err, engine.ReasonEndpointUnknown)
	cnt, err := e.Counts(context.Background())
	if err != nil || cnt.Endpoints != 1 {
		t.Errorf("Counts = %+v, %v; want one endpoint", cnt, err)
	}
}

func TestEndpointsListsEveryEndpointWithWhatTheAdapterSaid(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	res, err := e.RegisterDirect(context.Background(), "chat", adapterproto.RegisterParams{Key: "notes", DisplayName: "Notes", Description: "keeps the notes"})
	if err != nil {
		t.Fatal(err)
	}
	eps, err := e.Endpoints(context.Background())
	if err != nil || len(eps) != 1 {
		t.Fatalf("Endpoints = %v, %v", eps, err)
	}
	ep := eps[0]
	if string(res.Endpoint) != ep.ID.String() || ep.Address != (ident.Address{Node: r.node, Endpoint: ep.ID}) {
		t.Errorf("endpoint %s listed as %s at %s", res.Endpoint, ep.ID, ep.Address)
	}
	if ep.Kind != adapterreg.KindDirect || ep.Instance != "chat" || ep.Status != store.EndpointOpen ||
		ep.DisplayName != "Notes" || ep.Description != "keeps the notes" || ep.Draining {
		t.Errorf("listed %+v", ep)
	}

	// What the adapter said is kept: after a restart, before the adapter says it
	// again, the list still shows it.
	r.restart()
	e = r.start()
	eps, err = e.Endpoints(context.Background())
	if err != nil || len(eps) != 1 || eps[0].DisplayName != "Notes" || eps[0].Description != "keeps the notes" {
		t.Errorf("after a restart: %+v, %v", eps, err)
	}
	// And it is what the adapter says last.
	if _, err := e.RegisterDirect(context.Background(), "chat", adapterproto.RegisterParams{Key: "notes", DisplayName: "Notebook"}); err != nil {
		t.Fatal(err)
	}
	if eps, _ = e.Endpoints(context.Background()); len(eps) != 1 || eps[0].DisplayName != "Notebook" || eps[0].Description != "" {
		t.Errorf("after the adapter said it again: %+v", eps)
	}
}

func TestCloseIsRefusedForWhatIsNotAnEndpointOfTheNode(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	_, err := e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: mustEndpoint(t, 99)})
	wantReason(t, err, engine.ReasonEndpointUnknown)
	_, err = e.CloseEndpoint(context.Background(), engine.CloseRequest{})
	wantReason(t, err, engine.ReasonEndpointUnknown)
}

func mustEndpoint(t testing.TB, b byte) ident.EndpointID {
	t.Helper()
	bs := make([]byte, ident.EndpointIDLen)
	for i := range bs {
		bs[i] = b
	}
	id, err := ident.EndpointIDFromBytes(bs)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

var _ = errors.New

// The registration of an adapter's endpoints is for an instance the owner has
// enabled: an instance that was disabled is not admitted.
func TestAnInstanceTheOwnerDisabledIsNotAdmitted(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	var op admin.OperationID
	op[0] = 9
	if _, err := r.cfg.DisableAdapter(configRequest(op, r.cfg.Instances().Version), "chat", nil, jsonResult); err != nil {
		t.Fatalf("DisableAdapter: %v", err)
	}
	e, _, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	e.Start()
	t.Cleanup(func() { r.stop2(e) })
	wantReason(t, e.AdapterAdmitted(context.Background(), "chat", r.admit), engine.ReasonInstanceUnknown)
	wantReason(t, e.AdapterAdmitted(context.Background(), "nobody", r.admit), engine.ReasonInstanceUnknown)
}
