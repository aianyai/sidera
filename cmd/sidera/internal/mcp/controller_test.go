package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera/internal/controller"
	"github.com/aianyai/sidera/cmd/sidera/internal/mcp"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/manageapi"
)

// The tokens the fake node hands out to its endpoints. They differ from the one the
// test Agent client starts with, so a request carrying one proves the controller
// replaced the credential.
var issued = []string{
	"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAE",
	"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAI",
}

// attachRecorder is the node's side of sessions.attach for one session: a request that
// creates makes an endpoint when none is open, and one that only finds is refused when
// none is. Endpoint n has the address "n/e<n>" and the token issued[n-1]; a closed
// endpoint's token is refused by the Agent interface.
type attachRecorder struct {
	mu       sync.Mutex
	requests []string
	wake     string
	opened   int
	open     bool
}

func (a *attachRecorder) Call(_ context.Context, method string, in manageapi.Request) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	body, _ := json.Marshal(in.Params)
	a.requests = append(a.requests, method+" "+string(body))
	if !a.open {
		if strings.Contains(string(body), `"create":false`) {
			return nil, apiwire.New(apiwire.CategoryNotFound, "session_not_attached", "no open endpoint is attached to this session")
		}
		a.opened, a.open = a.opened+1, true
	}
	state := a.wake
	if state == "" {
		state = `{"state":"ready"}`
	}
	return json.RawMessage(fmt.Sprintf(`{"result":{"endpoint":"n/e%d","instance":"i","token":"%s","wake":%s}}`, a.opened, issued[a.opened-1], state)), nil
}

func (a *attachRecorder) sent() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.requests...)
}

func (a *attachRecorder) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.requests)
}

// closeEndpoint is the owner closing the endpoint.
func (a *attachRecorder) closeEndpoint() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.open = false
}

// accepts says whether the Agent interface knows the credential: the token of the
// open endpoint, or the client's own before any attachment.
func (a *attachRecorder) accepts(authorization string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opened == 0 || (a.open && authorization == "Bearer "+issued[a.opened-1])
}

// controllerSession runs a server with no token of its own and returns what the
// Agent interface saw as credentials.
func controllerSession(t *testing.T, env map[string]string, m *attachRecorder, interval time.Duration) (*session, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	c := httpAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		if !m.accepts(r.Header.Get("Authorization")) {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"category":"unauthenticated","code":"token_unknown","message":"the token is not known to this node or has been revoked"}}`+"\n")
			return
		}
		io.WriteString(w, "{\"nodes\":[]}\n")
	}))
	s := start(t, mcp.Config{Version: "test", Agent: c, Controller: &controller.Config{
		Manage: m, Getenv: func(k string) string { return env[k] },
		Getwd: func() (string, error) { return "/work/proj", nil }, Interval: interval,
	}})
	return s, &seen
}

func (s *session) rawCall(t *testing.T, id int, meta string) (texts []string, isError bool) {
	t.Helper()
	fmt.Fprintf(s.input, `{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"peers","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}%s}}}`+"\n", id, meta)
	line, err := s.output.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	for _, c := range response.Result.Content {
		texts = append(texts, c.Text)
	}
	return texts, response.Result.IsError
}

// The first tool call attaches the session and carries the notice as a second item;
// a call the Agent interface refuses for its token is answered as the error it is,
// and the next call attaches the session anew with a notice that names both
// addresses.
func TestAToolCallRefusedForItsTokenIsFollowedByANewAttachmentAndANotice(t *testing.T) {
	m := &attachRecorder{wake: `{"state":"unavailable","reason":"native_unreachable","detail":"inbox closed"}`}
	s, seen := controllerSession(t, map[string]string{"CLAUDE_CODE_SESSION_ID": "sess-1"}, m, time.Hour)
	texts, isError := s.rawCall(t, 1, "")
	if isError || len(texts) != 2 || texts[0] != "{\"nodes\":[]}\n" ||
		!strings.Contains(texts[1], "cannot wake it (native_unreachable: inbox closed)") || !strings.Contains(texts[1], "wait_s") {
		t.Fatalf("first call: %q %v", texts, isError)
	}
	if texts, _ = s.rawCall(t, 2, ""); len(texts) != 1 {
		t.Fatalf("a later call repeats the notice: %q", texts)
	}

	m.closeEndpoint()
	texts, isError = s.rawCall(t, 3, "")
	if !isError || len(texts) != 1 || !strings.Contains(texts[0], "token_unknown") {
		t.Fatalf("the call with the closed endpoint's token: %q %v", texts, isError)
	}
	texts, isError = s.rawCall(t, 4, "")
	if isError || len(texts) != 2 || !strings.Contains(texts[1], "n/e1") || !strings.Contains(texts[1], "n/e2") || !strings.Contains(texts[1], "closed") {
		t.Fatalf("the call after: %q %v", texts, isError)
	}
	want := []string{"Bearer " + issued[0], "Bearer " + issued[0], "Bearer " + issued[0], "Bearer " + issued[1]}
	if got := *seen; len(got) != len(want) || got[0] != want[0] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("credentials %q, want %q", got, want)
	}
}

