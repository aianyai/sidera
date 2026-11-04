//go:build realcodex

package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/codexcfg"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/server"
)

// mockModel is a model provider that answers every turn with one shell command
// taken from the prompt ("RUN[[command]]") and then a closing message, so that a
// real Codex runs a real command under the sandbox of its thread without a
// language model, an account or any quota.
type mockModel struct {
	*httptest.Server
}

var runRE = regexp.MustCompile(`RUN\[\[(.*?)\]\]`)

func newMockModel(t *testing.T) *mockModel {
	t.Helper()
	m := &mockModel{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		command, answered := "", false
		for _, item := range req.Input {
			answered = answered || strings.HasSuffix(item.Type, "function_call_output")
			if item.Role == "user" {
				for _, c := range item.Content {
					if m := runRE.FindStringSubmatch(c.Text); m != nil {
						command = m[1]
					}
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		event := func(name string, data any) {
			b, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
		}
		id := fmt.Sprintf("resp_%d", time.Now().UnixNano())
		event("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": id}})
		var item map[string]any
		if answered || command == "" {
			item = map[string]any{"type": "message", "role": "assistant", "id": "msg_" + id,
				"content": []any{map[string]any{"type": "output_text", "text": "done"}}}
		} else {
			args, _ := json.Marshal(map[string]any{"cmd": command, "login": false})
			item = map[string]any{"type": "function_call", "call_id": "call_" + id, "name": "exec_command", "arguments": string(args)}
		}
		event("response.output_item.done", map[string]any{"type": "response.output_item.done", "item": item})
		event("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{
			"id": id, "usage": map[string]any{"input_tokens": 1, "input_tokens_details": nil, "output_tokens": 1, "output_tokens_details": nil, "total_tokens": 2},
		}})
	}))
	t.Cleanup(m.Close)
	return m
}

