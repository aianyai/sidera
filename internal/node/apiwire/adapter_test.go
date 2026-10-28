package apiwire_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

func wireError(t *testing.T, err error) *peer.Error {
	t.Helper()
	pe, ok := err.(*peer.Error)
	if !ok {
		t.Fatalf("the error is %T (%v), want the *peer.Error the adapter is answered with", err, err)
	}
	return pe
}

func unknownEndpointRef(t *testing.T) adapterproto.EndpointRef {
	t.Helper()
	id, err := ident.EndpointIDFromBytes(bytes.Repeat([]byte{0x33}, ident.EndpointIDLen))
	if err != nil {
		t.Fatal(err)
	}
	return adapterproto.EndpointRef(id.String())
}

// Each of the three requests of an adapter that the engine answers itself is
// answered with the codes of the protocol, and not with the engine's own words.
func TestTheEngineBehindTheHostAnswersItsRefusalsWithTheCodesOfTheProtocol(t *testing.T) {
	h := harness.New(t, harness.Options{MaxDirect: 1})
	e := apiwire.AdapterEngine(h.Engine())
	ctx := context.Background()
	reg := func(key string) adapterproto.RegisterParams {
		return adapterproto.RegisterParams{Key: key, DisplayName: "Name of " + key}
	}

	if _, err := e.RegisterDirect(ctx, harness.Instance, reg("first")); err != nil {
		t.Fatalf("RegisterDirect: %v", err)
	}
	for _, tc := range []struct {
		name string
		err  error
		code int
		data string
	}{
		{"a second endpoint beyond the declared limit",
			func() error { _, err := e.RegisterDirect(ctx, harness.Instance, reg("second")); return err }(), 1007, `{"limit":"instance"}`},
		{"an instance the engine does not know",
			func() error { _, err := e.RegisterDirect(ctx, "nobody", reg("x")); return err }(), -32603, ""},
		{"a key that is not valid",
			func() error { _, err := e.RegisterDirect(ctx, harness.Instance, reg("")); return err }(), -32602, ""},
		{"an endpoint that does not exist, unregistered",
			e.UnregisterDirect(ctx, harness.Instance, adapterproto.UnregisterParams{Endpoint: unknownEndpointRef(t)}), 1001, ""},
		{"a checkpoint of a session that does not exist",
			e.CheckpointPut(ctx, harness.Instance, adapterproto.CheckpointPutParams{
				Scope: adapterproto.ScopeSession, ID: string(unknownEndpointRef(t)), State: json.RawMessage(`{}`)}), 1001, ""},
		{"a checkpoint that is not valid",
			e.CheckpointPut(ctx, harness.Instance, adapterproto.CheckpointPutParams{
				Scope: adapterproto.ScopeAttempt, ID: "not an attempt", State: json.RawMessage(`{}`)}), -32602, ""},
		{"a checkpoint of an attempt that does not exist",
			e.CheckpointPut(ctx, harness.Instance, adapterproto.CheckpointPutParams{
				Scope: adapterproto.ScopeAttempt, ID: "11111111-2222-4333-8444-555555555555", State: json.RawMessage(`{}`)}), 1004, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pe := wireError(t, tc.err)
			if pe.Code != tc.code || string(pe.Data) != tc.data {
				t.Errorf("code %d data %q (%s), want %d %q", pe.Code, pe.Data, pe.Message, tc.code, tc.data)
			}
		})
	}
}

// A request that arrives after the engine began to stop is told so with the code
// the peer uses for it, and a request that the engine did answer is passed on as
// it was.
func TestAnEngineThatIsStoppingAnswersShuttingDownAndAnAnsweredRequestIsPassedOn(t *testing.T) {
	h := harness.New(t, harness.Options{})
	e := apiwire.AdapterEngine(h.Engine())
	ctx := context.Background()
	res, err := e.RegisterDirect(ctx, harness.Instance, adapterproto.RegisterParams{Key: "notes", DisplayName: "Notes"})
	if err != nil || res.Endpoint == "" {
		t.Fatalf("RegisterDirect = %+v, %v", res, err)
	}
	if err := e.UnregisterDirect(ctx, harness.Instance, adapterproto.UnregisterParams{Endpoint: res.Endpoint}); err != nil {
		t.Fatalf("UnregisterDirect: %v", err)
	}
	h.Engine().BeginShutdown()
	_, err = e.RegisterDirect(ctx, harness.Instance, adapterproto.RegisterParams{Key: "more", DisplayName: "More"})
	if pe := wireError(t, err); pe.Code != peer.CodeShuttingDown {
		t.Errorf("code %d, want %d", pe.Code, peer.CodeShuttingDown)
	}
}

// The host answers endpoint.send and its kin through the optional interfaces of
// the engine it is given, and an engine that has them answers with its own errors.
// What the wrapper does not map must not reach the host by being promoted from the
// engine: it would be answered with the engine's words.
func TestTheEngineBehindTheHostOffersNoOptionalPartThatItDoesNotMap(t *testing.T) {
	h := harness.New(t, harness.Options{})
	var e adapterhost.Engine = apiwire.AdapterEngine(h.Engine())
	if _, ok := e.(adapterhost.EndpointCalls); ok {
		t.Error("the engine behind the host offers EndpointCalls, with no mapping of its refusals")
	}
}

// recv.observed is the one optional part the wrapper maps: a refusal of the engine
// reaches the adapter as the code the protocol names for it.
func TestRecvObservedRefusalsAreMappedForTheAdapter(t *testing.T) {
	h := harness.New(t, harness.Options{})
	oc, ok := apiwire.AdapterEngine(h.Engine()).(adapterhost.ObservedCalls)
	if !ok {
		t.Fatal("the engine behind the host does not offer ObservedCalls")
	}
	_, err := oc.RecvObserved(h.Ctx(), harness.Instance, adapterproto.RecvObservedParams{
		Endpoint: "ep1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq", Lease: "00000000000000000000000000000001",
		Frontend: adapterproto.FrontendCLI, Observed: "text",
	})
	if pe := wireError(t, err); pe.Code != adapterproto.CodeUnknownEndpoint {
		t.Errorf("code %d, want %d", pe.Code, adapterproto.CodeUnknownEndpoint)
	}
}
