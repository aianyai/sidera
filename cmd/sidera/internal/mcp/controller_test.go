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
	"github.com/aianyai/sidera/internal/node/manageapi"
)

// The token the fake management interface hands out. It differs from the one the
// test Agent client starts with, so a request carrying it proves the controller
// replaced the credential.
const issued = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAE"

type attachRecorder struct {
	mu       sync.Mutex
	requests []string
	wake     string
}

func (a *attachRecorder) Call(_ context.Context, method string, in manageapi.Request) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	body, _ := json.Marshal(in.Params)
	a.requests = append(a.requests, method+" "+string(body))
	state := a.wake
	if state == "" {
		state = `{"state":"ready"}`
	}
	return json.RawMessage(`{"result":{"endpoint":"n/e","instance":"i","token":"` + issued + `","wake":` + state + `}}`), nil
}

func (a *attachRecorder) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.requests)
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

func TestClaudeControllerAttachesOnFirstUseAndTellsTheModel(t *testing.T) {
	m := &attachRecorder{wake: `{"state":"unavailable","reason":"native_unreachable","detail":"inbox closed"}`}
	env := map[string]string{"CLAUDE_CODE_SESSION_ID": "sess-1", "CLAUDE_CODE_MESSAGING_TOKEN": "wake-secret"}
	s, seen := controllerSession(t, env, m, time.Hour)
	if m.count() != 0 {
		t.Fatal("attached before any tool call")
	}
	texts, isError := s.rawCall(t, 1, "")
	if isError || len(texts) != 2 || texts[0] != "{\"nodes\":[]}\n" ||
		!strings.Contains(texts[1], "cannot wake it (native_unreachable: inbox closed)") || !strings.Contains(texts[1], "wait_s") {
		t.Fatalf("first call: %q %v", texts, isError)
	}
	texts, _ = s.rawCall(t, 2, "")
	if len(texts) != 1 {
		t.Fatalf("a later call repeats the notice: %q", texts)
	}
	if want := `sessions.attach {"adapter":"claude","native_ref":"sess-1","display_name":"Claude controller (proj)","wake":{"CLAUDE_CODE_MESSAGING_TOKEN":"wake-secret"}}`; m.requests[0] != want {
		t.Fatalf("request %s; want %s", m.requests[0], want)
	}
	if m.count() != 1 || len(*seen) != 2 || (*seen)[0] != "Bearer "+issued || (*seen)[1] != (*seen)[0] {
		t.Fatalf("attachments %d, credentials %q", m.count(), *seen)
	}
}

func TestCodexControllerReadsTheThreadFromTheToolCall(t *testing.T) {
	for name, meta := range map[string]string{
		"object": `,"x-codex-turn-metadata":{"thread_id":"thread-9","turn_id":"x"}`,
		"string": `,"x-codex-turn-metadata":"{\"thread_id\":\"thread-9\"}"`,
	} {
		t.Run(name, func(t *testing.T) {
			m := &attachRecorder{}
			s, _ := controllerSession(t, nil, m, time.Hour)
			texts, isError := s.rawCall(t, 1, meta)
			if isError || len(texts) != 2 || !strings.Contains(texts[1], "wakes this session by itself") {
				t.Fatalf("%q %v", texts, isError)
			}
			if want := `sessions.attach {"adapter":"codex","native_ref":"thread-9","display_name":"Codex controller (proj)"}`; m.requests[0] != want {
				t.Fatalf("request %s; want %s", m.requests[0], want)
			}
		})
	}
}

func TestCodexFirstCallWithoutAThreadIsAToolError(t *testing.T) {
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

func TestControllerHeartbeatRunsWhileServingAndStopsAtEndOfInput(t *testing.T) {
	m := &attachRecorder{}
	s, _ := controllerSession(t, map[string]string{"CLAUDE_CODE_SESSION_ID": "sess-1"}, m, 2*time.Millisecond)
	s.rawCall(t, 1, "")
	deadline := time.Now().Add(5 * time.Second)
	for m.count() < 3 {
		if time.Now().After(deadline) {
			t.Fatal("no heartbeat")
		}
		time.Sleep(time.Millisecond)
	}
	// The session's cleanup closes stdin and requires Run to return, which it
	// cannot while the heartbeat still runs.
}
