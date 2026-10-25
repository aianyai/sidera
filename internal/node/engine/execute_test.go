package engine_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
)

// The management operations all run through one door, Execute, and the order the
// administrative rules fix for a side-effecting request is the engine's alone:
// look the identifier up in every stored result, run the operation, store the
// result with the fingerprint. These tests drive that door with the two
// operations the engine has (closing an endpoint, stopping the daemon) and with
// a versioned change of the configuration, so the rules are shown on each place a
// result can be kept.

func requestKey(b byte) admin.RequestKey {
	var id admin.OperationID
	id[0] = b
	return admin.KeyFor(admin.LocalOwner(), id)
}

func fingerprintOf(s string) admin.Fingerprint { return sha256.Sum256([]byte(s)) }

func encodeClose(o engine.CloseOutcome) ([]byte, error) {
	return []byte(`{"outcome":"` + o.String() + `"}`), nil
}

func encodeStop() ([]byte, error) { return []byte(`{"stopping":true}`), nil }

// closeCommand is a request to close an endpoint, as the management interface
// builds it.
func closeCommand(b byte, content string, id ident.EndpointID) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b), Fingerprint: fingerprintOf(content), Name: "endpoints.close",
		Operation: engine.CloseEndpointOp(id, encodeClose),
	}
}

func stopCommand(b byte, content string) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b), Fingerprint: fingerprintOf(content), Name: "daemon.stop",
		Operation: engine.StopDaemonOp(encodeStop),
	}
}

// The same identifier and the same request is answered with the stored result
// and nothing is executed or written; the same identifier with another request is
// refused and nothing is written. Without the replay check the second close would
// find the endpoint closed and store "already closed" over the answer the first
// one gave. The rule is the same for every operation, because it is the engine's:
// the table runs it for both.
func TestAResendReturnsTheStoredResultAndAReusedIdentifierIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		first  func(a ident.EndpointID) engine.ManagementCommand
		other  func(a ident.EndpointID) engine.ManagementCommand
		result string
		tx     string
	}{
		{"closing an endpoint",
			func(a ident.EndpointID) engine.ManagementCommand { return closeCommand(7, "close a", a) },
			func(a ident.EndpointID) engine.ManagementCommand { return closeCommand(7, "close b", a) },
			`{"outcome":"closed"}`, "endpoint-close"},
		{"stopping the daemon",
			func(ident.EndpointID) engine.ManagementCommand { return stopCommand(8, "stop") },
			func(ident.EndpointID) engine.ManagementCommand { return stopCommand(8, "stop, but different") },
			`{"stopping":true}`, "daemon-stop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noLeaks(t)
			r := newRig(t, 10)
			e := r.start()
			a := register(t, e, "a")
			ctx := context.Background()

			first, err := e.Execute(ctx, tc.first(a))
			if err != nil || first.Replayed || string(first.Result) != tc.result {
				t.Fatalf("first request: %+v, %v; want the result %s, not replayed", first, err, tc.result)
			}
			if got := r.commits(tc.tx); got != 1 {
				t.Fatalf("%d transactions of %s, want one", got, tc.tx)
			}
			before := r.snapshot()

			again, err := e.Execute(ctx, tc.first(a))
			if err != nil || !again.Replayed || string(again.Result) != tc.result {
				t.Errorf("a resend answered %+v, %v; want the stored result, replayed", again, err)
			}
			_, err = e.Execute(ctx, tc.other(a))
			wantReason(t, err, engine.ReasonOperationIDReused)
			if !reflect.DeepEqual(before, r.snapshot()) {
				t.Error("a resend and a reused identifier wrote to the disk")
			}
			if got := r.commits(tc.tx); got != 1 {
				t.Errorf("%d transactions of %s after the resend, want still one", got, tc.tx)
			}

			// The result is on disk, so a process that starts over it answers the same.
			r.restart()
			e = r.start()
			again, err = e.Execute(ctx, tc.first(a))
			if err != nil || !again.Replayed || string(again.Result) != tc.result {
				t.Errorf("after a restart a resend answered %+v, %v", again, err)
			}
			_, err = e.Execute(ctx, tc.other(a))
			wantReason(t, err, engine.ReasonOperationIDReused)
		})
	}
}

