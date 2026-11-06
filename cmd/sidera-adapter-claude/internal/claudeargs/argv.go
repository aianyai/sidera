package claudeargs

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aianyai/sidera/sdk/go/guidance"
)

// ChannelToolRules are the `--allowedTools` rules every turn passes: the
// channel's command-line and MCP tools. Whatever the permission mode, the
// agent can then reach the channel, and with `--permission-prompts none` a tool
// that is not allowed is denied at once instead of waiting for a person.
//
// The command line is allowed one agent command at a time and never as
// `Bash(sidera *)`: the management interface trusts the user's uid alone, so any
// `sidera` command that reaches it (preset, trust, adapter, identity, daemon
// stop, ...) would let a session act as the owner, and a session must never reach
// it. Only the commands of the agent interface are listed. In a rule, the space
// before the final `*` makes the match a whole word and also admits the command
// with no arguments, so one rule covers both. Claude Code checks each part of a
// compound command (`;`, `&&`, `|`, `$(...)`) against the rules, so an allowed
// command cannot carry an owner command with it. One gap remains: `status` is an
// agent command with a message identifier but the owner's node status without
// one, and the rule syntax cannot tell the two apart. The MCP tools are
// agent-only. `sidera mcp` needs no rule: Claude Code starts it from the MCP
// configuration, not through Bash.
var ChannelToolRules = []string{
	"Bash(sidera me *)",
	"Bash(sidera peers *)",
	"Bash(sidera directory *)",
	"Bash(sidera declare *)",
	"Bash(sidera send *)",
	"Bash(sidera reply *)",
	"Bash(sidera recv *)",
	"Bash(sidera ack *)",
	"Bash(sidera status *)",
	"Bash(sidera control *)",
	"Bash(sidera --help)",
	"mcp__sidera",
}

