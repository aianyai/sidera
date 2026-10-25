package engine_test

import (
	"context"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Closing an endpoint is a head record and then the closing of what is queued
// for it, chunk by chunk, so that the loop is never held for the whole queue.
func TestCloseRejectsTheQueuedDeliveriesInChunksAndReleasesTheirBodies(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10, withParams(engine.Params{ChunkReplaces: 2}))
	e := r.start()
	a, b := register(t, e, "a"), register(t, e, "b")
	r.restart()
	r.queue(1, 5, a)
	r.queue(10, 1, b)
	e = r.start()

	res, err := e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: a})
	if err != nil {
		t.Fatalf("CloseEndpoint: %v", err)
	}
	if res.Outcome != engine.CloseOutcomeClosed {
		t.Errorf("outcome %v, want closed for a direct endpoint", res.Outcome)
	}
	settle(t, e)

	sc := r.scan()
	rejected := 0
	for _, in := range sc.In {
		switch in.Key.Recipient {
		case a:
			if in.Record.State != delivery.StateRejected || in.Record.Reject != delivery.RejectEndpointClosed || in.Body != nil {
				t.Errorf("delivery %s for the closed endpoint: state %v reason %v body %v", in.Key.Message, in.Record.State, in.Record.Reject, in.Body)
			}
			rejected++
		case b:
			if in.Record.State != delivery.StateAccepted || in.Body == nil {
				t.Errorf("delivery %s for the other endpoint changed: state %v", in.Key.Message, in.Record.State)
			}
		}
	}
	if rejected != 5 {
		t.Errorf("%d deliveries of the closed endpoint rejected, want 5", rejected)
	}
	if got := r.commits("endpoint-drain"); got != 3 {
		t.Errorf("the drain took %d transactions, want 3 chunks of at most 2 for 5 records", got)
	}
	if bodies := store.OrphanBodies(sc); len(bodies) != 0 {
		t.Errorf("orphan bodies %v", bodies)
	}
	if len(sc.Bodies) != 1 {
		t.Errorf("%d bodies left, want only the one of the other endpoint", len(sc.Bodies))
	}
	for _, ep := range sc.Endpoints {
		if ep.ID == a && (ep.Status != store.EndpointClosed || ep.Token != nil) {
			t.Errorf("closed endpoint record: status %v token %v", ep.Status, ep.Token)
		}
	}
}

func TestCloseTwiceIsTheSameAnswer(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	a := register(t, e, "a")
	if res, err := e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: a}); err != nil || res.Outcome != engine.CloseOutcomeClosed {
		t.Fatalf("first close: %+v, %v", res, err)
	}
	res, err := e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: a})
	if err != nil || res.Outcome != engine.CloseOutcomeAlreadyClosed {
		t.Errorf("second close: %+v, %v; want already closed", res, err)
	}
}

// The result of the operation that asked for the close goes in the same
// transaction as the head record: a crash cannot leave one without the other.
func TestCloseStoresTheResultOfTheOperationWithTheHeadRecord(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	a := register(t, e, "a")
	var op admin.OperationID
	op[0] = 42
	res := store.AdminRecord{
		Key: admin.KeyFor(admin.LocalOwner(), op), Op: "endpoint.close", Result: []byte(`{"outcome":"closed"}`),
		At: t0, RetainUntil: t0.Add(store.AdminResultRetention),
	}
	if _, err := e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: a, Result: &res}); err != nil {
		t.Fatal(err)
	}
	sc := r.scan()
	if len(sc.Admin) != 1 || sc.Admin[0].Key != res.Key || string(sc.Admin[0].Result) != string(res.Result) {
		t.Errorf("stored results %+v, want the one given", sc.Admin)
	}
}

