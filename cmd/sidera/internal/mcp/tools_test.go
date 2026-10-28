package mcp_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera/internal/mcp"
	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

type held struct{}

func (held) Held() bool { return true }
func agentNode(t testing.TB) (*harness.H, *agentapi.Client, string) {
	t.Helper()
	h, c, address, _, _ := agentNodeDetails(t)
	return h, c, address
}

func agentNodeDetails(t testing.TB, observers ...func(string, int)) (*harness.H, *agentapi.Client, string, string, adapterproto.Token) {
	t.Helper()
	h := harness.New(t, harness.Options{Adapter: &harness.AdapterOptions{Runtime: true}})
	h.Quiesce()
	h.MustDirect("creator")
	created, err := h.Agent("creator").Control(h.Ctx(), fakeagent.ControlInput{To: h.RuntimeAddr(), Signal: adapterproto.SignalSessionCreate, Preset: "default"})
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		h.Quiesce()
		h.Advance(engine.DefaultParams().PullInterval)
	}
	h.Quiesce()
	status, err := h.Agent("creator").Status(h.Ctx(), fakeagent.StatusInput{MessageID: created.MessageID})
	if err != nil || status.ControlReply == nil || status.ControlReply.Create == nil {
		t.Fatalf("creation: %+v %v", status, err)
	}
	scan, err := h.Records()
	if err != nil {
		t.Fatal(err)
	}
	var token adapterproto.Token
	var address string
	for _, ep := range scan.Endpoints {
		if ep.ID.String() == string(status.ControlReply.Create.SessionEndpoint) && ep.Status == store.EndpointOpen {
			token = adapterproto.Token(base64.RawURLEncoding.EncodeToString(ep.Token))
			address = harness.NodeID().String() + "/" + ep.ID.String()
		}
	}
	if token == "" {
		t.Fatal("no open session")
	}
	home, err := os.MkdirTemp("", "mc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	if err := os.Mkdir(filepath.Join(home, "run"), 0700); err != nil {
		t.Fatal(err)
	}
	addr, err := ipc.Locate(home, ipc.KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	l, err := ipc.Listen(addr, held{}, ipc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := agentapi.New(agentapi.Config{Service: h.Engine()})
	if err != nil {
		t.Fatal(err)
	}
	srv.EnableMethods()
	httpServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := &agentResponse{ResponseWriter: w, status: http.StatusOK}
		srv.ServeHTTP(result, r)
		for _, observe := range observers {
			observe(r.URL.Path, result.status)
		}
	})}
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(l) }()
	t.Cleanup(func() {
		httpServer.Close()
		srv.Close()
		if err := <-done; err != nil && err != http.ErrServerClosed {
			t.Error(err)
		}
	})
	c, err := agentapi.NewClient(agentapi.ClientConfig{Home: home, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return h, c, address, home, token
}

// Observe only method names and statuses, never credentials or request bodies.
type agentResponse struct {
	http.ResponseWriter
	status int
}

func (w *agentResponse) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func TestToolsCallThroughTheAgentSocket(t *testing.T) {
	h, c, address := agentNode(t)
	s := start(t, mcp.Config{Agent: c, Version: "test"})
	s.exchange(t, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{}}}`, `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"sidera","version":"test"},"instructions":"Use me to inspect your endpoint and receive limits. Receive pages must be read before acknowledging them."}}`)
	want := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"declaration\":{\"paused\":true,\"refuse_multi_recipient\":false,\"restrict_senders\":false,\"accept_from\":[],\"hidden_from_directory\":false}}\n"}],"isError":false}}`
	s.exchange(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"declare","arguments":{"paused":true,"future":42}}}`, want)
	me, err := c.Me(context.Background())
	if err != nil || !me.Declaration.Paused || me.Address != address {
		t.Fatalf("me: %+v %v", me, err)
	}
	_ = h
}

func (s *session) call(t *testing.T, id int, name string, args any) (string, bool) {
	t.Helper()
	params := map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args, "_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientCapabilities": map[string]any{}}}}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	s.input.Write(append(raw, '\n'))
	line, err := s.output.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError    bool   `json:"isError"`
			ResultType string `json:"resultType"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil || response.Result.ResultType != "complete" || len(response.Result.Content) != 1 {
		t.Fatalf("response: %s", line)
	}
	return response.Result.Content[0].Text, response.Result.IsError
}

func TestToolArgumentsAndFailures(t *testing.T) {
	h, c, _ := agentNode(t)
	_ = h
	s := start(t, mcp.Config{Agent: c, Version: "test"})
	for i, tc := range []struct {
		name           string
		args           any
		category, code string
	}{
		{"send", map[string]any{"body": "missing recipient"}, "invalid_argument", "malformed_request"},
		{"recv", map[string]any{"wait_s": -1}, "invalid_argument", "malformed_request"},
		{"directory", map[string]any{"limit": 257}, "invalid_argument", "malformed_request"},
		{"declare", map[string]any{"paused": "true"}, "invalid_argument", "malformed_request"},
		{"send", map[string]any{"to": []string{"a", "b"}, "body": "message"}, "invalid_argument", "single_recipient_only"},
	} {
		t.Run(tc.name+tc.code, func(t *testing.T) {
			text, isError := s.call(t, i+1, tc.name, tc.args)
			var body struct {
				Error struct{ Category, Code string } `json:"error"`
			}
			json.Unmarshal([]byte(text), &body)
			if !isError || body.Error.Category != tc.category || body.Error.Code != tc.code {
				t.Fatalf("tool error: %s, isError=%v", text, isError)
			}
		})
	}
}

func TestReceivePageIsTheOnlyTextAndIsUnchanged(t *testing.T) {
	const page = "{ \"lease\":\"lease_1234567890123456\", \"messages\":[], \"more\":0 }\n"
	c := httpAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if string(raw) != "{\"frontend\":\"mcp\"}\n" {
			t.Errorf("receive request: %s", raw)
		}
		io.WriteString(w, page)
	}))
	s := start(t, mcp.Config{Agent: c, Version: "test"})
	text, isError := s.call(t, 1, "recv", map[string]any{})
	if isError || text != page {
		t.Fatalf("page changed: %q, isError=%v", text, isError)
	}
}

func httpAgent(t testing.TB, handler http.Handler) *agentapi.Client {
	t.Helper()
	home, err := os.MkdirTemp("", "mc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	if err := os.Mkdir(filepath.Join(home, "run"), 0700); err != nil {
		t.Fatal(err)
	}
	addr, err := ipc.Locate(home, ipc.KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	l, err := ipc.Listen(addr, held{}, ipc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	t.Cleanup(func() { srv.Close(); <-done })
	c, err := agentapi.NewClient(agentapi.ClientConfig{Home: home, Token: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ProbeToken: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