// DataDirDenyRules returns the `--disallowedTools` rules that keep Claude's own
// file tools out of Sidera's data directory: no read, which also covers Grep and
// Glob, and no edit. The directory holds the endpoint tokens of every session and
// the daemon's keys, and a session must reach the channel only through its own
// endpoint. Deny beats allow in every settings scope, so a preset's `allowed_tools`
// and the owner's own Claude settings cannot lift it. Claude Code checks `Edit`
// and `Read` rules only: a `Write` rule never matches, and a `Read` deny already
// stops the Write tool on the same path.
//
// The denial is best effort and not a boundary. Claude applies a read rule to its
// file tools and to the Bash commands it recognises as file commands (`cat`,
// `head`, `sed`, redirections), but not to a command that reads without naming
// the file, such as `grep -r pattern <parent of the data directory>` where the
// owner's settings allow `Bash(grep *)`, nor to a script that opens files itself.
// A guarantee needs Claude's operating-system sandbox, which the adapter does not
// use yet.
//
// dir is the absolute real path of the data directory, in the form of the
// platform it names: `/home/u/.sidera` or `C:\Users\u\Sidera`. In a rule an
// absolute path starts with `//` (a single `/` is relative to the settings
// source), and on Windows Claude Code matches the POSIX form of the path, so
// `C:\Users\u\Sidera` is written `//c/Users/u/Sidera`. Rules are gitignore
// patterns, so `\ * ? [ ]` in the path are escaped to match literally.
func DataDirDenyRules(dir string) ([]string, error) {
	var posix string
	switch {
	case isWindowsDrivePath(dir):
		posix = "/" + strings.ToLower(dir[:1]) + strings.ReplaceAll(dir[2:], `\`, "/")
	case strings.HasPrefix(dir, "/") && !strings.HasPrefix(dir, "//"):
		posix = dir
	default:
		return nil, fmt.Errorf("claudeargs: data directory %q is not an absolute local path", dir)
	}
	pattern := "/" + globEscaper.Replace(strings.TrimRight(posix, "/")) + "/**"
	return []string{"Read(" + pattern + ")", "Edit(" + pattern + ")"}, nil
}

var globEscaper = strings.NewReplacer(`\`, `\\`, "*", `\*`, "?", `\?`, "[", `\[`, "]", `\]`)

// isWindowsDrivePath reports whether p starts with a drive and a separator.
func isWindowsDrivePath(p string) bool {
	if len(p) < 3 || p[1] != ':' || (p[2] != '\\' && p[2] != '/') {
		return false
	}
	c := p[0] | 0x20
	return c >= 'a' && c <= 'z'
}

// Turn is one launch of a turn process: `claude -p` for one session.
type Turn struct {
	// Claude is the executable, passed as argv[0] as it is. A process of the
	// session is recognised by it, so it is never replaced by a resolved path.
	Claude string
	// SessionID is the session's uuid, on the command line of every turn
	// process of the session: it is how a process is found without a recorded PID.
	SessionID string
	// Resume continues an existing session (`--resume`); otherwise the session
	// is created with the uuid (`--session-id`), which fails once it exists.
	Resume bool
	// MCPConfig is the path of the MCP configuration file; see MCPConfig.
	MCPConfig string
	// DataDir is Sidera's data directory, absolute with symbolic links resolved;
	// see DataDirDenyRules. Every launch denies it.
	DataDir string
	Preset  Preset
}

// SessionFlag returns the flag that names the session, `--resume` or
// `--session-id`. Recognising a turn process looks for the flag and the uuid
// together, in this one place.
func SessionFlag(resume bool) string {
	if resume {
		return "--resume"
	}
	return "--session-id"
}

// IsTurnOf reports whether a process with the given argument vector is a turn
// process of the session: its argv[0] is the Claude executable as the adapter
// started it, and its arguments name the session with `--session-id` or
// `--resume`. A wrapper or a shell that merely mentions the uuid is not one.
func IsTurnOf(args []string, claude, session string) bool {
	if len(args) == 0 || args[0] != claude {
		return false
	}
	for i := 1; i+1 < len(args); i++ {
		if (args[i] == SessionFlag(false) || args[i] == SessionFlag(true)) && args[i+1] == session {
			return true
		}
	}
	return false
}

// Argv returns the argument vector of the turn process, argv[0] included. The
// input is read from standard input as stream-json, so that no prompt is on the
// command line, and the permission mode and the MCP configuration are given on
// every launch: a resumed session does not restore them.
func (t Turn) Argv() ([]string, error) {
	switch {
	case t.Claude == "":
		return nil, errors.New("claudeargs: no executable")
	case t.SessionID == "":
		return nil, errors.New("claudeargs: no session id")
	case t.MCPConfig == "":
		return nil, errors.New("claudeargs: no MCP configuration")
	case t.Preset.PermissionMode == "":
		return nil, errors.New("claudeargs: no permission mode")
	case t.DataDir == "":
		return nil, errors.New("claudeargs: no data directory")
	}
	deny, err := DataDirDenyRules(t.DataDir)
	if err != nil {
		return nil, err
	}
	argv := []string{
		t.Claude, "-p", SessionFlag(t.Resume), t.SessionID,
		"--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--strict-mcp-config", "--mcp-config", t.MCPConfig,
		"--permission-mode", t.Preset.PermissionMode,
		"--permission-prompts", "none",
		"--allowedTools",
	}
	argv = append(argv, ChannelToolRules...)
	if len(t.Preset.AllowedTools) > 0 {
		argv = append(argv, "--allowedTools")
		argv = append(argv, t.Preset.AllowedTools...)
	}
	argv = append(argv, "--disallowedTools")
	argv = append(argv, deny...)
	if t.Preset.Model != "" {
		argv = append(argv, "--model", t.Preset.Model)
	}
	if t.Preset.Effort != "" {
		argv = append(argv, "--effort", t.Preset.Effort)
	}
	argv = append(argv, "--append-system-prompt", SystemPrompt(t.Preset.AppendSystemPrompt))
	return argv, nil
}

// SystemPrompt is the text appended to Claude's system prompt: the statement every
// adapter gives, the sentence that is true only of Claude, then the preset's.
func SystemPrompt(preset string) string {
	if preset == "" {
		return systemPrompt
	}
	return fmt.Sprintf("%s\n\n%s", systemPrompt, preset)
}

// systemPrompt tells the agent what the channel is. The sentence of its own is for
// a session that has only the command-line front end, which cannot otherwise know
// that "recv" in a wake-up notice means `sidera recv`.
const systemPrompt = guidance.Full + "\n\n" +
	"If this session has no `sidera` MCP tools, use the commands: `recv` in a wake-up notice then means `sidera recv`."
