package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/claudeargs"
	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// callTimeout bounds one call of the claude command.
const callTimeout = 60 * time.Second

// mcpGetTimeout is how long `claude mcp get` waits for the server it connects to, in
// the milliseconds MCP_TIMEOUT counts in. Without it a server that does not answer
// holds every plan, check and refresh for about 30 seconds.
const mcpGetTimeout = "5000"

// CLI is Claude Code reached through its command, run by adaptersetup.ClientCommand:
// no shell, no standard input, a time limit, and the whole process group ended with it.
type CLI struct {
	// Path is the claude executable, absolute.
	Path string
	// Dir is the working directory of the calls. It must exist and hold no project
	// configuration, so that `mcp get` finds only what is in the user's scope. Claude
	// Code writes nothing into it.
	Dir string
	// Env is the environment of the calls; CLAUDE_CONFIG_DIR in it decides which
	// configuration they read and change.
	Env []string
	// Timeout bounds one call; zero is callTimeout.
	Timeout time.Duration
}

// NewCLI returns the client for the executable path, run in dir with the environment env.
func NewCLI(path, dir string, env []string) (*CLI, error) {
	if problem := claudeargs.ExecutableProblem(path, runtime.GOOS); problem != "" {
		return nil, errors.New(problem)
	}
	return &CLI{Path: path, Dir: dir, Env: env}, nil
}

func (c *CLI) command(args ...string) adaptersetup.ClientCommand {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = callTimeout
	}
	return adaptersetup.ClientCommand{Program: c.Path, Args: args, Dir: c.Dir, Env: c.Env, Timeout: timeout}
}

// run runs the command and returns it with what it wrote. An error is a command that
// did not run to an end; a non-zero exit is a result.
func (c *CLI) run(ctx context.Context, args ...string) (adaptersetup.ClientCommand, adaptersetup.ClientOutput, error) {
	cmd := c.command(args...)
	out, err := cmd.Run(ctx)
	return cmd, out, err
}

// jsonOutcome is the member of `--json` output that says how a command ended.
type jsonOutcome struct {
	Outcome     string `json:"outcome"`
	Message     string `json:"message"`
	FailureCode string `json:"failureCode"`
}

// alreadyDone is what a plugin command says when the state asked for is the state
// that is there; it is a success.
const alreadyDone = "already_in_goal_state"

// runJSON runs a `plugin` command with --json and returns nil when it did what was
// asked or found it already done.
func (c *CLI) runJSON(ctx context.Context, args ...string) error {
	cmd, out, err := c.run(ctx, append(args, "--json")...)
	if err != nil {
		return err
	}
	var res jsonOutcome
	_ = json.NewDecoder(bytes.NewReader(out.Stdout)).Decode(&res)
	if (out.ExitCode == 0 && res.Outcome != "failed") || res.FailureCode == alreadyDone {
		return nil
	}
	if res.Message != "" {
		out.Stderr = []byte(res.Message)
	}
	return cmd.Failure(out)
}

func (c *CLI) runPlain(ctx context.Context, args ...string) error {
	cmd, out, err := c.run(ctx, args...)
	if err != nil {
		return err
	}
	if out.ExitCode != 0 {
		return cmd.Failure(out)
	}
	return nil
}

// list runs a command that prints a JSON document and decodes it into v.
func (c *CLI) list(ctx context.Context, v any, args ...string) error {
	cmd, out, err := c.run(ctx, args...)
	if err != nil {
		return err
	}
	if out.ExitCode != 0 {
		return cmd.Failure(out)
	}
	if err := json.Unmarshal(out.Stdout, v); err != nil {
		return fmt.Errorf("%s: the output is not the list it was read as: %w", strings.Join(args, " "), err)
	}
	return nil
}

// MCPServer implements Client. `mcp get` connects to the server it prints, so it is
// run with a short MCP_TIMEOUT.
func (c *CLI) MCPServer(ctx context.Context) (*MCPServer, error) {
	get := *c
	get.Env = append(append([]string(nil), c.Env...), "MCP_TIMEOUT="+mcpGetTimeout)
	cmd, out, err := get.run(ctx, "mcp", "get", serverName)
	if err != nil {
		return nil, err
	}
	if out.ExitCode != 0 {
		if strings.Contains(string(out.Stdout)+string(out.Stderr), "No MCP server named") {
			return nil, nil
		}
		out.Stderr = append(out.Stdout, out.Stderr...)
		return nil, cmd.Failure(out)
	}
	return parseMCPGet(string(out.Stdout))
}

