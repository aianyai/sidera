package manageapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/store"
)

func endpointOf(b byte) ident.EndpointID {
	id, err := ident.EndpointIDFromBytes(bytes.Repeat([]byte{b}, ident.EndpointIDLen))
	if err != nil {
		panic(err)
	}
	return id
}

var ownNode = func() ident.NodeID {
	n, err := ident.NodeIDFromBytes(bytes.Repeat([]byte{7}, ident.NodeIDLen))
	if err != nil {
		panic(err)
	}
	return n
}()

func addressOf(b byte) string { return ident.Address{Node: ownNode, Endpoint: endpointOf(b)}.String() }

// Only a POST to a method that exists is taken. The other verbs and the other
// paths are answered, and the service is asked nothing.
func TestOnlyAPostToAKnownMethodIsTaken(t *testing.T) {
	r := newRig(t)
	c := r.client()
	defer c.CloseIdleConnections()
	for _, tc := range []struct {
		name, verb, url string
		status          int
		category, code  string
	}{
		{"a GET of a method", http.MethodGet, "http://sidera/v1/manage/endpoints.list", 405, "invalid_argument", "method_not_allowed"},
		{"a PUT of a method", http.MethodPut, "http://sidera/v1/manage/endpoints.list", 405, "invalid_argument", "method_not_allowed"},
		{"a method nobody has", http.MethodPost, "http://sidera/v1/manage/nothing.here", 404, "not_found", "method_unknown"},
		{"a method of the other interface", http.MethodPost, "http://sidera/v1/agent/endpoints.list", 404, "not_found", "method_unknown"},
		{"the version is not v1", http.MethodPost, "http://sidera/v2/manage/endpoints.list", 404, "not_found", "method_unknown"},
		{"no method", http.MethodPost, "http://sidera/v1/manage/", 404, "not_found", "method_unknown"},
		{"a method with a path below it", http.MethodPost, "http://sidera/v1/manage/endpoints.list/x", 404, "not_found", "method_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(tc.verb, tc.url, strings.NewReader(`{}`))
			resp, err := c.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var buf strings.Builder
			var b [512]byte
			for {
				n, err := resp.Body.Read(b[:])
				buf.Write(b[:n])
				if err != nil {
					break
				}
			}
			r.wantError(tc.status, tc.category, tc.code, resp.StatusCode, []byte(buf.String()))
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type %q", ct)
			}
		})
	}
	if n := len(r.svc.calls()); n != 0 {
		t.Errorf("the service was asked %d times", n)
	}
}

// A body over the limit is refused without being read to the end.
func TestARequestOverTheLimitIsRefused(t *testing.T) {
	r := newRig(t)
	body := `{"params":{"instance":"` + strings.Repeat("a", maxBody) + `"}}`
	status, out := r.post("endpoints.list", body)
	r.wantError(400, "invalid_argument", "request_too_large", status, out)
}

// A request that is not what the method takes is refused as malformed, with the
// position of the fault when the decoder can place it: the strict rules of the
// decoder are the ones that count (unknown member, repeated key, null, a key of
// another case, a value of another type).
func TestAMalformedRequestIsRefusedNamingThePositionOfTheFault(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct {
		name, method, body string
		path               string // "" when the decoder places no fault
	}{
		{"not JSON", "endpoints.list", `{`, ""},
		{"a list instead of an object", "endpoints.list", `[]`, ""},
		{"an unknown member of the request", "endpoints.list", `{"params":{},"extra":1}`, "extra"},
		{"a member of another case", "endpoints.list", `{"Params":{}}`, "Params"},
		{"a repeated key", "endpoints.list", `{"params":{},"params":{}}`, "params"},
		{"null for params", "endpoints.list", `{"params":null}`, "params"},
		{"an unknown member of the params", "endpoints.list", `{"params":{"nothing":1}}`, "params.nothing"},
		{"a value of the wrong type", "endpoints.list", `{"params":{"instance":7}}`, "params.instance"},
		{"an empty instance", "endpoints.list", `{"params":{"instance":""}}`, "params.instance"},
		{"a missing required member", "endpoints.close", `{"operation_id":"` + opID(1) + `","params":{}}`, "params.endpoint"},
		{"an address that is not one", "endpoints.close", `{"operation_id":"` + opID(1) + `","params":{"endpoint":"nope"}}`, "params.endpoint"},
		{"params of a method that takes none, not empty", "daemon.stop", `{"operation_id":"` + opID(1) + `","params":{"x":1}}`, "params.x"},
		{"an operation identifier on a read", "endpoints.list", `{"operation_id":"` + opID(1) + `"}`, "operation_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, out := r.post(tc.method, tc.body)
			e := r.wantError(400, "invalid_argument", "malformed_request", status, out)
			if got, _ := e.Error.Details["path"].(string); got != tc.path {
				t.Errorf("details %v, want the path %q", e.Error.Details, tc.path)
			}
		})
	}
	if n := len(r.svc.calls()); n != 0 {
		t.Errorf("a refused request reached the service %d times", n)
	}
}

