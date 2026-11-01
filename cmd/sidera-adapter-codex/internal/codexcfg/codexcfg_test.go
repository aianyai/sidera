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
	cwd := t.TempDir()
	for _, tc := range []struct {
		name     string
		in       map[string]any
		want     Preset
		problems string // a substring of the joined problems; empty means none
	}{
		{"defaults", map[string]any{"cwd": cwd},
			Preset{Name: "p", CWD: cwd, Sandbox: "workspace-write", ApprovalPolicy: "never", Approvals: "deny"}, ""},
		{"everything", map[string]any{"cwd": cwd, "sandbox": "read-only", "approval_policy": "on-request", "approvals": "approve", "model": "gpt-6.1-sol", "effort": "low"},
			Preset{Name: "p", CWD: cwd, Sandbox: "read-only", ApprovalPolicy: "on-request", Approvals: "approve", Model: "gpt-6.1-sol", Effort: "low"}, ""},
		{"instructions", map[string]any{"cwd": cwd, "developer_instructions": "Answer in French."},
			Preset{Name: "p", CWD: cwd, Sandbox: "workspace-write", ApprovalPolicy: "never", Approvals: "deny", DeveloperInstructions: "Answer in French."}, ""},
		{"instructions too long", map[string]any{"cwd": cwd, "developer_instructions": strings.Repeat("x", MaxDeveloperInstructions+1)}, Preset{}, "developer_instructions"},
		{"no cwd", map[string]any{}, Preset{}, "a project directory is required"},
		{"relative cwd", map[string]any{"cwd": "work"}, Preset{}, "absolute path"},
		{"bad sandbox", map[string]any{"cwd": cwd, "sandbox": "open"}, Preset{}, "sandbox"},
		{"bad policy", map[string]any{"cwd": cwd, "approval_policy": "always"}, Preset{}, "approval_policy"},
		{"bad effort", map[string]any{"cwd": cwd, "effort": "max"}, Preset{}, "effort"},
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

// The text Codex gets as developer instructions is the adapter's statement of what
// the channel is, then the owner's own instructions after a blank line. The
// adapter's part says nothing about how to treat a message: that is the owner's
// policy.
func TestDeveloperInstructions(t *testing.T) {
	for _, tc := range []struct{ name, preset, want string }{
		{"no preset text", "", ChannelStatement},
		{"preset text follows", "Be brief.", ChannelStatement + "\n\nBe brief."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeveloperInstructions(tc.preset); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	for _, fact := range []string{"owner's own cluster", "other agents and people", "recv", "reply", "send", "control", "status", "ack", "sidera MCP server", "sandbox", "again at once", "sidera ack <lease>", "sidera recv --ack <lease>", "wait for new messages"} {
		if !strings.Contains(ChannelStatement, fact) {
			t.Errorf("the statement does not say %q", fact)
		}
	}
}

func TestPresetFieldsDeclareDeveloperInstructions(t *testing.T) {
	for _, f := range PresetFields() {
		if f.Key == KeyDeveloperInstructions {
			return
		}
	}
	t.Fatal("the manifest does not declare developer_instructions")
}

// A string that is not UTF-8 is refused as it is, before decoding would turn its
// bad bytes into U+FFFD and change what the owner wrote.
func TestParsePresetRefusesInstructionsThatAreNotUTF8(t *testing.T) {
	v := values(map[string]any{"cwd": t.TempDir()})
	v[KeyDeveloperInstructions] = json.RawMessage("\"bad \xff\"")
	_, problems := ParsePreset("p", v)
	if !strings.Contains(strings.Join(problems, "; "), "UTF-8") {
		t.Fatalf("problems = %q", problems)
	}
}