// Two requests with one identifier that reach the engine at once run once: the
// replay check and the storing of the result are one step of the loop, so the
// second finds the first one's result. A check made outside the loop would let
// both pass it.
func TestConcurrentResendsOfOneRequestRunOnce(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	a := register(t, e, "a")
	const n = 16
	var wg sync.WaitGroup
	results := make([]engine.StoredResult, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = e.Execute(context.Background(), closeCommand(9, "close a", a))
		}()
	}
	wg.Wait()
	replayed := 0
	for i := range n {
		if errs[i] != nil || string(results[i].Result) != `{"outcome":"closed"}` {
			t.Fatalf("request %d: %+v, %v", i, results[i], errs[i])
		}
		if results[i].Replayed {
			replayed++
		}
	}
	if replayed != n-1 {
		t.Errorf("%d of %d requests were answered from the stored result, want all but one", replayed, n)
	}
	if got := r.commits("endpoint-close"); got != 1 {
		t.Errorf("%d transactions closed the endpoint, want one", got)
	}
}

// A request that was refused stored nothing, so the identifier is still free:
// the same identifier may be used for the request again once what it asked for
// can be done.
func TestARefusedRequestStoresNothingAndTheIdentifierStaysFree(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	ctx := context.Background()
	unknown := mustEndpoint(t, 99)
	before := r.snapshot()
	_, err := e.Execute(ctx, closeCommand(11, "close it", unknown))
	wantReason(t, err, engine.ReasonEndpointUnknown)
	if !reflect.DeepEqual(before, r.snapshot()) {
		t.Fatal("a refused close wrote to the disk")
	}
	a := register(t, e, "a")
	res, err := e.Execute(ctx, closeCommand(11, "close it", a))
	if err != nil || res.Replayed || string(res.Result) != `{"outcome":"closed"}` {
		t.Errorf("the same identifier after the refusal: %+v, %v", res, err)
	}
}

// What a request that stops the daemon says is the operation's, not the result's:
// a resend of it asks for the stop again, so a daemon that was killed between
// storing the result and stopping is stopped by the resend.
func TestStopFollowsTheOperationOfTheRequest(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	a := register(t, e, "a")
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		cmd  engine.ManagementCommand
		stop bool
	}{
		{"stopping the daemon", stopCommand(20, "stop"), true},
		{"the same request again", stopCommand(20, "stop"), true},
		{"closing an endpoint", closeCommand(21, "close a", a), false},
		{"the same close again", closeCommand(21, "close a", a), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := e.Execute(ctx, tc.cmd)
			if err != nil || res.Stop != tc.stop {
				t.Errorf("answered %+v, %v; want Stop = %v", res, err, tc.stop)
			}
		})
	}
}

// A request is taken only when the node is running: while it starts, a request
// that changes state is told so, and reads answer; once it stops, nothing is
// taken.
func TestExecuteIsRefusedWhileRecoveringAndWhileStopping(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e, _, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	e.Start()
	t.Cleanup(func() { r.stop(e) })
	ctx := context.Background()
	if _, err := e.Execute(ctx, stopCommand(30, "stop")); err == nil {
		t.Fatal("a request that changes state was taken while the node was recovering")
	} else {
		wantReason(t, err, engine.ReasonRecovering)
	}
	if _, err := e.Endpoints(ctx); err != nil {
		t.Errorf("a read while recovering: %v", err)
	}
	if err := e.RecoverDelivery(ctx); err != nil {
		t.Fatal(err)
	}
	e.Serve()
	e.BeginShutdown()
	_, err = e.Execute(ctx, stopCommand(31, "stop"))
	wantReason(t, err, engine.ReasonShuttingDown)
}

