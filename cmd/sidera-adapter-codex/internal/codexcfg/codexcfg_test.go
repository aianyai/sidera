package codexcfg

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func values(kv map[string]any) adapterproto.ConfigValues {
	v := adapterproto.ConfigValues{}
	for k, x := range kv {
		b, _ := json.Marshal(x)
		v[k] = b
	}
	return v
}

func TestParsePreset(t *testing.T) {
	for _, tc := range []struct {
		name     string
		in       map[string]any
		want     Preset
		problems string // a substring of the joined problems; empty means none
	}{
		{"defaults", map[string]any{"cwd": "/work"},
			Preset{Name: "p", CWD: "/work", Sandbox: "workspace-write", ApprovalPolicy: "never", Approvals: "deny"}, ""},
		{"everything", map[string]any{"cwd": "/work", "sandbox": "read-only", "approval_policy": "on-request", "approvals": "approve", "model": "gpt-6.1-sol", "effort": "low"},
			Preset{Name: "p", CWD: "/work", Sandbox: "read-only", ApprovalPolicy: "on-request", Approvals: "approve", Model: "gpt-6.1-sol", Effort: "low"}, ""},
		{"no cwd", map[string]any{}, Preset{}, "a project directory is required"},
		{"relative cwd", map[string]any{"cwd": "work"}, Preset{}, "absolute path"},
		{"bad sandbox", map[string]any{"cwd": "/w", "sandbox": "open"}, Preset{}, "sandbox"},
		{"bad policy", map[string]any{"cwd": "/w", "approval_policy": "always"}, Preset{}, "approval_policy"},
		{"bad effort", map[string]any{"cwd": "/w", "effort": "max"}, Preset{}, "effort"},
		{"not a string", map[string]any{"cwd": 3}, Preset{}, "a string is expected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, problems := ParsePreset("p", values(tc.in))
			if tc.problems != "" {
				if !strings.Contains(strings.Join(problems, "; "), tc.problems) {
					t.Fatalf("problems = %q, want one containing %q", problems, tc.problems)
				}
				return
			}
			if len(problems) != 0 || got != tc.want {
				t.Fatalf("got %+v, %q; want %+v", got, problems, tc.want)
			}
		})
	}
}

func TestParseInstance(t *testing.T) {
	in, err := ParseInstance(values(nil))
	if err != nil || in != (Instance{CodexProgram: "codex", SideraProgram: "sidera"}) {
		t.Fatalf("defaults: %+v, %v", in, err)
	}
	in, err = ParseInstance(values(map[string]any{"codex_program": "/opt/codex", "sidera_program": "/opt/sidera"}))
	if err != nil || in != (Instance{CodexProgram: "/opt/codex", SideraProgram: "/opt/sidera"}) {
		t.Fatalf("given: %+v, %v", in, err)
	}
	if _, err := ParseInstance(values(map[string]any{"codex_program": 1})); err == nil {
		t.Fatal("a number was accepted as a program")
	}
}

func TestFrontendsFollowTheSandbox(t *testing.T) {
	for sandbox, want := range map[string][]adapterproto.Frontend{
		SandboxReadOnly:         {adapterproto.FrontendMCP},
		SandboxWorkspaceWrite:   {adapterproto.FrontendMCP},
		SandboxDangerFullAccess: {adapterproto.FrontendMCP, adapterproto.FrontendCLI},
	} {
		if got := (Preset{Sandbox: sandbox}).Frontends(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: frontends = %v, want %v", sandbox, got, want)
		}
	}
}

func TestSandboxPolicy(t *testing.T) {
	for sandbox, want := range map[string]string{SandboxReadOnly: "readOnly", SandboxWorkspaceWrite: "workspaceWrite", SandboxDangerFullAccess: "dangerFullAccess"} {
		if got := (Preset{Sandbox: sandbox}).SandboxPolicy()["type"]; got != want {
			t.Errorf("%s: type = %v, want %s", sandbox, got, want)
		}
	}
}

// The configuration that carries a session's credentials is the contract with
// Codex: both places that need them, the approval of the channel's tools and no
// credentials in the arguments.
func TestThreadConfig(t *testing.T) {
	cfg := (Preset{}).ThreadConfig(Access{Program: "/bin/sidera", Env: map[string]string{"SIDERA_TOKEN": "tok", "SIDERA_HOME": "/home"}})
	raw, _ := json.Marshal(cfg)
	const want = `{"mcp_servers":{"sidera":{"args":["mcp"],"command":"/bin/sidera","default_tools_approval_mode":"approve","env":{"SIDERA_HOME":"/home","SIDERA_TOKEN":"tok"},"tool_timeout_sec":120}},"shell_environment_policy":{"set":{"SIDERA_HOME":"/home","SIDERA_TOKEN":"tok"}}}`
	if string(raw) != want {
		t.Fatalf("config = %s\nwant     %s", raw, want)
	}
}
