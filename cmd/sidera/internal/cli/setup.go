package cli

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"

	"github.com/aianyai/sidera/internal/datahome"
)

// setup prints what a person runs once to let their own sessions become
// controllers. It never runs the commands and never edits a configuration: the
// client's configuration is the owner's.
func (c *command) setup(args []string) error {
	if len(args) == 0 || args[0] != "controller" {
		return usage("setup needs a topic: controller")
	}
	p, err := parse(c.arguments.flags, args[1:])
	if err != nil {
		return err
	}
	if len(p) != 0 {
		return usage("setup controller takes no arguments")
	}
	mcp := c.mcpServerCommand()
	_, err = fmt.Fprintf(c.Stdout, `Run these once. This command only prints them; it changes no configuration.

Claude Code, for every session of your user:
  claude mcp add --scope user sidera -- %[1]s

Codex, in your user configuration (~/.codex/config.toml):
  codex mcp add sidera -- %[1]s

With the server added, a session you opened attaches itself to this node the first time it uses a Sidera tool, and can then send and receive messages like any Sidera session.
`, mcp)
	return err
}

// mcpServerCommand is the command a client starts as the MCP server. A node whose
// data directory is not the default is named in it by --home: the client starts the
// server with an environment of its own (Codex passes an allow-list of variables), so
// SIDERA_HOME would not reach it.
func (c *command) mcpServerCommand() string {
	home, err := c.dataDir()
	if err != nil {
		return "sidera mcp" // no data directory can be named; the server finds the default itself
	}
	byDefault, err := datahome.Resolve("", datahome.Host{
		OS: runtime.GOOS, UserHomeDir: os.UserHomeDir, Lstat: os.Lstat,
		Getenv: func(name string) string {
			if name == datahome.Env {
				return ""
			}
			return c.Getenv(name)
		},
	})
	if err == nil && home == byDefault {
		return "sidera mcp"
	}
	return "sidera --home " + shellWord(home) + " mcp"
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellWord quotes s for the shell a person pastes the command into: double quotes
// around a path with a space on Windows, single quotes elsewhere unless the word is
// plain.
func shellWord(s string) string {
	if runtime.GOOS == "windows" {
		if strings.ContainsAny(s, " \t") {
			return `"` + s + `"`
		}
		return s
	}
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