// An operation that does not change state is not a management request that
// stores a result: reads and the export of the revocation do not come through
// Execute, and a command that says it is one is a mistake of the caller, refused
// before anything runs.
func TestExecuteRefusesAnOperationThatChangesNothing(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	ran := false
	op := engine.NewTestOperation(admin.Operation{Kind: admin.OpRead}, false, func(engine.TestRun) ([]byte, error) {
		ran = true
		return []byte(`{}`), nil
	})
	_, err := e.Execute(context.Background(), engine.ManagementCommand{Key: requestKey(40), Fingerprint: fingerprintOf("read"), Name: "read", Operation: op})
	if err == nil || ran {
		t.Fatalf("a read came through Execute: ran %v, error %v", ran, err)
	}
	if _, err := e.Execute(context.Background(), engine.ManagementCommand{Key: requestKey(41), Fingerprint: fingerprintOf("none"), Name: "none"}); err == nil {
		t.Error("a command without an operation was taken")
	}
}

// nodeSetOperation is a versioned change of the configuration through the door,
// the way the owner of such an operation writes it: the engine hands the
// expected version over and config checks it inside the change, in the same step
// as the write.
func nodeSetOperation(r *rig, patch string) engine.Operation {
	return engine.NewTestOperation(admin.Operation{Kind: admin.OpChangeNodeSettings}, false, func(x engine.TestRun) ([]byte, error) {
		req := config.Request{Key: x.Key, Fingerprint: x.Fingerprint, Expected: x.Expected}
		_, err := r.cfg.SetNodeSettings(req, []byte(patch), func(config.Node) (json.RawMessage, error) {
			return json.RawMessage(`{"changed":true}`), nil
		})
		if err != nil {
			return nil, err
		}
		return []byte(`{"changed":true}`), nil
	})
}

func nodeSetCommand(r *rig, b byte, content string, expected admin.Version, patch string) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b), Fingerprint: fingerprintOf(content), Expected: expected, Name: "node.set",
		Operation: nodeSetOperation(r, patch),
	}
}

// A change of a versioned setting through the door: the version it names must be
// the current one, a stale one is a conflict and nothing is applied; a resend of a
// request that was applied returns its stored result and is not judged by the
// version again, though the version has moved on since; the result is kept in the
// file the change wrote, so a process that starts over it knows it.
func TestAVersionedChangeIsAppliedOnceAndAStaleOneNeverIsApplied(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	ctx := context.Background()
	v1 := r.cfg.Node().Version

	res, err := e.Execute(ctx, nodeSetCommand(r, 50, "set a", v1, `{"node_queue_limit":20000}`))
	if err != nil || res.Replayed || string(res.Result) != `{"changed":true}` {
		t.Fatalf("the change: %+v, %v", res, err)
	}
	if r.cfg.Node().Version != v1+1 || r.cfg.Node().Settings.NodeQueueLimit != 20000 {
		t.Fatalf("the change was not applied: %+v", r.cfg.Node())
	}

	// a stale version is a conflict, and nothing changes
	before := r.snapshot()
	_, err = e.Execute(ctx, nodeSetCommand(r, 51, "set b", v1, `{"node_queue_limit":30000}`))
	var ce *config.ChangeError
	if !errors.As(err, &ce) || ce.Reason != config.ChangeVersionConflict || ce.Current != v1+1 {
		t.Fatalf("a stale version: %v; want a version conflict naming the current version %d", err, v1+1)
	}
	if !reflect.DeepEqual(before, r.snapshot()) || r.cfg.Node().Settings.NodeQueueLimit != 20000 {
		t.Error("a conflicting change was applied")
	}

	// the first request again: the stored result, not a conflict, and not run twice
	again, err := e.Execute(ctx, nodeSetCommand(r, 50, "set a", v1, `{"node_queue_limit":20000}`))
	if err != nil || !again.Replayed || string(again.Result) != `{"changed":true}` {
		t.Errorf("a resend of the applied change: %+v, %v; want the stored result", again, err)
	}
	if r.cfg.Node().Version != v1+1 {
		t.Errorf("the resend changed the version to %d", r.cfg.Node().Version)
	}
	// the same identifier for another request is refused, whatever its version
	_, err = e.Execute(ctx, nodeSetCommand(r, 50, "set c", v1+1, `{"node_queue_limit":40000}`))
	wantReason(t, err, engine.ReasonOperationIDReused)

	// a result that lives in config/ is found by a process that starts over the disk
	r.restart()
	e = r.start()
	again, err = e.Execute(ctx, nodeSetCommand(r, 50, "set a", v1, `{"node_queue_limit":20000}`))
	if err != nil || !again.Replayed || string(again.Result) != `{"changed":true}` {
		t.Errorf("after a restart a resend of the change: %+v, %v; want the result stored in node.json", again, err)
	}
	_, err = e.Execute(ctx, nodeSetCommand(r, 50, "set c", v1+1, `{"node_queue_limit":40000}`))
	wantReason(t, err, engine.ReasonOperationIDReused)
}

