package agentapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestProbeOnlyModeRefusesOtherMethods(t *testing.T) {
	h := harness.New(t, harness.Options{})
	s, err := agentapi.New(agentapi.Config{Service: h.Engine()})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"me", "declare", "peers", "directory", "send", "reply", "recv", "ack", "status", "control"} {
		t.Run(name, func(t *testing.T) {
			for _, token := range []string{"", "not a token", strings.Repeat("A", 43)} {
				body := &countedBody{Reader: strings.NewReader(`{}`)}
				r := httptest.NewRequest(http.MethodPost, "/v1/agent/"+name, body)
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if body.reads != 0 {
					t.Fatal("probe-only refusal read the request body")
				}
				var answer apiwire.Body
				if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil {
					t.Fatal(err)
				}
				if w.Code != 503 || answer.Error.Category != apiwire.CategoryUnavailable || answer.Error.Code != "probe_only" {
					t.Fatalf("token %q: HTTP %d, %s; want unavailable/probe_only", token, w.Code, w.Body)
				}
			}
		})
	}
}

func TestProbeTokenOverAgentSocketMarksFrontendReachable(t *testing.T) {
	h := harness.New(t, harness.Options{Adapter: &harness.AdapterOptions{Runtime: true}})
	if err := h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	probes, err := h.Engine().ProbeTokens(context.Background(), harness.Instance)
	if err != nil || len(probes) == 0 {
		t.Fatalf("ProbeTokens: %+v, %v", probes, err)
	}
	home := socketHome(t)
	addr, err := ipc.Locate(home, ipc.KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	l, err := agentapi.Listen(addr, held{}, ipc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := agentapi.New(agentapi.Config{Service: h.Engine()})
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() { ended <- s.Serve(l) }()
	t.Cleanup(func() {
		s.Close()
		if err := <-ended; err != nil {
			t.Error(err)
		}
	})
	q := probes[0]
	c, err := agentapi.NewClient(agentapi.ClientConfig{Home: home, ProbeToken: q.Token})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	got, err := c.Probe(context.Background(), q.Frontend)
	if err != nil || !got.OK || got.Preset != q.Preset || got.Frontend != q.Frontend {
		t.Fatalf("Probe: %+v, %v; want %s/%s", got, err, q.Preset, q.Frontend)
	}
	v, err := h.Engine().Adapter(context.Background(), harness.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Probe.Reached) != 1 || v.Probe.Reached[0].Preset != q.Preset || v.Probe.Reached[0].Frontend != q.Frontend {
		t.Fatalf("the socket probe reached %+v, want %s/%s", v.Probe.Reached, q.Preset, q.Frontend)
	}
	_, err = c.Probe(context.Background(), q.Frontend)
	if e := apiwire.FromError(err); e == nil || e.Code != "probe_token_used" {
		t.Fatalf("a second use: %v; want probe_token_used", err)
	}
}

type held struct{}

func (held) Held() bool { return true }

func socketHome(t testing.TB) string {
	t.Helper()
	home, err := os.MkdirTemp("", "aa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestProbeRequestsEnforceAuthenticationAndStrictInput(t *testing.T) {
	h := harness.New(t, harness.Options{})
	var log bytes.Buffer
	s, err := agentapi.New(agentapi.Config{Service: h.Engine(), Log: slog.New(slog.NewJSONHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		t.Fatal(err)
	}
	valid := "Bearer " + strings.Repeat("A", 43)
	for _, tc := range []struct {
		name, authorization, body, category, code string
	}{
		{"no credential", "", `{"frontend":"cli"}`, "unauthenticated", "token_missing"},
		{"another authentication scheme", "Basic sensitive-token", `{"frontend":"cli"}`, "unauthenticated", "token_malformed"},
		{"a noncanonical token", valid + "=", `{"frontend":"cli"}`, "unauthenticated", "token_malformed"},
		{"a token the node did not issue", valid, `{"frontend":"cli"}`, "unauthenticated", "token_unknown"},
		{"unknown member", valid, `{"frontend":"cli","extra":1}`, "invalid_argument", "malformed_request"},
		{"repeated member", valid, `{"frontend":"cli","frontend":"mcp"}`, "invalid_argument", "malformed_request"},
		{"wrong case", valid, `{"Frontend":"cli"}`, "invalid_argument", "malformed_request"},
		{"missing front end", valid, `{}`, "invalid_argument", "malformed_request"},
		{"null front end", valid, `{"frontend":null}`, "invalid_argument", "malformed_request"},
		{"wrong front end type", valid, `{"frontend":1}`, "invalid_argument", "malformed_request"},
		{"another front end", valid, `{"frontend":"direct"}`, "invalid_argument", "malformed_request"},
		{"two values", valid, `{"frontend":"cli"}{}`, "invalid_argument", "malformed_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &countedBody{Reader: strings.NewReader(tc.body)}
			r := httptest.NewRequest("POST", "/v1/agent/probe", body)
			if tc.authorization != "" {
				r.Header.Set("Authorization", tc.authorization)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if (tc.code == "token_missing" || tc.code == "token_malformed") && body.reads != 0 {
				t.Fatal("credential refusal read the request body")
			}
			var out apiwire.Body
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if string(out.Error.Category) != tc.category || out.Error.Code != tc.code || w.Code != out.Error.Category.HTTPStatus() {
				t.Fatalf("HTTP %d, %s; want %s/%s", w.Code, w.Body, tc.category, tc.code)
			}
			if strings.Contains(w.Body.String(), "sensitive-token") || strings.Contains(w.Body.String(), strings.Repeat("A", 43)) || strings.Contains(log.String(), "sensitive-token") || strings.Contains(log.String(), strings.Repeat("A", 43)) {
				t.Fatal("a credential appeared in an error or diagnostic log")
			}
		})
	}
}

func TestAProbeWithAnIncompleteAnswerHasAnUnknownOutcomeAndIsNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name         string
		partial, raw bool
	}{
		{"typed no answer", false, false},
		{"typed cut answer", true, false},
		{"raw no answer", false, true},
		{"raw cut answer", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := socketHome(t)
			addr, err := ipc.Locate(home, ipc.KindAgent)
			if err != nil {
				t.Fatal(err)
			}
			l, err := ipc.Listen(addr, held{}, ipc.Options{})
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				if r.Method != "POST" || r.Header.Get("Idempotency-Key") != "" || r.Header.Get("X-Idempotency-Key") != "" {
					t.Error("an Agent request was made retryable by the HTTP transport")
				}
				if call == 1 {
					io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"ok":true,"preset":"default","frontend":"cli"}`)
					return
				}
				conn, rw, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				if tc.partial {
					rw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n{\"ok\":")
					rw.Flush()
				}
				conn.Close()
			})}
			ended := make(chan error, 1)
			go func() { ended <- s.Serve(l) }()
			t.Cleanup(func() { s.Close(); <-ended })
			c, err := agentapi.NewClient(agentapi.ClientConfig{Home: home, ProbeToken: adapterproto.Token(strings.Repeat("A", 43))})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(c.Close)
			probe := func() error {
				if tc.raw {
					_, err := c.Call(context.Background(), "probe", json.RawMessage(`{"frontend":"cli"}`))
					return err
				}
				_, err := c.Probe(context.Background(), adapterproto.FrontendCLI)
				return err
			}
			if err := probe(); err != nil {
				t.Fatalf("establishing the keep-alive connection: %v", err)
			}
			err = probe()
			e := apiwire.FromError(err)
			if e == nil || e.Category != apiwire.CategoryOutcomeUnknown || e.Code != apiwire.CodeOutcomeUnknown {
				t.Fatalf("an incomplete answer returned %v; want outcome_unknown", err)
			}
			if n := calls.Load(); n != 2 {
				t.Fatalf("%d requests; want the successful request and one interrupted request", n)
			}
		})
	}
}

// Count reads without changing the request text or the transport boundary.
type countedBody struct {
	*strings.Reader
	reads int
}

func (b *countedBody) Read(p []byte) (int, error) { b.reads++; return b.Reader.Read(p) }
