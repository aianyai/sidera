package codexcfg

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/guidance"
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

// A preset with a sandbox is a permission profile of the thread, and gives Codex no
// legacy sandbox mode and no per-turn policy: given either, Codex drops the profile.
func TestSandboxIsAProfileNotALegacyMode(t *testing.T) {
	for _, tc := range []struct {
		sandbox, extends, thread string
		policy                   map[string]any
	}{
		{SandboxReadOnly, ":read-only", "", nil},
		{SandboxWorkspaceWrite, ":workspace", "", nil},
		{SandboxDangerFullAccess, "", SandboxDangerFullAccess, map[string]any{"type": "dangerFullAccess"}},
	} {
		p := Preset{Sandbox: tc.sandbox}
		name, extends, ok := p.ExpectedProfile()
		if ok != (tc.extends != "") || extends != tc.extends || ok && name != ProfileName {
			t.Errorf("%s: expected profile = %q %q %v", tc.sandbox, name, extends, ok)
		}
		if got := p.ThreadSandbox(); got != tc.thread {
			t.Errorf("%s: thread sandbox = %q, want %q", tc.sandbox, got, tc.thread)
		}
		if got := p.SandboxPolicy(); !reflect.DeepEqual(got, tc.policy) {
			t.Errorf("%s: policy = %v, want %v", tc.sandbox, got, tc.policy)
		}
	}
}

// The configuration that carries a session's credentials is the contract with
// Codex: both places that need them, the approval of the channel's tools and no
// credentials in the arguments. A preset with a sandbox adds the profile that
// denies the data directory, as an exact table and without a sandbox key.
func TestThreadConfig(t *testing.T) {
	dir := t.TempDir()
	access := Access{Program: "/bin/sidera", Env: map[string]string{"SIDERA_TOKEN": "tok", "SIDERA_HOME": "/home"}, DataDir: dir}
	const base = `"mcp_servers":{"sidera":{"args":["mcp"],"command":"/bin/sidera","default_tools_approval_mode":"approve","enabled":true,"env":{"SIDERA_HOME":"/home","SIDERA_TOKEN":"tok"},"tool_timeout_sec":120}},"shell_environment_policy":{"set":{"SIDERA_HOME":"/home","SIDERA_TOKEN":"tok"}}`
	profile := func(extends string) string {
		d, _ := json.Marshal(dir)
		return `"default_permissions":"` + ProfileName + `",` + base + `,"permissions":{"` + ProfileName + `":{"extends":"` + extends + `","filesystem":{` + string(d) + `:"deny"}}}`
	}
	for _, tc := range []struct{ sandbox, want string }{
		{SandboxWorkspaceWrite, "{" + profile(":workspace") + "}"},
		{SandboxReadOnly, "{" + profile(":read-only") + "}"},
		{SandboxDangerFullAccess, "{" + base + "}"},
	} {
		cfg, err := (Preset{Sandbox: tc.sandbox}).ThreadConfig(access)
		if err != nil {
			t.Fatalf("%s: %v", tc.sandbox, err)
		}
		raw, _ := json.Marshal(cfg)
		var got, want any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: config = %s\nwant         %s", tc.sandbox, raw, tc.want)
		}
	}
	// With no data directory to deny, a sandbox would leave it readable: there is
	// no configuration to give. A preset without a sandbox needs none.
	access.DataDir = ""
	if _, err := (Preset{Sandbox: SandboxReadOnly}).ThreadConfig(access); err == nil {
		t.Error("a configuration was made without a data directory to deny")
	}
	if _, err := (Preset{Sandbox: SandboxDangerFullAccess}).ThreadConfig(access); err != nil {
		t.Errorf("a preset without a sandbox needs no data directory: %v", err)
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
	// The facts are the SDK's, proved there; the statement carries them whole.
	if !strings.Contains(ChannelStatement, guidance.Full) {
		t.Error("the statement does not carry the shared statement")
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

// The program a client starts is named by an absolute path, because the client starts
// it from a working directory of its own.
func TestResolveProgram(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	look := func(name string) (string, error) {
		switch name {
		case "sidera":
			return "bin/sidera", nil // a relative PATH entry
		case "./here":
			return "./here", nil
		}
		return "", errors.New("not found")
	}
	got, err := ResolveProgram("sidera", look)
	if err != nil || !filepath.IsAbs(got) || filepath.Base(got) != "sidera" {
		t.Errorf("a name found through a relative PATH entry = %q, %v", got, err)
	}
	if got, err := ResolveProgram("./here", look); err != nil || got != filepath.Join(dir, "here") {
		t.Errorf("a relative path = %q, %v", got, err)
	}
	// A program that is not found is kept as it was given, with the reason.
	if got, err := ResolveProgram("missing", look); err == nil || got != "missing" {
		t.Errorf("a missing program = %q, %v", got, err)
	}
}