// The operation identifier of a request that changes something: absent is
// refused by the administrative rules, one that is not 32 lower-case hexadecimal
// digits or is all zero is refused as malformed, and none of them reaches the
// engine.
func TestTheOperationIdentifierOfARequestThatChangesSomething(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct {
		name, body, code string
	}{
		{"absent", `{}`, "missing_operation_id"},
		{"all zero", `{"operation_id":"00000000000000000000000000000000"}`, "malformed_request"},
		{"too short", `{"operation_id":"abcd"}`, "malformed_request"},
		{"too long", `{"operation_id":"` + opID(1) + `00"}`, "malformed_request"},
		{"upper case", `{"operation_id":"` + strings.ToUpper(strings.Repeat("ab", 16)) + `"}`, "malformed_request"},
		{"not hexadecimal", `{"operation_id":"` + strings.Repeat("zz", 16) + `"}`, "malformed_request"},
		{"empty", `{"operation_id":""}`, "malformed_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, out := r.post("daemon.stop", tc.body)
			r.wantError(400, "invalid_argument", tc.code, status, out)
		})
	}
	if n := len(r.svc.calls()); n != 0 {
		t.Errorf("a refused request reached the service %d times", n)
	}
}

// A method that changes no versioned setting takes no expected version: it would
// be taken for a check that the method does not make.
func TestAnExpectedVersionIsRefusedWhereNoneIsCarried(t *testing.T) {
	r := newRig(t)
	status, out := r.post("daemon.stop", `{"operation_id":"`+opID(2)+`","expected_version":3}`)
	r.wantError(400, "invalid_argument", "unexpected_version", status, out)
	// the member is refused for being there, whatever its value: zero is no
	// exception, or a sender that wrote it would believe a check was made
	status, out = r.post("daemon.stop", `{"operation_id":"`+opID(2)+`","expected_version":0}`)
	r.wantError(400, "invalid_argument", "unexpected_version", status, out)
	if n := len(r.svc.calls()); n != 0 {
		t.Errorf("a refused request reached the service %d times", n)
	}
}

// extraMethods are two methods that the daemon does not have yet, put into the
// table of a server: a change of a versioned setting, and an operation that the
// catalogue does not know. The owners of such methods add theirs the same way,
// and the generic handling is the same for all.
func extraMethods(s *Server) {
	s.methods["test.set"] = &method{
		name: "test.set", op: admin.Operation{Kind: admin.OpChangeNodeSettings},
		plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
			var p struct {
				Value int `json:"value"`
			}
			if err := decodeParams(raw, &p); err != nil {
				return nil, err
			}
			// the fake service never runs the operation; any value of the type will do
			return &plan{canonical: canonical(p), operation: engine.CloseEndpointOp(endpointOf(1), nil)}, nil
		},
	}
	s.methods["test.unknown"] = &method{
		name: "test.unknown", op: admin.Operation{Kind: admin.OpUnknown},
		plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
			return &plan{canonical: []byte(`{}`), operation: engine.StopDaemonOp(func() ([]byte, error) { return []byte(`{}`), nil })}, nil
		},
	}
}