// parseMCPGet reads the text of `claude mcp get`: there is no machine-readable form.
// The lines it reads are `Scope:`, `Type:`, `Command:` and `Args:`; the first word of
// the scope must be "User", and the command is the rest of its line (a path may hold
// spaces). Text without a `Type:` line, or of a stdio server without a `Command:` line,
// is not what this was written for, and is an error: reading it as a server with no
// command would call a changed registration missing or changed.
func parseMCPGet(text string) (*MCPServer, error) {
	var scope, kind string
	var server MCPServer
	for _, line := range strings.Split(text, "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), ":")
		value = strings.TrimSpace(value)
		switch key {
		case "Scope":
			scope = value
		case "Type":
			kind = value
		case "Command":
			server.Command = value
		case "Args":
			server.Args = value
		}
	}
	if !strings.HasPrefix(scope, "User") {
		return nil, fmt.Errorf("a MCP server named %s exists in Claude Code's %q, not in the user's; setup registers it in the user's scope and will not leave another beside it. Remove it with `claude mcp remove %s` from the place it was added, then run setup again", serverName, scope, serverName)
	}
	switch {
	case kind == "":
		return nil, errors.New("`claude mcp get` printed no Type line, so the registration of the server cannot be read; Claude Code's output has changed")
	case kind != "stdio":
		server.Command, server.Args = "("+kind+" server)", ""
	case server.Command == "":
		return nil, errors.New("`claude mcp get` printed a stdio server with no Command line, so the registration cannot be read; Claude Code's output has changed")
	}
	return &server, nil
}

// AddMCPServer implements Client.
func (c *CLI) AddMCPServer(ctx context.Context, command string, args []string) error {
	return c.runPlain(ctx, append([]string{"mcp", "add", "--scope", "user", serverName, "--", command}, args...)...)
}

// RemoveMCPServer implements Client.
func (c *CLI) RemoveMCPServer(ctx context.Context) error {
	return c.runPlain(ctx, "mcp", "remove", serverName, "--scope", "user")
}

// Marketplaces implements Client.
func (c *CLI) Marketplaces(ctx context.Context) ([]Marketplace, error) {
	var list []struct{ Name, Path string }
	if err := c.list(ctx, &list, "plugin", "marketplace", "list", "--json"); err != nil {
		return nil, err
	}
	out := make([]Marketplace, len(list))
	for i, m := range list {
		out[i] = Marketplace{Name: m.Name, Path: m.Path}
	}
	return out, nil
}

// AddMarketplace implements Client.
func (c *CLI) AddMarketplace(ctx context.Context, dir string) error {
	return c.runJSON(ctx, "plugin", "marketplace", "add", dir)
}

// UpdateMarketplace implements Client.
func (c *CLI) UpdateMarketplace(ctx context.Context) error {
	return c.runJSON(ctx, "plugin", "marketplace", "update", marketplaceName)
}

// RemoveMarketplace implements Client.
func (c *CLI) RemoveMarketplace(ctx context.Context) error {
	return c.runJSON(ctx, "plugin", "marketplace", "remove", marketplaceName)
}

// Plugins implements Client.
func (c *CLI) Plugins(ctx context.Context) ([]Plugin, error) {
	var list []struct {
		ID            string   `json:"id"`
		Version       string   `json:"version"`
		FolderVersion string   `json:"folderVersion"`
		Scope         string   `json:"scope"`
		Enabled       bool     `json:"enabled"`
		Errors        []string `json:"errors"`
	}
	if err := c.list(ctx, &list, "plugin", "list", "--json"); err != nil {
		return nil, err
	}
	var out []Plugin
	for _, p := range list {
		if p.Scope == "user" {
			out = append(out, Plugin{ID: p.ID, Version: p.Version, SourceVersion: p.FolderVersion, Enabled: p.Enabled, Errors: p.Errors})
		}
	}
	return out, nil
}

// InstallPlugin implements Client.
func (c *CLI) InstallPlugin(ctx context.Context) error {
	return c.runJSON(ctx, "plugin", "install", pluginID, "--scope", "user")
}

// UpdatePlugin implements Client.
func (c *CLI) UpdatePlugin(ctx context.Context) error {
	return c.runJSON(ctx, "plugin", "update", pluginID, "--scope", "user")
}

// UninstallPlugin implements Client.
func (c *CLI) UninstallPlugin(ctx context.Context) error {
	return c.runJSON(ctx, "plugin", "uninstall", pluginID, "--scope", "user")
}
