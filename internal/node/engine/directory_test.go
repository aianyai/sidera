package engine_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/clock"
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

// A distinct duration separates the directory timer from adapter and loop timers.
const directoryTestWait = 7*time.Second + 13*time.Millisecond

type directoryWaitClock struct {
	clock.Clock
	armed chan struct{}
}

func (c *directoryWaitClock) NewTimer(d time.Duration) clock.Timer {
	timer := c.Clock.NewTimer(d)
	if d == directoryTestWait {
		c.armed <- struct{}{}
	}
	return timer
}

// heldDirectory is a transport that answers its first query late, even after
// cancellation. Subsequent queries use the real loopback and target.
type heldDirectory struct {
	engine.Transport
	began chan struct{}
	late  chan struct{}
	once  sync.Once
}

func (h *heldDirectory) QueryDirectory(ctx context.Context, node ident.NodeID, sender ident.EndpointID, req transport.DirectoryRequest) (transport.DirectoryPage, error) {
	held := false
	h.once.Do(func() { held = true })
	if held {
		close(h.began)
		<-h.late
		return transport.DirectoryPage{}, nil
	}
	return h.Transport.QueryDirectory(ctx, node, sender, req)
}

func directoryRig(t *testing.T) (*rig, *engine.Engine, engine.Caller, *directoryWaitClock, *heldDirectory, func()) {
	t.Helper()
	noLeaks(t)
	r := newRig(t, 10)
	r.params.DirectoryWait = directoryTestWait
	c := &directoryWaitClock{Clock: r.clk, armed: make(chan struct{}, 8)}
	r.engineClock = c
	e, _, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	tr := &heldDirectory{Transport: transport.NewLoopback(r.node, e, nil), began: make(chan struct{}), late: make(chan struct{})}
	e.SetTransport(tr)
	e.Start()
	if err := e.RecoverDelivery(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Serve()
	if err := admitOn(context.Background(), e, "chat", r.admit); err != nil {
		t.Fatal(err)
	}
	r.eng = e
	t.Cleanup(func() { r.stop(e) })
	var once sync.Once
	release := func() { once.Do(func() { close(tr.late) }) }
	t.Cleanup(release)
	id := register(t, e, "reader")
	return r, e, engineCaller(id), c, tr, release
}

type directoryAnswer struct {
	page engine.DirectoryPage
	err  error
}

func directoryAsync(e *engine.Engine, caller engine.Caller) <-chan directoryAnswer {
	done := make(chan directoryAnswer, 1)
	go func() {
		page, err := e.Directory(context.Background(), caller, engine.DirectoryQuery{})
		done <- directoryAnswer{page, err}
	}()
	return done
}

func directoryArmed(t testing.TB, c *directoryWaitClock) {
	t.Helper()
	select {
	case <-c.armed:
	case <-time.After(10 * time.Second):
		t.Fatal("directory did not arm its duration timer")
	}
}

func directoryResult(t testing.TB, done <-chan directoryAnswer) directoryAnswer {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("directory did not return within its budget")
		return directoryAnswer{}
	}
}