// A change of a versioned setting carries the version it is based on; without it
// the administrative rules refuse the request, and with it the version goes to the
// engine, which hands it to the owner of the setting.
func TestAChangeOfASettingNeedsItsExpectedVersion(t *testing.T) {
	r := newRig(t, extraMethods)
	status, out := r.post("test.set", `{"operation_id":"`+opID(3)+`","params":{"value":1}}`)
	r.wantError(400, "invalid_argument", "missing_expected_version", status, out)
	if n := len(r.svc.calls()); n != 0 {
		t.Fatalf("a refused request reached the service")
	}
	status, out = r.post("test.set", `{"operation_id":"`+opID(3)+`","expected_version":12,"params":{"value":1}}`)
	if status != 200 {
		t.Fatalf("answered %d %s", status, out)
	}
	if calls := r.svc.calls(); len(calls) != 1 || calls[0].Expected != 12 {
		t.Errorf("the engine was asked %+v, want one command with the expected version 12", calls)
	}
}

// The request is judged by the catalogue before it is run: an operation outside
// it is denied to everyone, the local owner included, which is a defect of the
// program and not of the request, and the engine is never asked.
func TestAnOperationOutsideTheCatalogueIsNeverRun(t *testing.T) {
	r := newRig(t, extraMethods)
	status, out := r.post("test.unknown", `{"operation_id":"`+opID(4)+`"}`)
	r.wantError(500, "internal", "unknown_operation", status, out)
	if n := len(r.svc.calls()); n != 0 {
		t.Errorf("the engine was asked %d times", n)
	}
}

// What the engine is asked: the operation identifier scoped to the local owner,
// the fingerprint of the whole request, the expected version, the name of the
// method as the stored result carries it, and an operation the catalogue calls
// by the right kind.
func TestTheCommandTheEngineIsAsked(t *testing.T) {
	r := newRig(t)
	status, out := r.post("endpoints.close", `{"operation_id":"`+opID(0x5a)+`","params":{"endpoint":"`+addressOf(9)+`"}}`)
	if status != 200 {
		t.Fatalf("answered %d %s", status, out)
	}
	calls := r.svc.calls()
	if len(calls) != 1 {
		t.Fatalf("the engine was asked %d times", len(calls))
	}
	c := calls[0]
	var id admin.OperationID
	id[15] = 0x5a
	if c.Key != admin.KeyFor(admin.LocalOwner(), id) || c.Key.Person != (ident.PersonID{}) {
		t.Errorf("key %+v, want the local owner's key of the identifier", c.Key)
	}
	if c.Name != "endpoints.close" || c.Expected != 0 || c.Operation == nil || c.Operation.Admin().Kind != admin.OpManageEndpointSession {
		t.Errorf("command %+v", c)
	}
	// the fingerprint, worked out by hand from the rule: the version of the rule,
	// the method, the canonical params and the expected version, NUL between
	want := sha256.Sum256([]byte("sidera-manage-v1\x00endpoints.close\x00" + `{"endpoint":"` + addressOf(9) + `"}` + "\x00" + "0"))
	if c.Fingerprint != want {
		t.Errorf("fingerprint %x, want %x", c.Fingerprint, want)
	}
}

// Requests that mean the same thing have one fingerprint, so a resend that the
// client wrote with another order of members or other spacing is still a resend;
// requests that differ in what they ask, or in the version they are based on, do
// not.
func TestEqualRequestsHaveOneFingerprintAndDifferentOnesDoNot(t *testing.T) {
	r := newRig(t, extraMethods)
	send := func(body string) admin.Fingerprint {
		t.Helper()
		before := len(r.svc.calls())
		if status, out := r.post("test.set", body); status != 200 {
			t.Fatalf("%s answered %d %s", body, status, out)
		}
		calls := r.svc.calls()
		if len(calls) != before+1 {
			t.Fatalf("%d commands, want %d", len(calls), before+1)
		}
		return calls[before].Fingerprint
	}
	a := send(`{"operation_id":"` + opID(1) + `","expected_version":2,"params":{"value":5}}`)
	b := send(`{ "params" : { "value" : 5 } , "expected_version" : 2 , "operation_id" : "` + opID(9) + `" }`)
	if a != b {
		t.Error("the same request written differently has two fingerprints")
	}
	if c := send(`{"operation_id":"` + opID(1) + `","expected_version":2,"params":{"value":6}}`); c == a {
		t.Error("requests with other params share a fingerprint")
	}
	if d := send(`{"operation_id":"` + opID(1) + `","expected_version":3,"params":{"value":5}}`); d == a {
		t.Error("requests based on another version share a fingerprint")
	}
	// the method is part of it
	if status, out := r.post("daemon.stop", `{"operation_id":"`+opID(1)+`"}`); status != 200 {
		t.Fatalf("daemon.stop answered %d %s", status, out)
	}
	if calls := r.svc.calls(); calls[len(calls)-1].Fingerprint == a {
		t.Error("two methods share a fingerprint")
	}
}