// The stored result of an operation is read by the key it was filed under, and
// only by that key: the same identifier of another person is another key, and an
// identifier that never ran, or whose request was refused, has no result. Results
// are kept in two places, state/admin/ and the versioned files of config/, and a
// read finds both, before and after a restart.
func TestTheStoredResultOfAnOperationIsReadByItsRequestKey(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	a := register(t, e, "a")
	ctx := context.Background()
	v1 := r.cfg.Node().Version
	if _, err := e.Execute(ctx, closeCommand(60, "close a", a)); err != nil { // kept under state/admin/
		t.Fatal(err)
	}
	if _, err := e.Execute(ctx, nodeSetCommand(r, 61, "set a", v1, `{"node_queue_limit":20000}`)); err != nil { // kept in node.json
		t.Fatal(err)
	}
	if _, err := e.Execute(ctx, closeCommand(62, "close the unknown", mustEndpoint(t, 99))); err == nil {
		t.Fatal("closing an endpoint that does not exist was taken")
	}
	person, err := ident.PersonIDFromBytes(bytes.Repeat([]byte{0x33}, ident.PersonIDLen))
	if err != nil {
		t.Fatal(err)
	}
	check := func(where string, e *engine.Engine) {
		t.Helper()
		for _, tc := range []struct {
			name   string
			key    admin.RequestKey
			result string // empty: no result
		}{
			{"kept under state/admin/", requestKey(60), `{"outcome":"closed"}`},
			{"kept in node.json", requestKey(61), `{"changed":true}`},
			{"never used", requestKey(63), ""},
			{"refused, so nothing was stored", requestKey(62), ""},
			{"the same identifier of another person", admin.RequestKey{Person: person, ID: requestKey(60).ID}, ""},
		} {
			got, ok, err := e.OperationResult(ctx, tc.key)
			if err != nil {
				t.Errorf("%s, %s: %v", where, tc.name, err)
				continue
			}
			if ok != (tc.result != "") || string(got) != tc.result {
				t.Errorf("%s, %s: %q, found %v; want %q", where, tc.name, got, ok, tc.result)
			}
		}
	}
	before := r.snapshot()
	check("running", e)
	if !reflect.DeepEqual(before, r.snapshot()) {
		t.Error("reading a result wrote to the disk")
	}
	r.restart()
	e = r.start()
	check("after a restart", e)
}

// A read of a stored result is a read: it is taken while the node starts, like the
// other reads, and refused once it stops.
func TestAStoredResultIsReadWhileRecoveringAndNotWhileStopping(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e, _, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	e.Start()
	t.Cleanup(func() { r.stop(e) })
	ctx := context.Background()
	if _, ok, err := e.OperationResult(ctx, requestKey(70)); err != nil || ok {
		t.Errorf("while recovering: found %v, %v; want a read that finds nothing", ok, err)
	}
	if err := e.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, err = e.OperationResult(ctx, requestKey(70))
	wantReason(t, err, engine.ReasonShuttingDown)
}
