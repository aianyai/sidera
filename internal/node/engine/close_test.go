package engine_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
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
	if diff, err := engine.ViewDiffersFromDisk(context.Background(), e, sc); err != nil || len(diff) != 0 {
		t.Errorf("the view of the engine differs from the disk after the drain: %v, %v", diff, err)
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

// Closing an endpoint that is closed already changes nothing and says so; the
// management interface maps the word to its own.
func TestClosingAnEndpointThatIsClosedAlreadySaysSo(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	a := register(t, e, "a")
	if res, err := e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: a}); err != nil || res.Outcome != engine.CloseOutcomeClosed {
		t.Fatalf("first close: %+v, %v", res, err)
	}
	res, err := e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: a})
	if err != nil || res.Outcome != engine.CloseOutcomeAlreadyClosed || res.Result != nil {
		t.Errorf("second close: %+v, %v; want already closed and no stored result", res, err)
	}
	if got := r.commits("endpoint-close"); got != 1 {
		t.Errorf("%d transactions closed the endpoint, want the first only", got)
	}
}

// closeOp is the operation that closes an endpoint, as the management interface
// builds it: the identifier of the request, the fingerprint of its content and the
// shape of its result.
func closeOp(b byte, content string) *engine.CloseOperation {
	var op admin.OperationID
	op[0] = b
	return &engine.CloseOperation{
		Key: admin.KeyFor(admin.LocalOwner(), op), Fingerprint: sha256.Sum256([]byte(content)), Name: "endpoints.close",
		Encode: func(o engine.CloseOutcome) ([]byte, error) { return []byte(`{"outcome":"` + o.String() + `"}`), nil },
	}
}

// adminResults is the results of administrative operations on disk, by key.
func (r *rig) adminResults() map[admin.RequestKey]store.AdminRecord {
	out := map[admin.RequestKey]store.AdminRecord{}
	for _, a := range r.scan().Admin {
		out[a.Key] = a
	}
	return out
}

// The result of the operation that asked for the close goes in the same
// transaction as the head record, and it is the result of what the close did: a
// crash cannot leave one without the other.
func TestCloseStoresTheResultOfTheOperationWithTheHeadRecord(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	a := register(t, e, "a")
	before := r.scan().Admin // what the setting up of the node wrote
	op := closeOp(42, "close a")
	res, err := e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: a, Operation: op})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Result) != `{"outcome":"closed"}` || res.Replayed {
		t.Errorf("answered %+v, want the result of the close", res)
	}
	after := r.scan().Admin
	if len(after) != len(before)+1 {
		t.Fatalf("%d stored results, want the %d of the setting up and exactly one for the close", len(after), len(before))
	}
	found, ok := r.adminResults()[op.Key]
	if !ok || string(found.Result) != `{"outcome":"closed"}` || found.Op != "endpoints.close" || found.Fingerprint != op.Fingerprint {
		t.Errorf("the stored result is %+v (found %v)", found, ok)
	}
	if got := r.commits("endpoint-close"); got != 1 {
		t.Errorf("%d transactions, want one for the head record and the result together", got)
	}
}

// What is stored is what the loop did, not what the caller guessed: a session
// endpoint says closing, because the session behind it is still to be released.
func TestTheStoredResultSaysWhatTheCloseDidForEveryKindOfEndpoint(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	direct := register(t, e, "a")
	session := mustEndpoint(t, 80)
	r.restart()
	r.seedRecord(store.EndpointRecord{
		ID: session, Kind: sessionKind, Instance: "chat", Status: store.EndpointOpen,
		Token: bytes.Repeat([]byte{0x5a}, store.TokenLen), Session: &store.SessionInfo{Preset: "default"},
	})
	e = r.start()
	for _, tc := range []struct {
		name string
		id   ident.EndpointID
		op   *engine.CloseOperation
		want string
	}{
		{"a direct endpoint", direct, closeOp(101, "a"), "closed"},
		{"a session endpoint", session, closeOp(102, "b"), "closing"},
		{"an endpoint that is closed already", direct, closeOp(103, "c"), "already_closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: tc.id, Operation: tc.op})
			if err != nil {
				t.Fatal(err)
			}
			want := `{"outcome":"` + tc.want + `"}`
			if string(res.Result) != want {
				t.Errorf("answered %s, want %s", res.Result, want)
			}
			if got := r.adminResults()[tc.op.Key]; string(got.Result) != want {
				t.Errorf("stored %s, want %s", got.Result, want)
			}
		})
	}
}