func TestDirectoryClockRollbackAfterQueuingStillEndsAtTheBudget(t *testing.T) {
	r, e, caller, c, tr, release := directoryRig(t)
	first := directoryAsync(e, caller)
	directoryArmed(t, c)
	select {
	case <-tr.began:
	case <-time.After(10 * time.Second):
		t.Fatal("first query did not reach the transport")
	}
	queued := directoryAsync(e, caller)
	directoryArmed(t, c)
	// This later loop command is a barrier: the second query is now queued
	// behind the transport call, before the wall clock is set back.
	if _, err := e.Endpoints(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.clk.Set(r.clk.Now().Add(-time.Hour))
	if _, err := e.NodeSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantReason(t, directoryResult(t, directoryAsync(e, caller)).err, engine.ReasonClockRollback)
	r.clk.Advance(directoryTestWait)
	wantReason(t, directoryResult(t, queued).err, engine.ReasonDirectoryUnavailable)
	wantReason(t, directoryResult(t, first).err, engine.ReasonDirectoryUnavailable)
	release()
}

func TestDirectoryTimeoutIgnoresALateTransportResultAndKeepsServing(t *testing.T) {
	r, e, caller, c, tr, release := directoryRig(t)
	done := directoryAsync(e, caller)
	directoryArmed(t, c)
	select {
	case <-tr.began:
	case <-time.After(10 * time.Second):
		t.Fatal("query did not reach the transport")
	}
	r.clk.Advance(directoryTestWait)
	wantReason(t, directoryResult(t, done).err, engine.ReasonDirectoryUnavailable)
	release()
	result := directoryResult(t, directoryAsync(e, caller))
	if result.err != nil || len(result.page.Endpoints) != 1 {
		t.Fatalf("query after the late result = %+v, %v, want the reader's open endpoint", result.page, result.err)
	}
}

func TestDirectoryUsesTransportOriginAndListsOnlyOpenVisibleEndpoints(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "asker", "shown", "hidden", "closed")
	e := h.Engine()
	asker := directoryCaller(t, h, "asker")
	if _, err := e.Declare(h.Ctx(), directoryCaller(t, h, "hidden"), engine.DeclarationPatch{HiddenFromDirectory: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	closed, _ := ident.ParseAddress(h.Addr("closed"))
	if _, err := e.CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: closed.Endpoint}); err != nil {
		t.Fatal(err)
	}
	got, err := e.Directory(h.Ctx(), asker, engine.DirectoryQuery{})
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
	asker := directoryCaller(t, h, "asker")
	first, err := e.Directory(h.Ctx(), asker, engine.DirectoryQuery{Limit: 1})
	if err != nil || len(first.Endpoints) != 1 || first.NextCursor == "" {
		t.Fatalf("first page = %+v, %v, want one entry and a continuation", first, err)
	}
	want := []string{h.Addr("asker"), h.Addr("a"), h.Addr("b")}
	slices.Sort(want)
	if first.Endpoints[0].Address.String() != want[0] {
		t.Fatalf("first address = %v, want %v", first.Endpoints[0].Address, want[0])
	}
	_, err = e.Directory(h.Ctx(), directoryCaller(t, h, "a"), engine.DirectoryQuery{Limit: 1, Cursor: first.NextCursor})
	if r := wantReason(t, err, engine.ReasonInvalidArgument); r.Detail.String() != "cursor_invalid" {
		t.Errorf("another caller's cursor = %v, want cursor_invalid", r)
	}
	hidden, _ := ident.ParseAddress(want[1])
	if _, err := e.Declare(h.Ctx(), engine.ByAdapter(harness.Instance, adapterproto.EndpointRef(hidden.Endpoint.String())), engine.DeclarationPatch{HiddenFromDirectory: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	second, err := e.Directory(h.Ctx(), asker, engine.DirectoryQuery{Limit: 1, Cursor: first.NextCursor})
	if err != nil || len(second.Endpoints) != 1 || second.Endpoints[0].Address.String() != want[2] || second.NextCursor != "" {
		t.Errorf("second live page = %+v, %v, want only %v after the newly hidden entry", second, err, want[2])
	}
	h.Restart()
	_, err = h.Engine().Directory(h.Ctx(), asker, engine.DirectoryQuery{Cursor: first.NextCursor})
	if r := wantReason(t, err, engine.ReasonInvalidArgument); r.Detail.String() != "cursor_invalid" {
		t.Errorf("cursor after restart = %v, want cursor_invalid", r)
	}
}

func TestDirectoryRefusesUnavailableStatesAndStillListsUnadmittedOpenEndpoints(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	old := r.start()
	id := register(t, old, "reader")
	caller := engineCaller(id)
	r.restart()
	e, _, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	e.SetTransport(transport.NewLoopback(r.node, e, nil))
	e.Start()
	r.eng = e
	t.Cleanup(func() { r.stop(e) })
	_, err = e.Directory(context.Background(), caller, engine.DirectoryQuery{})
	wantReason(t, err, engine.ReasonRecovering)
	if err := e.RecoverDelivery(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Serve()
	page, err := e.Directory(context.Background(), caller, engine.DirectoryQuery{})
	if err != nil || len(page.Endpoints) != 1 || !slices.Equal(page.Endpoints[0].Accepts, []authz.Action{authz.ActionMessageSend, authz.ActionEndpointStatus}) {
		t.Fatalf("open endpoint before admission = %+v, %v; want its kind-only signals", page, err)
	}
	_, err = e.Directory(context.Background(), caller, engine.DirectoryQuery{Node: mustNode(t, 42)})
	wantReason(t, err, engine.ReasonDirectoryUnavailable)
	r.clk.Set(r.clk.Now().Add(-time.Hour))
	_, err = e.Directory(context.Background(), caller, engine.DirectoryQuery{})
	wantReason(t, err, engine.ReasonClockRollback)
	e.BeginShutdown()
	_, err = e.Directory(context.Background(), caller, engine.DirectoryQuery{})
	wantReason(t, err, engine.ReasonShuttingDown)
}

func FuzzDirectoryCursor(f *testing.F) {
	h := harness.New(f, harness.Options{})
	h.MustDirect("reader")
	h.MustDirect("other")
	asker := directoryCaller(f, h, "reader")
	e, ctx := h.Engine(), h.Ctx()
	first, err := e.Directory(ctx, asker, engine.DirectoryQuery{Limit: 1})
	if err != nil || first.NextCursor == "" {
		f.Fatalf("initial page = %+v, %v", first, err)
	}
	f.Add("")
	f.Add(first.NextCursor)
	f.Add("invalid")
	f.Add(first.NextCursor + "\n")
	f.Fuzz(func(t *testing.T, cursor string) {
		_, err := e.Directory(ctx, asker, engine.DirectoryQuery{Cursor: cursor, Limit: 1})
		if err != nil {
			r := wantReason(t, err, engine.ReasonInvalidArgument)
			if r.Detail.String() != "cursor_invalid" {
				t.Fatalf("cursor returned %v, want cursor_invalid", r)
			}
		}
	})
}
