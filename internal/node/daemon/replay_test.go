package daemon_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
)

// endpointRecord is an endpoint of the enabled instance on the disk of a home.
func endpointRecord(t testing.TB, b byte, kind adapterreg.EndpointKind) store.EndpointRecord {
	t.Helper()
	r := store.EndpointRecord{ID: endpointID(t, b), Kind: kind, Instance: "chat", Status: store.EndpointOpen}
	if kind == adapterreg.KindSession {
		r.Token = []byte(strings.Repeat("\x5a", store.TokenLen))
		r.Session = &store.SessionInfo{Preset: "default"}
	}
	return r
}

type manageAnswer struct {
	OperationID string          `json:"operation_id"`
	Replayed    bool            `json:"replayed"`
	Result      json.RawMessage `json:"result"`
}

func closeRequest(t testing.TB, home string, op string, addr ident.Address) (int, manageAnswer, []byte) {
	t.Helper()
	status, body := manage(t, home, "endpoints.close", map[string]any{
		"operation_id": op, "params": map[string]any{"endpoint": addr.String()},
	})
	var a manageAnswer
	if status == 200 {
		if err := json.Unmarshal(body, &a); err != nil {
			t.Fatalf("the answer is not JSON: %v\n%s", err, body)
		}
	}
	return status, a, body
}

// A management request that is sent again gets the result stored the first time,
// and the identifier of one request is never taken for another, through the whole
// daemon: the socket, the administrative rules, the engine's door and the disk.
// The result is the one the operation reached, so a close of an endpoint that is
// closed already says so, and what was stored before a restart is what the node
// answers after it.
func TestAReplayedManagementOperationReturnsTheStoredResultAndAReusedIdentifierIsRefused(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	session := endpointRecord(t, 0x11, adapterreg.KindSession)
	runtimeEp := endpointRecord(t, 0x12, adapterreg.KindRuntime)
	other := endpointRecord(t, 0x13, adapterreg.KindSession)
	seedHome(t, c.Home, session, runtimeEp, other)
	node := nodeOf(t, c)
	addr := func(r store.EndpointRecord) ident.Address { return ident.Address{Node: node, Endpoint: r.ID} }
	id := func(n int) string {
		return strings.Repeat("0", 30) + string("0123456789abcdef"[n>>4]) + string("0123456789abcdef"[n&15])
	}

	d := started(t, c)
	status, first, body := closeRequest(t, c.Home, id(1), addr(session))
	if status != 200 || first.Replayed || !strings.Contains(string(first.Result), `"outcome":"closing"`) {
		t.Fatalf("the first close: %d %s", status, body)
	}
	status, again, body := closeRequest(t, c.Home, id(1), addr(session))
	if status != 200 || !again.Replayed || string(again.Result) != string(first.Result) {
		t.Errorf("a resend: %d %s; want the stored result, replayed", status, body)
	}
	// the identifier of another request
	status, body = manage(t, c.Home, "endpoints.close", map[string]any{"operation_id": id(1), "params": map[string]any{"endpoint": addr(other).String()}})
	if status != 409 || !strings.Contains(string(body), "operation_id_reused") {
		t.Errorf("the identifier reused for another endpoint: %d %s; want 409 operation_id_reused", status, body)
	}
	if _, listed := manage(t, c.Home, "endpoints.list", map[string]any{}); !strings.Contains(string(listed), addr(other).String()) ||
		strings.Count(string(listed), `"state":"closed"`) != 1 {
		t.Errorf("the refused request changed the other endpoint, or the list is wrong: %s", listed)
	}
	// the endpoint that is closed already says so, under an identifier of its own
	status, second, body := closeRequest(t, c.Home, id(2), addr(session))
	if status != 200 || second.Replayed || !strings.Contains(string(second.Result), `"outcome":"already_closed"`) {
		t.Errorf("a close of an endpoint that is closed: %d %s", status, body)
	}
	// a runtime endpoint stands for its instance, an unknown one is none, and an
	// address of another node is not an endpoint of this one
	status, body = manage(t, c.Home, "endpoints.close", map[string]any{"operation_id": id(3), "params": map[string]any{"endpoint": addr(runtimeEp).String()}})
	if status != 422 || !strings.Contains(string(body), "endpoint_kind") {
		t.Errorf("closing a runtime endpoint: %d %s; want 422 endpoint_kind", status, body)
	}
	unknown := ident.Address{Node: node, Endpoint: endpointID(t, 0x7f)}
	status, body = manage(t, c.Home, "endpoints.close", map[string]any{"operation_id": id(4), "params": map[string]any{"endpoint": unknown.String()}})
	if status != 404 || !strings.Contains(string(body), "endpoint_unknown") {
		t.Errorf("closing an unknown endpoint: %d %s; want 404 endpoint_unknown", status, body)
	}
	foreign := ident.Address{Node: nodeOfOther(t), Endpoint: session.ID}
	status, body = manage(t, c.Home, "endpoints.close", map[string]any{"operation_id": id(5), "params": map[string]any{"endpoint": foreign.String()}})
	if status != 404 || !strings.Contains(string(body), "endpoint_unknown") {
		t.Errorf("closing an endpoint of another node: %d %s; want 404 endpoint_unknown", status, body)
	}
	// a refusal stores nothing: the identifier is free for the request that works
	status, third, body := closeRequest(t, c.Home, id(4), addr(other))
	if status != 200 || third.Replayed || !strings.Contains(string(third.Result), `"outcome":"closing"`) {
		t.Errorf("the identifier of a refused request, used again: %d %s", status, body)
	}

	// after a restart the node answers from what is on disk
	d.RequestStop("restart")
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	started(t, c)
	status, restarted, body := closeRequest(t, c.Home, id(1), addr(session))
	if status != 200 || !restarted.Replayed || string(restarted.Result) != string(first.Result) {
		t.Errorf("a resend after a restart: %d %s; want the stored result", status, body)
	}
	status, body = manage(t, c.Home, "endpoints.close", map[string]any{"operation_id": id(1), "params": map[string]any{"endpoint": addr(other).String()}})
	if status != 409 {
		t.Errorf("the reused identifier after a restart: %d %s", status, body)
	}
}