// A closed endpoint is not opened again by the registration of its key until
// the deliveries queued for it are closed: they would land on an endpoint that
// is open again and be handed to an agent that never was their addressee.
func TestRegisteringTheKeyOfAnEndpointStillDrainingIsRefused(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10, withParams(engine.Params{ChunkReplaces: 1}))
	e := r.start()
	a := register(t, e, "a")
	r.restart()
	r.queue(1, 6, a)
	e = r.start()

	// Hold the loop, put the close and then the registration in its inbox in this
	// order, and let it run: the close commits its head and leaves the chunks to
	// the continuations, which wait behind what is already in the inbox.
	release := engine.StallLoop(t, e)
	closed := make(chan error, 1)
	go func() {
		_, err := e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: a})
		closed <- err
	}()
	engine.WaitInbox(t, e, 1)
	registered := make(chan error, 1)
	go func() {
		_, err := e.RegisterDirect(context.Background(), "chat", registerParams("a"))
		registered <- err
	}()
	engine.WaitInbox(t, e, 2)
	release()

	if err := <-closed; err != nil {
		t.Fatalf("CloseEndpoint: %v", err)
	}
	wantReason(t, <-registered, engine.ReasonEndpointDraining)
	settle(t, e)
	if id := register(t, e, "a"); id != a {
		t.Errorf("after the drain the key gave %s, want the same endpoint %s again", id, a)
	}
	if got := openEndpoints(t, e); got != 1 {
		t.Errorf("%d endpoints open, want the one that was opened again", got)
	}
}

var _ = engine.Reasons

// An adapter unregisters only its own direct endpoints, and what it unregistered
// is closed like one the owner closed.
func TestUnregisterClosesADirectEndpointOfTheInstanceThatOwnsIt(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	a := register(t, e, "a")
	b := register(t, e, "b")
	ctx := context.Background()

	err := e.UnregisterDirect(ctx, "other", adapterproto.UnregisterParams{Endpoint: adapterproto.EndpointRef(a.String())})
	wantReason(t, err, engine.ReasonInstanceUnknown)
	err = e.UnregisterDirect(ctx, "chat", adapterproto.UnregisterParams{Endpoint: adapterproto.EndpointRef(mustEndpoint(t, 77).String())})
	wantReason(t, err, engine.ReasonEndpointUnknown)
	err = e.UnregisterDirect(ctx, "chat", adapterproto.UnregisterParams{Endpoint: "nonsense"})
	wantReason(t, err, engine.ReasonEndpointUnknown)

	if err := e.UnregisterDirect(ctx, "chat", adapterproto.UnregisterParams{Endpoint: adapterproto.EndpointRef(a.String())}); err != nil {
		t.Fatalf("UnregisterDirect: %v", err)
	}
	settle(t, e)
	if got := openEndpoints(t, e); got != 1 {
		t.Errorf("%d endpoints open, want only b (%s)", got, b)
	}
	err = e.UnregisterDirect(ctx, "chat", adapterproto.UnregisterParams{Endpoint: adapterproto.EndpointRef(a.String())})
	wantReason(t, err, engine.ReasonEndpointUnknown)
}

// The id of a key is kept for ever, and a token is not: an endpoint that is
// opened again by its key is the same endpoint with a token that has never been
// given out before.
func TestReopeningAnEndpointKeepsItsIdAndGivesItANewToken(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	a := register(t, e, "a")
	tokenOf := func() []byte {
		for _, ep := range r.scan().Endpoints {
			if ep.ID == a {
				return ep.Token
			}
		}
		return nil
	}
	first := tokenOf()
	if len(first) != store.TokenLen {
		t.Fatalf("token of %d bytes, want %d", len(first), store.TokenLen)
	}
	if _, err := e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: a}); err != nil {
		t.Fatal(err)
	}
	settle(t, e)
	if tokenOf() != nil {
		t.Error("a closed endpoint keeps a token")
	}
	if id := register(t, e, "a"); id != a {
		t.Fatalf("the key gave %s, want %s", id, a)
	}
	second := tokenOf()
	if len(second) != store.TokenLen || string(second) == string(first) {
		t.Errorf("reopened with token %x after %x, want a new one", second, first)
	}
}
