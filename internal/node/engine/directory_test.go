package engine_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func directoryCaller(t testing.TB, h *harness.H, name string) engine.Caller {
	t.Helper()
	a, err := ident.ParseAddress(h.Addr(name))
	if err != nil {
		t.Fatal(err)
	}
	return engine.ByAdapter(harness.Instance, adapterproto.EndpointRef(a.Endpoint.String()))
}

func targetDirectory(t testing.TB, h *harness.H, name string, req transport.DirectoryRequest) (transport.DirectoryPage, error) {
	t.Helper()
	from, err := ident.ParseAddress(h.Addr(name))
	if err != nil {
		t.Fatal(err)
	}
	return transport.NewLoopback(harness.NodeID(), h.Engine(), nil).QueryDirectory(h.Ctx(), harness.NodeID(), from.Endpoint, req)
}

func TestDirectoryUsesTransportOriginAndListsOnlyOpenVisibleEndpoints(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "asker", "shown", "hidden", "closed")
	e := h.Engine()
	if _, err := e.Declare(h.Ctx(), directoryCaller(t, h, "hidden"), engine.DeclarationPatch{HiddenFromDirectory: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	closed, _ := ident.ParseAddress(h.Addr("closed"))
	if _, err := e.CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: closed.Endpoint}); err != nil {
		t.Fatal(err)
	}
	got, err := targetDirectory(t, h, "asker", transport.DirectoryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{h.Addr("asker"), h.Addr("shown")}
	slices.Sort(want)
	var addresses []string
	for _, ep := range got.Endpoints {
		addresses = append(addresses, ep.Address.String())
		if ep.Kind != adapterreg.KindDirect || !slices.Equal(ep.Accepts, []authz.Action{authz.ActionMessageSend, authz.ActionEndpointStatus}) {
			t.Errorf("directory entry = %+v, want direct with its known signals", ep)
		}
	}
	if !slices.Equal(addresses, want) || got.NextCursor != "" {
		t.Errorf("directory = %v, next %q; want %v without another page", addresses, got.NextCursor, want)
	}
	from, _ := ident.ParseAddress(h.Addr("asker"))
	remote, err := e.QueryDirectory(h.Ctx(), transport.RemoteOrigin(harness.NodeID()), from.Endpoint, transport.DirectoryRequest{})
	if err != nil || len(remote.Endpoints) != 0 {
		t.Errorf("remote origin claiming this node = %+v, %v; must not gain local visibility", remote, err)
	}
}

func TestDirectoryPagesAreLiveAndCursorsBelongToTheCallerAndProcess(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "asker", "a", "b")
	e := h.Engine()
	first, err := targetDirectory(t, h, "asker", transport.DirectoryRequest{Limit: 1})
	if err != nil || len(first.Endpoints) != 1 || first.NextCursor == "" {
		t.Fatalf("first page = %+v, %v, want one entry and a continuation", first, err)
	}
	want := []string{h.Addr("asker"), h.Addr("a"), h.Addr("b")}
	slices.Sort(want)
	if first.Endpoints[0].Address.String() != want[0] {
		t.Fatalf("first address = %v, want %v", first.Endpoints[0].Address, want[0])
	}
	_, err = targetDirectory(t, h, "a", transport.DirectoryRequest{Limit: 1, Cursor: first.NextCursor})
	if r := wantReason(t, err, engine.ReasonInvalidArgument); r.Detail.String() != "cursor_invalid" {
		t.Errorf("another caller's cursor = %v, want cursor_invalid", r)
	}
	hidden, _ := ident.ParseAddress(want[1])
	if _, err := e.Declare(h.Ctx(), engine.ByAdapter(harness.Instance, adapterproto.EndpointRef(hidden.Endpoint.String())), engine.DeclarationPatch{HiddenFromDirectory: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	second, err := targetDirectory(t, h, "asker", transport.DirectoryRequest{Limit: 1, Cursor: first.NextCursor})
	if err != nil || len(second.Endpoints) != 1 || second.Endpoints[0].Address.String() != want[2] || second.NextCursor != "" {
		t.Errorf("second live page = %+v, %v, want only %v after the newly hidden entry", second, err, want[2])
	}
	h.Restart()
	_, err = targetDirectory(t, h, "asker", transport.DirectoryRequest{Cursor: first.NextCursor})
	if r := wantReason(t, err, engine.ReasonInvalidArgument); r.Detail.String() != "cursor_invalid" {
		t.Errorf("cursor after restart = %v, want cursor_invalid", r)
	}
}

func TestDirectoryRefusesUnavailableStatesAndStillListsUnadmittedOpenEndpoints(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	old := r.start()
	id := register(t, old, "reader")
	r.restart()
	e, _, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	e.SetTransport(transport.NewLoopback(r.node, e, nil))
	e.Start()
	r.eng = e
	t.Cleanup(func() { r.stop(e) })
	_, err = transport.NewLoopback(r.node, e, nil).QueryDirectory(context.Background(), r.node, id, transport.DirectoryRequest{})
	wantReason(t, err, engine.ReasonRecovering)
	if err := e.RecoverDelivery(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Serve()
	page, err := transport.NewLoopback(r.node, e, nil).QueryDirectory(context.Background(), r.node, id, transport.DirectoryRequest{})
	if err != nil || len(page.Endpoints) != 1 || !slices.Equal(page.Endpoints[0].Accepts, []authz.Action{authz.ActionMessageSend, authz.ActionEndpointStatus}) {
		t.Fatalf("open endpoint before admission = %+v, %v; want its kind-only signals", page, err)
	}
	r.clk.Set(r.clk.Now().Add(-time.Hour))
	_, err = transport.NewLoopback(r.node, e, nil).QueryDirectory(context.Background(), r.node, id, transport.DirectoryRequest{})
	wantReason(t, err, engine.ReasonClockRollback)
	e.BeginShutdown()
	_, err = transport.NewLoopback(r.node, e, nil).QueryDirectory(context.Background(), r.node, id, transport.DirectoryRequest{})
	wantReason(t, err, engine.ReasonShuttingDown)
}

func FuzzDirectoryCursor(f *testing.F) {
	h := harness.New(f, harness.Options{})
	h.MustDirect("reader")
	h.MustDirect("other")
	from, err := ident.ParseAddress(h.Addr("reader"))
	if err != nil {
		f.Fatal(err)
	}
	ctx := h.Ctx()
	tr := transport.NewLoopback(harness.NodeID(), h.Engine(), nil)
	first, err := tr.QueryDirectory(ctx, harness.NodeID(), from.Endpoint, transport.DirectoryRequest{Limit: 1})
	if err != nil || first.NextCursor == "" {
		f.Fatalf("initial page = %+v, %v", first, err)
	}
	f.Add("")
	f.Add(first.NextCursor)
	f.Add("invalid")
	f.Add(first.NextCursor + "\n")
	f.Fuzz(func(t *testing.T, cursor string) {
		_, err := tr.QueryDirectory(ctx, harness.NodeID(), from.Endpoint, transport.DirectoryRequest{Cursor: cursor, Limit: 1})
		if err != nil {
			r := wantReason(t, err, engine.ReasonInvalidArgument)
			if r.Detail.String() != "cursor_invalid" {
				t.Fatalf("cursor returned %v, want cursor_invalid", r)
			}
		}
	})
}