// A server that has no Agent client has nothing to give a token to: it looks up no
// endpoint at start and answers every tool call as the node not running.
func TestAControllerServerWithoutAnAgentClientLooksUpNothing(t *testing.T) {
	m := &attachRecorder{opened: 1, open: true}
	s := start(t, mcp.Config{Version: "test", Controller: &controller.Config{
		Manage: m, Getenv: func(k string) string { return map[string]string{"CLAUDE_CODE_SESSION_ID": "sess-1"}[k] },
		Getwd: func() (string, error) { return "/work/proj", nil }, Interval: time.Hour,
	}})
	if texts, isError := s.rawCall(t, 1, ""); !isError || len(texts) != 1 || !strings.Contains(texts[0], "daemon_not_running") {
		t.Fatalf("%q %v", texts, isError)
	}
	if m.count() != 0 {
		t.Fatalf("requests %q, want none", m.sent())
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for " + what)
		}
		time.Sleep(time.Millisecond)
	}
}

// Codex puts the thread into the metadata of each tool call, as an object or, in some
// versions, as a JSON string; anything else is no thread.
func TestCodexControllerReadsTheThreadFromTheToolCall(t *testing.T) {
	for name, meta := range map[string]string{
		"object": `,"x-codex-turn-metadata":{"thread_id":"thread-9","turn_id":"x"}`,
		"string": `,"x-codex-turn-metadata":"{\"thread_id\":\"thread-9\"}"`,
	} {
		t.Run(name, func(t *testing.T) {
			m := &attachRecorder{}
			s, _ := controllerSession(t, nil, m, time.Hour)
			if _, isError := s.rawCall(t, 1, meta); isError {
				t.Fatal("the call failed")
			}
			if want := `sessions.attach {"adapter":"codex","native_ref":"thread-9","display_name":"Codex controller (proj)"}`; m.sent()[0] != want {
				t.Fatalf("request %s; want %s", m.sent()[0], want)
			}
		})
	}
	m := &attachRecorder{}
	s, seen := controllerSession(t, nil, m, time.Hour)
	for _, meta := range []string{"", `,"x-codex-turn-metadata":{"turn_id":"x"}`, `,"x-codex-turn-metadata":7`} {
		texts, isError := s.rawCall(t, 1, meta)
		if !isError || len(texts) != 1 || !strings.Contains(texts[0], "session id is unknown") {
			t.Fatalf("%s: %q %v", meta, texts, isError)
		}
	}
	if m.count() != 0 || len(*seen) != 0 {
		t.Fatalf("the unknown session attached (%d) or reached the Agent interface (%d)", m.count(), len(*seen))
	}
}

// Run starts the heartbeat with the server and ends it when the input ends: the
// server's cleanup closes stdin and requires Run to return, which it cannot while the
// heartbeat still runs.
func TestControllerHeartbeatRunsWhileServingAndStopsAtEndOfInput(t *testing.T) {
	m := &attachRecorder{}
	s, _ := controllerSession(t, map[string]string{"CLAUDE_CODE_SESSION_ID": "sess-1"}, m, 2*time.Millisecond)
	s.rawCall(t, 1, "")
	waitFor(t, "a heartbeat", func() bool { return m.count() >= 3 })
}