// A sandboxed command cannot read the data directory, whatever its sandbox allows
// elsewhere, and a command without a sandbox can. This is the guarantee the
// permission profile of a thread gives, and it holds only while Codex keeps the
// profile: the thread has to report it as active, and the turn must not bring a
// policy of its own.
func TestSandboxedCommandsCannotReadTheDataDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sandbox of Windows needs an elevated setup that this test does not make")
	}
	program := requireRealCodex(t)

	dir, err := os.MkdirTemp("", "k5")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	// The data directory, as the adapter names it for the sandbox.
	data, err := filepath.EvalSymlinks(filepath.Join(dir, "data"))
	if err == nil {
		t.Fatal("the data directory exists before it was made")
	}
	if err := os.Mkdir(filepath.Join(dir, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if data, err = filepath.EvalSymlinks(filepath.Join(dir, "data")); err != nil {
		t.Fatal(err)
	}
	const secret = "token-of-another-session"
	secretFile := filepath.Join(data, "secret")
	if err := os.WriteFile(secretFile, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(dir, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}

	// An isolated Codex home that names the mock model and holds no account.
	mock := newMockModel(t)
	home := filepath.Join(dir, "codex-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`model = "mock-model"
model_provider = "mock"

[model_providers.mock]
name = "mock"
base_url = %q
wire_api = "responses"
request_max_retries = 0
stream_max_retries = 0
`, mock.URL+"/v1")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := server.New(server.Config{Program: program, StateDir: filepath.Join(dir, "state"), Environ: append(os.Environ(), "CODEX_HOME="+home)})
	if err := os.Mkdir(filepath.Join(dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ep, err := srv.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = srv.Terminate(context.Background()) }()
	var recorded struct {
		WorkDir string `json:"work_dir"`
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "state", "app-server.json")); err == nil && json.Unmarshal(raw, &recorded) == nil && recorded.WorkDir != "" {
		defer os.RemoveAll(recorded.WorkDir)
	}

	type event struct {
		ThreadID string `json:"threadId"`
		Item     struct {
			Type             string `json:"type"`
			AggregatedOutput string `json:"aggregatedOutput"`
		} `json:"item"`
		Turn struct {
			Status string `json:"status"`
		} `json:"turn"`
	}
	var mu sync.Mutex
	outputs, turns := map[string]string{}, map[string]chan string{}
	turnEnded := func(thread string) chan string {
		mu.Lock()
		defer mu.Unlock()
		if turns[thread] == nil {
			turns[thread] = make(chan string, 4)
		}
		return turns[thread]
	}
	client, err := appserver.Dial(ctx, ep, appserver.Handlers{Notification: func(n appserver.Notification) {
		e, err := appserver.DecodeParams[event](n)
		if err != nil {
			return
		}
		switch n.Method {
		case appserver.NotifyItemCompleted:
			if e.Item.Type == "commandExecution" {
				mu.Lock()
				outputs[e.ThreadID] = e.Item.AggregatedOutput
				mu.Unlock()
			}
		case appserver.NotifyTurnCompleted:
			turnEnded(e.ThreadID) <- e.Turn.Status
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Initialize(ctx, appserver.InitializeParams{
		ClientInfo:   appserver.ClientInfo{Name: "sidera-realcodex", Version: "0"},
		Capabilities: &appserver.InitializeCapabilities{OptOutNotificationMethods: appserver.QuietNotifications},
	}); err != nil {
		t.Fatal(err)
	}

	// run starts a thread the way the adapter does, runs the command in one turn and
	// returns what the command printed.
	run := func(sandbox, command string, resume bool) string {
		t.Helper()
		preset := codexcfg.Preset{Sandbox: sandbox, CWD: project}
		tc, err := preset.ThreadConfig(codexcfg.Access{Program: "true", Env: map[string]string{}, DataDir: data})
		if err != nil {
			t.Fatal(err)
		}
		started, err := client.ThreadStart(ctx, appserver.ThreadStartParams{
			CWD: project, ApprovalPolicy: codexcfg.ApprovalNever, Sandbox: preset.ThreadSandbox(), Ephemeral: !resume, Config: tc,
		})
		if err != nil {
			t.Fatalf("%s: thread/start: %v", sandbox, err)
		}
		thread, loaded := started.Thread.ID, started
		if resume {
			// A thread is resumed with the same configuration and has to be protected
			// again; only a stored thread can be resumed, and it is stored once named.
			if err := client.ThreadSetName(ctx, thread, "sidera realcodex"); err != nil {
				t.Fatalf("%s: thread/name/set: %v", sandbox, err)
			}
			if loaded, err = client.ThreadResume(ctx, appserver.ThreadResumeParams{
				ThreadID: thread, CWD: project, ApprovalPolicy: codexcfg.ApprovalNever, Sandbox: preset.ThreadSandbox(), ExcludeTurns: true, Config: tc,
			}); err != nil {
				t.Fatalf("%s: thread/resume: %v", sandbox, err)
			}
		}
		for _, r := range []appserver.ThreadResult{started, loaded} {
			name, extends, protected := preset.ExpectedProfile()
			got := r.ActivePermissionProfile
			switch {
			case protected && (got == nil || got.ID != name || got.Extends == nil || *got.Extends != extends):
				t.Fatalf("%s: the thread reports the profile %+v, want %s extending %s", sandbox, got, name, extends)
			case !protected && got != nil:
				t.Fatalf("%s: a preset without a sandbox reports the profile %+v", sandbox, got)
			}
		}
		if _, err := client.TurnStart(ctx, appserver.TurnStartParams{
			ThreadID: thread, Input: []appserver.UserInput{appserver.TextInput("RUN[[" + command + "]]")},
			CWD: project, ApprovalPolicy: codexcfg.ApprovalNever, SandboxPolicy: preset.SandboxPolicy(),
		}); err != nil {
			t.Fatalf("%s: turn/start: %v", sandbox, err)
		}
		select {
		case status := <-turnEnded(thread):
			if status != "completed" {
				t.Fatalf("%s: the turn ended %s", sandbox, status)
			}
		case <-time.After(60 * time.Second):
			t.Fatalf("%s: the turn did not end", sandbox)
		}
		mu.Lock()
		defer mu.Unlock()
		return outputs[thread]
	}

	read := "cat '" + secretFile + "' 2>&1; echo \"exit=$?\""
	for _, tc := range []struct {
		sandbox  string
		resume   bool
		readable bool
	}{
		{codexcfg.SandboxWorkspaceWrite, false, false},
		{codexcfg.SandboxWorkspaceWrite, true, false},
		{codexcfg.SandboxReadOnly, false, false},
		{codexcfg.SandboxDangerFullAccess, false, true},
	} {
		out := run(tc.sandbox, read, tc.resume)
		t.Logf("%s (resumed %v): %q", tc.sandbox, tc.resume, out)
		if got := strings.Contains(out, secret); got != tc.readable {
			t.Errorf("%s (resumed %v): the command's output %q, secret readable = %v, want %v", tc.sandbox, tc.resume, out, got, tc.readable)
		}
		if !tc.readable && !strings.Contains(out, "exit=") {
			t.Errorf("%s: the command did not run at all: %q", tc.sandbox, out)
		}
	}

	// The profile is a sandbox and not only a denial: the commands of a
	// workspace-write thread still write to their project, and those of a read-only
	// thread cannot.
	for _, tc := range []struct {
		sandbox, command, want string
	}{
		{codexcfg.SandboxWorkspaceWrite, "touch '" + filepath.Join(project, "w") + "' 2>&1; echo \"exit=$?\"", "exit=0"},
		{codexcfg.SandboxReadOnly, "touch '" + filepath.Join(project, "r") + "' 2>&1; echo \"exit=$?\"", "exit=1"},
	} {
		if out := run(tc.sandbox, tc.command, false); !strings.Contains(out, tc.want) {
			t.Errorf("%s: %q gave %q, want %q", tc.sandbox, tc.command, out, tc.want)
		}
	}
}
