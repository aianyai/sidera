package claudeargs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// abs returns an absolute path of this platform.
func abs(elem ...string) string {
	return filepath.Join(append([]string{filepath.VolumeName(os.TempDir()) + string(filepath.Separator)}, elem...)...)
}

// channelRules is spelled out here on purpose: a rule added to ChannelToolRules is a
// command a session may run, and it has to show in a diff of the tests. Every
// command of the agent interface is here and none of the owner's (preset, trust,
// adapter, identity, daemon, ...): the management interface trusts the uid alone.
var channelRules = []string{
	"Bash(sidera me *)", "Bash(sidera peers *)", "Bash(sidera directory *)", "Bash(sidera declare *)",
	"Bash(sidera send *)", "Bash(sidera reply *)", "Bash(sidera recv *)", "Bash(sidera ack *)",
	"Bash(sidera status *)", "Bash(sidera control *)", "Bash(sidera --help)", "mcp__sidera",
}

func TestArgv(t *testing.T) {
	const id = "6ee62271-42c1-4558-89cf-0e32c50e0c1b"
	base := Turn{Claude: "/bin/claude", SessionID: id, MCPConfig: "/s/mcp.json", Preset: Preset{PermissionMode: "dontAsk"}}
	common := func(flag, mode string) []string {
		return []string{
			"/bin/claude", "-p", flag, id,
			"--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
			"--strict-mcp-config", "--mcp-config", "/s/mcp.json",
			"--permission-mode", mode, "--permission-prompts", "none",
			"--allowedTools",
		}
	}
	withChannel := func(flag, mode string, rest ...string) []string {
		return append(append(common(flag, mode), channelRules...), rest...)
	}
	full := base
	full.Resume = true
	full.Preset = Preset{PermissionMode: "plan", AllowedTools: []string{"Read", "Bash(git *)"}, Model: "opus", Effort: "high", AppendSystemPrompt: "Be brief."}

	tests := []struct {
		name string
		turn Turn
		want []string
	}{
		{"a new session names its uuid with --session-id", base,
			withChannel("--session-id", "dontAsk", "--append-system-prompt", SystemPrompt(""))},
		{"a resumed session names its uuid with --resume", func() Turn { t := base; t.Resume = true; return t }(),
			withChannel("--resume", "dontAsk", "--append-system-prompt", SystemPrompt(""))},
		{"preset tools, model, effort and prompt follow the channel's rules", full, withChannel("--resume", "plan",
			"--allowedTools", "Read", "Bash(git *)",
			"--model", "opus", "--effort", "high",
			"--append-system-prompt", SystemPrompt("Be brief."),
		)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.turn.Argv()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("argv\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestArgvRefusesWhatItCannotBuild(t *testing.T) {
	ok := Turn{Claude: "claude", SessionID: "u", MCPConfig: "m", Preset: Preset{PermissionMode: "plan"}}
	tests := []struct {
		name   string
		mutate func(*Turn)
	}{
		{"no executable", func(t *Turn) { t.Claude = "" }},
		{"no session", func(t *Turn) { t.SessionID = "" }},
		{"no MCP configuration", func(t *Turn) { t.MCPConfig = "" }},
		{"no permission mode", func(t *Turn) { t.Preset.PermissionMode = "" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			turn := ok
			tc.mutate(&turn)
			if _, err := turn.Argv(); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestSanitizeEnv(t *testing.T) {
	in := []string{
		"HOME=/home/u", "PATH=/bin", "CLAUDE_CONFIG_DIR=/cfg",
		"ANTHROPIC_API_KEY=k", "ANTHROPIC_AUTH_TOKEN=t", "ANTHROPIC_BASE_URL=https://x",
		"CLAUDECODE=1", "CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDE_CODE_SSE_PORT=1", "CLAUDE_PROJECT_DIR=/p",
		"SIDERA_TOKEN=s", "SIDERA_PROBE_TOKEN=p", "SIDERA_HOME=/h",
		"BAD=\xff", "NOEQUALS", "=x", "EMPTY=",
	}
	want := []string{"HOME=/home/u", "PATH=/bin", "CLAUDE_CONFIG_DIR=/cfg", "ANTHROPIC_BASE_URL=https://x", "SIDERA_HOME=/h", "EMPTY="}
	if got := SanitizeEnv(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestEnviron(t *testing.T) {
	sep := string(filepath.ListSeparator)
	tests := []struct {
		name  string
		base  []string
		extra map[string]string
		dir   string
		want  []string
	}{
		{"extra variables are added after the base, in name order", []string{"A=1"},
			map[string]string{"SIDERA_TOKEN": "tok", "SIDERA_HOME": "/h"}, "",
			[]string{"A=1", "SIDERA_HOME=/h", "SIDERA_TOKEN=tok"}},
		{"an extra variable replaces the base's", []string{"SIDERA_HOME=/old", "A=1"},
			map[string]string{"SIDERA_HOME": "/new"}, "", []string{"SIDERA_HOME=/new", "A=1"}},
		{"the sidera directory goes in front of PATH", []string{"PATH=/bin" + sep + "/usr/bin"}, nil, "/opt/sidera",
			[]string{"PATH=/opt/sidera" + sep + "/bin" + sep + "/usr/bin"}},
		{"no PATH gives the sidera directory alone", []string{"A=1"}, nil, "/opt/sidera", []string{"A=1", "PATH=/opt/sidera"}},
		{"an inherited token is never passed on", []string{"SIDERA_TOKEN=leak"}, nil, "", []string{}},
		{"the preset's variables survive the CLAUDE_CODE_ strip", nil,
			map[string]string{"CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS": "5"}, "", []string{"CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS=5"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Environ(tc.base, tc.extra, tc.dir)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestUserLine(t *testing.T) {
	line, err := UserLine("11111111-2222-4333-8444-555555555555", `say "hi" <b>`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(line), "\n") || strings.Count(string(line), "\n") != 1 {
		t.Fatalf("not exactly one line: %q", line)
	}
	var got map[string]any
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"type": "user", "uuid": "11111111-2222-4333-8444-555555555555", "session_id": "", "parent_tool_use_id": nil,
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": `say "hi" <b>`}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	for _, args := range [][2]string{{"", "x"}, {"u", ""}} {
		if _, err := UserLine(args[0], args[1]); err == nil {
			t.Fatalf("UserLine(%q, %q): want an error", args[0], args[1])
		}
	}
}

func TestMCPConfigHoldsNoToken(t *testing.T) {
	got, err := MCPConfig("/opt/sidera/sidera")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"mcpServers":{"sidera":{"command":"/opt/sidera/sidera","args":["mcp"]}}}` + "\n"
	if string(got) != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if _, err := MCPConfig(""); err == nil {
		t.Fatal("want an error without an executable")
	}
}

func TestParsePreset(t *testing.T) {
	dir := abs("work")
	cfg := func(kv ...string) map[string]json.RawMessage {
		m := map[string]json.RawMessage{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = json.RawMessage(kv[i+1])
		}
		return m
	}
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	ten := int64(10)
	tests := []struct {
		name     string
		cfg      map[string]json.RawMessage
		want     Preset
		problems []string
	}{
		{"the required values alone", cfg("project_dir", q(dir), "permission_mode", `"plan"`),
			Preset{ProjectDir: dir, PermissionMode: "plan"}, nil},
		{"every value", cfg("project_dir", q(dir), "permission_mode", `"bypassPermissions"`, "model", `"opus"`, "effort", `"max"`,
			"allowed_tools", `["Read"]`, "append_system_prompt", `"x"`, "background_wait_ms", `10`),
			Preset{ProjectDir: dir, PermissionMode: "bypassPermissions", Model: "opus", Effort: "max", AllowedTools: []string{"Read"},
				AppendSystemPrompt: "x", BackgroundWaitMS: &ten}, nil},
		{"a zero background wait is a value, not an absent one", cfg("project_dir", q(dir), "permission_mode", `"plan"`, "background_wait_ms", `0`),
			Preset{ProjectDir: dir, PermissionMode: "plan", BackgroundWaitMS: new(int64)}, nil},
		{"nothing given", cfg(), Preset{}, []string{"project_dir: required", "permission_mode: required"}},
		{"a relative directory", cfg("project_dir", `"work"`, "permission_mode", `"plan"`),
			Preset{ProjectDir: "work", PermissionMode: "plan"}, []string{"project_dir: not an absolute path"}},
		{"an unknown mode", cfg("project_dir", q(dir), "permission_mode", `"yolo"`),
			Preset{ProjectDir: dir, PermissionMode: "yolo"}, []string{`permission_mode: "yolo" is not one of ` + "[default acceptEdits plan auto dontAsk bypassPermissions]"}},
		{"an unknown effort", cfg("project_dir", q(dir), "permission_mode", `"plan"`, "effort", `"huge"`),
			Preset{ProjectDir: dir, PermissionMode: "plan", Effort: "huge"}, []string{`effort: "huge" is not one of [low medium high xhigh max]`}},
		{"a wrongly typed value", cfg("project_dir", q(dir), "permission_mode", `"plan"`, "model", `3`, "allowed_tools", `"Read"`, "background_wait_ms", `-1`),
			Preset{ProjectDir: dir, PermissionMode: "plan"},
			[]string{"model: not a string", "allowed_tools: not a list of strings", "background_wait_ms: not a non-negative integer"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, problems := ParsePreset(tc.cfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("preset\n got %+v\nwant %+v", got, tc.want)
			}
			if !reflect.DeepEqual(problems, tc.problems) {
				t.Errorf("problems\n got %q\nwant %q", problems, tc.problems)
			}
		})
	}
}

func TestPresetEnv(t *testing.T) {
	n := int64(0)
	if got := (Preset{BackgroundWaitMS: &n}).Env(); !reflect.DeepEqual(got, map[string]string{"CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS": "0"}) {
		t.Fatalf("got %v", got)
	}
	if got := (Preset{}).Env(); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestIsTurnOf(t *testing.T) {
	const id = "6ee62271-42c1-4558-89cf-0e32c50e0c1b"
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"a new session", []string{"/bin/claude", "-p", "--session-id", id, "--verbose"}, true},
		{"a resumed session", []string{"/bin/claude", "-p", "--resume", id}, true},
		{"another executable with the same arguments", []string{"/usr/bin/node", "-p", "--resume", id}, false},
		{"a shell that mentions the uuid", []string{"/bin/claude", "-c", "echo --resume " + id}, false},
		{"another session", []string{"/bin/claude", "--resume", "other"}, false},
		{"the uuid is not the value of a session flag", []string{"/bin/claude", "--model", id}, false},
		{"the flag is last", []string{"/bin/claude", "--resume"}, false},
		{"no arguments", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTurnOf(tc.args, "/bin/claude", id); got != tc.want {
				t.Fatalf("IsTurnOf(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// The adapter's part of the system prompt says what the channel and its tools do and
// nothing about how to treat a message, which is the owner's policy and belongs in
// the preset. A receive that is not acknowledged hands the same page out again at
// once; a session that does not know it concludes that no reply came.
func TestSystemPromptStatesTheChannel(t *testing.T) {
	for _, fact := range []string{"owner's own cluster", "other agents and people", "recv", "reply", "send", "control", "status", "ack",
		"sidera MCP server", "again at once", "sidera ack <lease>", "sidera recv --ack <lease>", "wait for new messages"} {
		if !strings.Contains(SystemPrompt(""), fact) {
			t.Errorf("the system prompt does not say %q", fact)
		}
	}
}