// The answer of a request that changes something: the identifier, whether it was
// a resend, and the result the engine stored, as it is.
func TestTheAnswerOfARequestThatChangesSomething(t *testing.T) {
	r := newRig(t)
	for _, replayed := range []bool{false, true} {
		r.svc.execute = func(context.Context, engine.ManagementCommand) (engine.StoredResult, error) {
			return engine.StoredResult{Result: []byte(`{"endpoint":"x","outcome":"closed"}`), Replayed: replayed}, nil
		}
		status, out := r.post("endpoints.close", `{"operation_id":"`+opID(6)+`","params":{"endpoint":"`+addressOf(1)+`"}}`)
		want := fmt.Sprintf(`{"operation_id":"%s","replayed":%v,"result":{"endpoint":"x","outcome":"closed"}}`, opID(6), replayed)
		if status != 200 || strings.TrimSpace(string(out)) != want {
			t.Errorf("replayed=%v: %d\n%s\nwant\n%s", replayed, status, out, want)
		}
	}
}

// A read answers the result and nothing else; the list of endpoints names no
// token, and an instance filters it.
func TestAReadAnswersItsResult(t *testing.T) {
	r := newRig(t)
	r.svc.endpoints = []engine.EndpointView{
		{ID: endpointOf(1), Address: ident.Address{Node: ownNode, Endpoint: endpointOf(1)}, Kind: adapterreg.KindDirect, Instance: "chat", Status: store.EndpointOpen, Queued: 3},
		{ID: endpointOf(2), Address: ident.Address{Node: ownNode, Endpoint: endpointOf(2)}, Kind: adapterreg.KindSession, Instance: "other", Status: store.EndpointClosed, Draining: true},
	}
	status, out := r.post("endpoints.list", `{}`)
	want := fmt.Sprintf(`{"result":{"endpoints":[{"address":"%s","kind":"direct","instance":"chat","state":"open","queued":3,"draining":false},`+
		`{"address":"%s","kind":"session","instance":"other","state":"closed","queued":0,"draining":true}]}}`, addressOf(1), addressOf(2))
	if status != 200 || strings.TrimSpace(string(out)) != want {
		t.Errorf("%d\n%s\nwant\n%s", status, out, want)
	}
	status, out = r.post("endpoints.list", `{"params":{"instance":"other"}}`)
	if status != 200 || !strings.Contains(string(out), addressOf(2)) || strings.Contains(string(out), addressOf(1)) {
		t.Errorf("filtered by an instance: %d %s", status, out)
	}
	status, out = r.post("endpoints.list", `{"params":{"instance":"none"}}`)
	if status != 200 || strings.TrimSpace(string(out)) != `{"result":{"endpoints":[]}}` {
		t.Errorf("no endpoint of the instance: %d %s", status, out)
	}
	if strings.Contains(strings.ToLower(string(out)), "token") {
		t.Error("the list names a token")
	}
}

