package claudeargs

import (
	"errors"
	"fmt"
)

// ChannelToolRules are the `--allowedTools` rules every turn passes: the
// channel's command-line and MCP tools. Whatever the permission mode, the
// agent can then reach the channel, and with `--permission-prompts none` a tool
// that is not allowed is denied at once instead of waiting for a person.
var ChannelToolRules = []string{"Bash(sidera *)", "mcp__sidera"}

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
	Preset    Preset
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
	if t.Preset.Model != "" {
		argv = append(argv, "--model", t.Preset.Model)
	}
	if t.Preset.Effort != "" {
		argv = append(argv, "--effort", t.Preset.Effort)
	}
	argv = append(argv, "--append-system-prompt", SystemPrompt(t.Preset.AppendSystemPrompt))
	return argv, nil
}

// SystemPrompt is the text appended to Claude's system prompt: the adapter's
// own, then the preset's.
func SystemPrompt(preset string) string {
	if preset == "" {
		return systemPrompt
	}
	return fmt.Sprintf("%s\n\n%s", systemPrompt, preset)
}

// systemPrompt tells the agent what the channel is. Without it a session that
// has only the command-line front end cannot know that "recv" in a wake-up
// notice means `sidera recv`.
const systemPrompt = `This session is connected to Sidera, a channel on which you exchange messages with other agents and people in your owner's own cluster.
You are woken by a short notification that a message is waiting. Read it with the recv tool of the sidera MCP server, or with "sidera recv" in the shell when the MCP tools are not available, and answer with reply ("sidera reply"). Use send ("sidera send") to write to another address, control ("sidera control") for control signals, and status ("sidera status") to see what became of a message you sent. "sidera --help" lists the other commands.
recv hands out a page that has not been acknowledged again at once, with the same lease, instead of waiting. A page is acknowledged with the ack tool ("sidera ack <lease>") or by passing its lease to the next recv (ack_lease, or "sidera recv --ack <lease>"). Only after that does recv with a wait wait for new messages.`
