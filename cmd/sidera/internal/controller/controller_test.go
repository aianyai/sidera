package controller_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera/internal/controller"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/manageapi"
)

const tokenA = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
const tokenB = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAE"

func env(pairs ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return func(k string) string { return m[k] }
}

func cwd(dir string) func() (string, error) { return func() (string, error) { return dir, nil } }

func TestRequestFromEnvironment(t *testing.T) {
	claudeEnv := []string{"CLAUDECODE", "1", "CLAUDE_CODE_SESSION_ID", "sess-1",
		"CLAUDE_CODE_MESSAGING_SOCKET", "/tmp/s.sock", "CLAUDE_CODE_MESSAGING_TOKEN", "wake-secret", "CLAUDE_PID", "4242"}
	for _, tc := range []struct {
		name    string
		env     []string
		thread  string
		want    controller.Request
		notCtl  bool
		unknown bool
	}{
		{name: "sidera token", env: append([]string{"SIDERA_TOKEN", tokenA}, claudeEnv...), notCtl: true},
		{name: "probe token", env: append([]string{"SIDERA_PROBE_TOKEN", tokenA}, claudeEnv...), notCtl: true},
		{name: "plain terminal", notCtl: true},
		{name: "plain terminal with a thread id from nowhere", env: nil, thread: "", notCtl: true},
		{name: "claude copies its wake keys", env: claudeEnv, want: controller.Request{
			Adapter: "claude", NativeRef: "sess-1", DisplayName: "Claude controller (proj)",
			Wake: map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": "/tmp/s.sock", "CLAUDE_CODE_MESSAGING_TOKEN": "wake-secret", "CLAUDE_PID": "4242"}}},
		{name: "claude without wake keys", env: []string{"CLAUDE_CODE_SESSION_ID", "sess-2"}, want: controller.Request{
			Adapter: "claude", NativeRef: "sess-2", DisplayName: "Claude controller (proj)"}},
		{name: "claude without a session id", env: []string{"CLAUDECODE", "1"}, unknown: true},
		{name: "codex command line", env: []string{"CODEX_THREAD_ID", "thread-1"}, want: controller.Request{
			Adapter: "codex", NativeRef: "thread-1", DisplayName: "Codex controller (proj)"}},
		{name: "codex mcp thread", thread: "thread-2", want: controller.Request{
			Adapter: "codex", NativeRef: "thread-2", DisplayName: "Codex controller (proj)"}},
		{name: "claude wins over codex", env: []string{"CLAUDE_CODE_SESSION_ID", "c", "CODEX_THREAD_ID", "x"}, want: controller.Request{
			Adapter: "claude", NativeRef: "c", DisplayName: "Claude controller (proj)"}},
		{name: "a thread does not override a token", env: []string{"SIDERA_TOKEN", tokenA}, thread: "thread-3", notCtl: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := controller.RequestFromEnv(env(tc.env...), cwd("/work/proj"), tc.thread)
			switch {
			case tc.notCtl:
				if !errors.Is(err, controller.ErrNotController) {
					t.Fatalf("got %+v, %v; want ErrNotController", got, err)
				}
			case tc.unknown:
				var wire *apiwire.Error
				if !errors.As(err, &wire) || wire.Code != "token_missing" {
					t.Fatalf("got %+v, %v; want token_missing", got, err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				gb, _ := json.Marshal(got)
				wb, _ := json.Marshal(tc.want)
				if string(gb) != string(wb) {
					t.Fatalf("request %s; want %s", gb, wb)
				}
			}
		})
	}
}

func TestDisplayNameStaysWithinTheLimit(t *testing.T) {
	long := "/w/" + strings.Repeat("\u76ee", 100)
	req, err := controller.RequestFromEnv(env("CODEX_THREAD_ID", "t"), cwd(long), "")
	if err != nil || len(req.DisplayName) > 128 || !strings.HasSuffix(req.DisplayName, ")") {
		t.Fatalf("%q %v", req.DisplayName, err)
	}
	req, err = controller.RequestFromEnv(env("CODEX_THREAD_ID", "t"), cwd("/"), "")
	if err != nil || req.DisplayName != "Codex controller" {
		t.Fatalf("%q %v", req.DisplayName, err)
	}
}

// fakeManager answers sessions.attach and records what it was sent.
type fakeManager struct {
	mu       sync.Mutex
	requests []string
	tokens   []string // answers in order; the last repeats
	fail     error
}

func (f *fakeManager) Call(_ context.Context, method string, in manageapi.Request) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := json.Marshal(in.Params)
	f.requests = append(f.requests, method+" "+string(body))
	if f.fail != nil {
		return nil, f.fail
	}
	token := f.tokens[min(len(f.requests)-1, len(f.tokens)-1)]
	return json.RawMessage(`{"result":{"endpoint":"n/e","instance":"claude","token":"` + token + `","wake":{"state":"ready"}}}`), nil
}

func (f *fakeManager) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func TestAttachSendsTheRequestAndReturnsTheToken(t *testing.T) {
	m := &fakeManager{tokens: []string{tokenA}}
	req := controller.Request{Adapter: "codex", NativeRef: "t", DisplayName: "Codex controller (p)"}
	att, err := controller.Attach(context.Background(), m, req)
	if err != nil || string(att.Token) != tokenA || att.Wake.State != "ready" {
		t.Fatalf("%+v %v", att, err)
	}
	if want := `sessions.attach {"adapter":"codex","native_ref":"t","display_name":"Codex controller (p)"}`; m.requests[0] != want {
		t.Fatalf("request %s; want %s", m.requests[0], want)
	}
}

func TestAttachRefusalsAreWordedForTheCaller(t *testing.T) {
	for code, want := range map[string]string{
		"instance_unknown":       "sidera adapter enable claude",
		"attach_unsupported":     "does not take sessions a person opened",
		"native_session_created": "created by Sidera",
	} {
		m := &fakeManager{fail: apiwire.New(apiwire.CategoryRefused, code, "server words")}
		_, err := controller.Attach(context.Background(), m, controller.Request{Adapter: "claude", NativeRef: "s"})
		var wire *apiwire.Error
		if !errors.As(err, &wire) || wire.Code != code || !strings.Contains(wire.Message, want) {
			t.Errorf("%s: %v", code, err)
		}
	}
}