// The stop is asked for after the answer was written, once, and only for a
// request whose operation stops the daemon: the client gets its answer before the
// daemon goes away.
func TestStopIsRequestedAfterTheAnswerIsWrittenAndOnlyForAnOperationThatStops(t *testing.T) {
	r := newRig(t)
	r.svc.execute = func(_ context.Context, c engine.ManagementCommand) (engine.StoredResult, error) {
		return engine.StoredResult{Result: []byte(`{"stopping":true}`), Stop: c.Name == "daemon.stop"}, nil
	}
	status, out := r.post("endpoints.close", `{"operation_id":"`+opID(1)+`","params":{"endpoint":"`+addressOf(1)+`"}}`)
	if status != 200 {
		t.Fatalf("%d %s", status, out)
	}
	if r.stop.count() != 0 {
		t.Fatal("a request that does not stop the daemon asked for the stop")
	}
	// the stop waits for the client to have its answer: asked for before the answer
	// was written and flushed, it would wait for a client that waits for it
	r.stop.hold = make(chan struct{})
	status, out = r.post("daemon.stop", `{"operation_id":"`+opID(2)+`"}`)
	close(r.stop.hold)
	if status != 200 || !strings.Contains(string(out), `"stopping":true`) {
		t.Fatalf("the answer of the stop: %d %s", status, out)
	}
	select {
	case <-r.stop.ch:
	case <-time.After(10 * time.Second):
		t.Fatal("the stop was never asked for")
	}
	if r.stop.timedOut.Load() {
		t.Error("the stop was asked for before the client had its answer")
	}
	// a refused request asks for no stop
	r.svc.execute = func(context.Context, engine.ManagementCommand) (engine.StoredResult, error) {
		return engine.StoredResult{Stop: true}, &engine.Refusal{Reason: engine.ReasonRecovering}
	}
	status, out = r.post("daemon.stop", `{"operation_id":"`+opID(3)+`"}`)
	r.wantError(503, "unavailable", "recovering", status, out)
	time.Sleep(50 * time.Millisecond)
	if r.stop.count() != 1 {
		t.Errorf("the stop was asked for %d times, want once", r.stop.count())
	}
}

// What the engine refuses for is answered in the words of the wire, once, by the
// one mapping; an error that nobody expected is an internal error whose text is in
// the log of the daemon and not in the answer.
func TestRefusalsAreAnsweredInTheWordsOfTheWireAndUnknownErrorsStayInTheLog(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct {
		name     string
		err      error
		status   int
		cat, cod string
	}{
		{"recovering", &engine.Refusal{Reason: engine.ReasonRecovering}, 503, "unavailable", "recovering"},
		{"shutting down", &engine.Refusal{Reason: engine.ReasonShuttingDown}, 503, "unavailable", "shutting_down"},
		{"the identifier reused", &engine.Refusal{Reason: engine.ReasonOperationIDReused}, 409, "conflict", "operation_id_reused"},
		{"an unknown endpoint", &engine.Refusal{Reason: engine.ReasonEndpointUnknown}, 404, "not_found", "endpoint_unknown"},
		{"a runtime endpoint", &engine.Refusal{Reason: engine.ReasonWrongEndpointKind}, 422, "refused", "endpoint_kind"},
		{"the disk is full", &engine.Refusal{Reason: engine.ReasonStorageFull}, 429, "backpressure", "storage_full"},
		{"a fatal failure", &engine.Refusal{Reason: engine.ReasonStorageFatal}, 500, "outcome_unknown", "storage_fatal"},
		{"an unexpected error", errors.New("open /h/.sidera/keys/x: secret-token-abc"), 500, "internal", "internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r.svc.execute = func(context.Context, engine.ManagementCommand) (engine.StoredResult, error) {
				return engine.StoredResult{}, tc.err
			}
			status, out := r.post("daemon.stop", `{"operation_id":"`+opID(7)+`"}`)
			r.wantError(tc.status, tc.cat, tc.cod, status, out)
			if strings.Contains(string(out), "secret-token-abc") {
				t.Error("the text of an unexpected error is in the answer")
			}
		})
	}
	if !strings.Contains(r.log.String(), "secret-token-abc") {
		t.Error("the text of the unexpected error is not in the log of the daemon")
	}
}