// One identifier has one result: a resend of the request returns the stored
// result without closing or writing anything, and the identifier of another
// request is refused. Without the replay check the second close would store
// already_closed over the answer the first one gave.
func TestAResendOfAClosingReturnsTheStoredResultAndAReusedIdentifierIsRefused(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	a := register(t, e, "a")
	ctx := context.Background()
	first, err := e.CloseEndpoint(ctx, engine.CloseRequest{Endpoint: a, Operation: closeOp(7, "close a")})
	if err != nil || string(first.Result) != `{"outcome":"closed"}` {
		t.Fatalf("first: %+v, %v", first, err)
	}
	before := r.snapshot()

	again, err := e.CloseEndpoint(ctx, engine.CloseRequest{Endpoint: a, Operation: closeOp(7, "close a")})
	if err != nil || !again.Replayed || string(again.Result) != `{"outcome":"closed"}` {
		t.Errorf("a resend answered %+v, %v; want the stored result, replayed", again, err)
	}
	_, err = e.CloseEndpoint(ctx, engine.CloseRequest{Endpoint: a, Operation: closeOp(7, "close b")})
	wantReason(t, err, engine.ReasonOperationIDReused)
	if !reflect.DeepEqual(before, r.snapshot()) {
		t.Error("a resend and a reused identifier wrote to the disk")
	}
	// The stored result is still what the first close said, after a restart too.
	r.restart()
	e = r.start()
	again, err = e.CloseEndpoint(ctx, engine.CloseRequest{Endpoint: a, Operation: closeOp(7, "close a")})
	if err != nil || !again.Replayed || string(again.Result) != `{"outcome":"closed"}` {
		t.Errorf("after a restart a resend answered %+v, %v", again, err)
	}
}

// A closed endpoint is not opened again by the registration of its key until
// the deliveries queued for it are closed: they would land on an endpoint that
// is open again and be handed to an agent that never was their addressee. The
// registration is not refused for it: it waits for the drain, and then runs.
func TestRegisteringTheKeyOfAnEndpointStillDrainingWaitsForTheDrain(t *testing.T) {
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
	type answer struct {
		res adapterproto.RegisterResult
		err error
	}
	registered := make(chan answer, 1)
	go func() {
		res, err := e.RegisterDirect(context.Background(), "chat", registerParams("a"))
		registered <- answer{res, err}
	}()
	engine.WaitInbox(t, e, 2)
	release()

	if err := <-closed; err != nil {
		t.Fatalf("CloseEndpoint: %v", err)
	}
	got := <-registered
	if got.err != nil || got.res.Endpoint != adapterproto.EndpointRef(a.String()) {
		t.Fatalf("the registration answered %+v, %v; want the same endpoint again", got.res, got.err)
	}
	// It was answered after the drain, not before: nothing is queued for the
	// endpoint that is open again, and no delivery of the old one was left
	// accepted.
	for _, in := range r.scan().In {
		if in.Record.State != delivery.StateRejected {
			t.Errorf("when the registration was answered, record %s was %v; the drain had not finished", in.Key.Message, in.Record.State)
		}
	}
	settle(t, e)
	if got := openEndpoints(t, e); got != 1 {
		t.Errorf("%d endpoints open, want the one that was opened again", got)
	}
}

// A drain whose chunk cannot be written is tried again after the back off. The
// registration that waits for it waits too, and does not spin in the loop.
func TestARegistrationWaitsAcrossAFailedChunkOfTheDrain(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10, withParams(engine.Params{ChunkReplaces: 1, RetryBackoff: 10 * time.Second}))
	e := r.start()
	a := register(t, e, "a")
	r.restart()
	r.queue(1, 3, a)
	e = r.start()

	rule := r.faulty.Fail(matchT{Tx: "endpoint-drain", Step: fsx.StepWrite, Hit: 1}, syscall.EIO) // the first write of a chunk, once
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
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return rule.Hits() > 0 })
	// Everything the engine can do now is done: the retry is not due, and the
	// registration waits for it.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if rep, err := e.Quiesce(ctx, engine.Expect{}); err == nil || rep.Parked != 1 || rep.Continuations != 0 {
		t.Fatalf("Quiesce = %+v, %v; want the registration parked, nothing queued, not quiet", rep, err)
	}
	select {
	case err := <-registered:
		t.Fatalf("the registration was answered (%v) before the drain was done", err)
	default:
	}
	r.clk.Advance(10 * time.Second)
	settle(t, e)
	if err := <-registered; err != nil {
		t.Errorf("the registration after the retry: %v", err)
	}
	for _, in := range r.scan().In {
		if in.Record.State != delivery.StateRejected {
			t.Errorf("record %s is %v after the drain", in.Key.Message, in.Record.State)
		}
	}
}

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

// The view is kept in step with what the store hands back from each write, and
// must show what a fresh scan of the disk shows: records by their encoding (the
// revision included), the count of unfinished deliveries and the references to
// bodies. The expectation is the scan, never the engine's own computation, and
// the closing is run in chunks so that several writes have been applied to the
// view by the time it is compared.
func TestViewMatchesAFreshScanAfterWrites(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10, withParams(engine.Params{ChunkReplaces: 2}))
	e := r.start()
	a := register(t, e, "a")
	r.restart()
	r.queue(1, 5, a)
	e = r.start()
	if diff, err := engine.ViewDiffersFromDisk(context.Background(), e, r.scan()); err != nil || len(diff) != 0 {
		t.Fatalf("before any write: %v, %v", diff, err)
	}
	if _, err := e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: a}); err != nil {
		t.Fatal(err)
	}
	settle(t, e)
	diff, err := engine.ViewDiffersFromDisk(context.Background(), e, r.scan())
	if err != nil || len(diff) != 0 {
		t.Errorf("after closing the endpoint: %v, %v", diff, err)
	}
	for _, in := range r.scan().In {
		if in.Rev != 2 { // the write that queued it, and the write that closed it
			t.Errorf("record %s has revision %d on disk, want 2", in.Key.Message, in.Rev)
		}
	}
}