// A client that goes away cancels the request: the context the engine is asked
// with ends, so a call that waits is not left waiting for a reader that is gone.
func TestAClientThatGoesAwayCancelsTheRequest(t *testing.T) {
	r := newRig(t)
	entered := make(chan struct{})
	ended := make(chan struct{})
	r.svc.execute = func(ctx context.Context, _ engine.ManagementCommand) (engine.StoredResult, error) {
		close(entered)
		<-ctx.Done()
		close(ended)
		return engine.StoredResult{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://sidera/v1/manage/daemon.stop", strings.NewReader(`{"operation_id":"`+opID(8)+`"}`))
	c := r.client()
	defer c.CloseIdleConnections()
	errc := make(chan error, 1)
	go func() { _, err := c.Do(req); errc <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the request never reached the service")
	}
	cancel()
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the context of the request did not end when the client went away")
	}
	<-errc
}

// Listen takes only the address of the management interface: a connection it
// accepts is the local owner, and the Agent interface's address must not get a
// server that treats its connections that way.
func TestListenRefusesTheAddressOfTheAgentInterface(t *testing.T) {
	home := newHome(t)
	addr, err := ipc.Locate(home, ipc.KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Listen(addr, held{}, ipc.Options{})
	if err == nil {
		t.Fatal("the management interface listened on the address of the Agent interface")
		_ = l
	}
	if (ipc.Address{}) == addr {
		t.Fatal("setting up: no address")
	}
	if _, err := Listen(ipc.Address{}, held{}, ipc.Options{}); err == nil {
		t.Error("Listen took the zero address")
	}
}

// Shutdown waits for a request that is being served and ends the serving; nothing
// of the server runs afterwards.
func TestShutdownWaitsForARequestAndLeavesNoGoroutine(t *testing.T) {
	before := runtime.NumGoroutine()
	r := newRig(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	r.svc.execute = func(context.Context, engine.ManagementCommand) (engine.StoredResult, error) {
		close(entered)
		<-release
		return engine.StoredResult{Result: []byte(`{"stopping":true}`)}, nil
	}
	got := make(chan int, 1)
	go func() {
		status, _ := r.post("daemon.stop", `{"operation_id":"`+opID(1)+`"}`)
		got <- status
	}()
	<-entered
	shut := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		shut <- r.srv.Shutdown(ctx)
	}()
	select {
	case err := <-shut:
		t.Fatalf("Shutdown returned %v while a request was being served", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if status := <-got; status != 200 {
		t.Errorf("the request in flight was answered %d", status)
	}
	if err := <-shut; err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	if status, _ := func() (int, error) {
		c := r.client()
		defer c.CloseIdleConnections()
		resp, err := c.Post("http://sidera/v1/manage/endpoints.list", "application/json", strings.NewReader(`{}`))
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		return resp.StatusCode, nil
	}(); status == 200 {
		t.Error("the interface answered after Shutdown")
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before+1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+1 { // +1: the cleanup's own Shutdown wait
		buf := make([]byte, 1<<16)
		t.Errorf("%d goroutines, %d before:\n%s", n, before, buf[:runtime.Stack(buf, true)])
	}
}

// The limits of the server: a slow header is cut, there is no deadline on the
// write (a request may be a wait), connections do not sit idle for ever.
func TestTheServerTimeouts(t *testing.T) {
	r := newRig(t)
	h := r.srv.http
	if h.ReadHeaderTimeout != 10*time.Second || h.ReadTimeout != 30*time.Second || h.IdleTimeout != 60*time.Second || h.MaxHeaderBytes != 8<<10 {
		t.Errorf("limits: header %v, read %v, idle %v, header bytes %d", h.ReadHeaderTimeout, h.ReadTimeout, h.IdleTimeout, h.MaxHeaderBytes)
	}
	if h.WriteTimeout != 0 {
		t.Errorf("a write timeout of %v would cut a request that waits", h.WriteTimeout)
	}
}

// Concurrent requests are served at once and each is answered its own.
func TestConcurrentRequestsAreAnsweredEachItsOwn(t *testing.T) {
	r := newRig(t)
	r.svc.execute = func(_ context.Context, c engine.ManagementCommand) (engine.StoredResult, error) {
		return engine.StoredResult{Result: []byte(fmt.Sprintf(`{"id":"%x"}`, c.Key.ID))}, nil
	}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, out := r.post("daemon.stop", `{"operation_id":"`+opID(byte(i+1))+`"}`)
			want := fmt.Sprintf(`"result":{"id":"%s"}`, opID(byte(i+1)))
			if status != 200 || !strings.Contains(string(out), want) {
				t.Errorf("request %d: %d %s", i, status, out)
			}
		}()
	}
	wg.Wait()
}

// An endpoint is of this node or it is not an endpoint of this node: an address of
// another node never reaches the engine, whose endpoints are known by the part of
// the address that is theirs alone.
func TestAnAddressOfAnotherNodeIsNoEndpointOfThisOne(t *testing.T) {
	r := newRig(t)
	other, err := ident.NodeIDFromBytes(bytes.Repeat([]byte{0x2a}, ident.NodeIDLen))
	if err != nil {
		t.Fatal(err)
	}
	foreign := ident.Address{Node: other, Endpoint: endpointOf(9)}.String()
	status, out := r.post("endpoints.close", `{"operation_id":"`+opID(1)+`","params":{"endpoint":"`+foreign+`"}}`)
	r.wantError(404, "not_found", "endpoint_unknown", status, out)
	if n := len(r.svc.calls()); n != 0 {
		t.Errorf("the engine was asked %d times about an endpoint of another node", n)
	}
}

// A change of a versioned setting that carries the member with the value zero has
// no version: zero is "not supplied", so it is the same as leaving it out.
func TestAnExplicitZeroVersionOfAVersionedChangeIsMissing(t *testing.T) {
	r := newRig(t, extraMethods)
	status, out := r.post("test.set", `{"operation_id":"`+opID(5)+`","expected_version":0,"params":{"value":1}}`)
	r.wantError(400, "invalid_argument", "missing_expected_version", status, out)
	if n := len(r.svc.calls()); n != 0 {
		t.Errorf("a refused request reached the service %d times", n)
	}
}

// operation.result answers the result stored under the key of the requester: the
// owner's own identifiers, as the engine files them. The answer is a read's, a
// request for an identifier that has no result says so with the listed code, and
// the interface looks nothing up itself.
func TestOperationResultAnswersTheResultStoredForTheOwnersIdentifier(t *testing.T) {
	r := newRig(t)
	var id admin.OperationID
	id[15] = 9
	r.svc.results = map[admin.RequestKey][]byte{
		admin.KeyFor(admin.LocalOwner(), id): []byte(`{"outcome":"closed"}`),
	}
	status, out := r.post("operation.result", `{"params":{"operation_id":"`+opID(9)+`"}}`)
	if status != 200 || strings.TrimSpace(string(out)) != `{"result":{"outcome":"closed"}}` {
		t.Fatalf("answered %d %s; want the stored result as the result of a read", status, out)
	}
	if asked := r.svc.asked(); len(asked) != 1 || asked[0] != admin.KeyFor(admin.LocalOwner(), id) {
		t.Errorf("the engine was asked for %v; want the key of the local owner and the identifier", asked)
	}
	status, out = r.post("operation.result", `{"params":{"operation_id":"`+opID(10)+`"}}`)
	r.wantError(404, "not_found", "operation_unknown", status, out)
	if n := len(r.svc.calls()); n != 0 {
		t.Errorf("a read went through the door of operations %d times", n)
	}
}

func TestOperationResultNeedsAnIdentifierThatIsWellFormed(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct{ name, body, code string }{
		{"absent", `{"params":{}}`, "missing_operation_id"},
		{"no params", `{}`, "missing_operation_id"},
		{"all zero", `{"params":{"operation_id":"00000000000000000000000000000000"}}`, "malformed_request"},
		{"upper case", `{"params":{"operation_id":"` + strings.ToUpper(strings.Repeat("ab", 16)) + `"}}`, "malformed_request"},
		{"too short", `{"params":{"operation_id":"abcd"}}`, "malformed_request"},
		{"another member", `{"params":{"operation_id":"` + opID(1) + `","extra":1}}`, "malformed_request"},
		{"a member of a request that changes something", `{"operation_id":"` + opID(1) + `","params":{"operation_id":"` + opID(1) + `"}}`, "malformed_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, out := r.post("operation.result", tc.body)
			r.wantError(400, "invalid_argument", tc.code, status, out)
		})
	}
	if n := len(r.svc.asked()); n != 0 {
		t.Errorf("a refused request reached the engine %d times", n)
	}
}

// What the engine says when it cannot answer is said in the words of the wire.
func TestOperationResultAnswersAnEngineThatCannotAnswer(t *testing.T) {
	r := newRig(t)
	r.svc.lookupErr = &engine.Refusal{Reason: engine.ReasonShuttingDown}
	status, out := r.post("operation.result", `{"params":{"operation_id":"`+opID(1)+`"}}`)
	r.wantError(503, "unavailable", "shutting_down", status, out)
}

// A test row is a row of the table that is no operation of the owner's: it is not
// judged by the catalogue, so it needs no catalogue entry, carries no operation
// identifier and takes no expected version, and the engine's door is never used.
// The row below is put into the table the way the streams that own the harness's
// methods put theirs (without a test tag, only this test can); an operation with
// no catalogue entry, in the same table, is denied.
func TestATestRowIsOutsideTheCatalogueAndIsNotAuthorized(t *testing.T) {
	r := newRig(t, extraMethods, func(s *Server) {
		s.methods["test.echo"] = &method{name: "test.echo", test: func(_ context.Context, raw json.RawMessage) (any, error) {
			var p struct {
				Say string `json:"say"`
			}
			if err := decodeParams(raw, &p); err != nil {
				return nil, err
			}
			return map[string]string{"said": p.Say}, nil
		}}
	})
	status, out := r.post("test.echo", `{"params":{"say":"hello"}}`)
	if status != 200 || strings.TrimSpace(string(out)) != `{"result":{"said":"hello"}}` {
		t.Fatalf("answered %d %s", status, out)
	}
	status, out = r.post("test.echo", `{}`)
	r.wantError(400, "invalid_argument", "malformed_request", status, out) // the row's params are decoded strictly
	status, out = r.post("test.echo", `{"operation_id":"`+opID(1)+`","params":{"say":"x"}}`)
	r.wantError(400, "invalid_argument", "malformed_request", status, out) // it carries no operation identifier
	status, out = r.post("test.unknown", `{"operation_id":"`+opID(4)+`"}`)
	r.wantError(500, "internal", "unknown_operation", status, out) // an operation with no entry is denied
	if n := len(r.svc.calls()) + len(r.svc.asked()); n != 0 {
		t.Errorf("the engine was asked %d times", n)
	}
	// a GET is refused for a test row as for every method
	resp, err := r.client().Get("http://sidera/v1/manage/test.echo")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Errorf("GET answered %d, want 405", resp.StatusCode)
	}
}

// A row of the table is an operation of the owner's or a test row, never both and
// never neither, and the name says which: a test row is called test.something and
// nothing else is.
func TestARowIsAnOperationOrATestRowAndItsNameSaysWhich(t *testing.T) {
	test := func(context.Context, json.RawMessage) (any, error) { return nil, nil }
	plan := func(*Server, json.RawMessage) (*plan, error) { return nil, nil }
	for _, tc := range []struct {
		name string
		m    *method
		ok   bool
	}{
		{"an operation", &method{name: "things.list", op: admin.Operation{Kind: admin.OpRead}, plan: plan}, true},
		{"a test row", &method{name: "test.thing", test: test}, true},
		{"a test row with a catalogue entry", &method{name: "test.thing", test: test, op: admin.Operation{Kind: admin.OpRead}}, false},
		{"a test row with a plan", &method{name: "test.thing", test: test, plan: plan}, false},
		{"neither", &method{name: "things.list"}, false},
		{"a test row that is not called test.", &method{name: "things.list", test: test}, false},
		{"an operation called test.", &method{name: "test.thing", plan: plan, op: admin.Operation{Kind: admin.OpRead}}, false},
		{"no name", &method{test: test}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkRow(tc.m); (err == nil) != tc.ok {
				t.Errorf("checkRow = %v, want ok = %v", err, tc.ok)
			}
		})
	}
}
