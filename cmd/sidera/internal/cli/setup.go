package cli

import (
	"fmt"
	"os"
	"path/filepath"
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
	exe, err := c.executablePath()
	if err != nil {
		return err
	}
	c.warnIfTemporary(exe)
	mcp := c.mcpServerCommand(exe)
	_, err = fmt.Fprintf(c.Stdout, `Run these once. This command only prints them; it changes no configuration.

Claude Code, for every session of your user:
  claude mcp add --scope user sidera -- %[1]s

Claude Code asks before each call of a tool of a server. To let the sidera tools run without asking, add this rule to the "permissions" "allow" list of %[2]s:
  "mcp__sidera"

Codex, in your user configuration:
  codex mcp add sidera -- %[1]s

Codex asks before each call of a tool that is not read-only. To let the sidera tools run without asking, add this line to the [mcp_servers.sidera] table that the command above created in %[3]s:
  default_tools_approval_mode = "approve"

The path of this program is stored in each configuration. If the program moves, run "claude mcp remove --scope user sidera" before adding it to Claude Code again, and add the Codex approval line again after "codex mcp add", which rewrites the table without it.

With the server added and allowed, a session you opened attaches itself to this node the first time it uses a Sidera tool, and can then send and receive messages like any Sidera session, including when it is woken without a person at its prompt.
`, mcp, userFile(runtime.GOOS, c.Getenv, "CLAUDE_CONFIG_DIR", ".claude", "settings.json"),
		userFile(runtime.GOOS, c.Getenv, "CODEX_HOME", ".codex", "config.toml"))
	return err
}

// userFile names a file of a client's user configuration as the person finds it on
// their platform: under the directory the environment variable names when the client
// is relocated by it, otherwise under dir in the user's profile.
func userFile(goos string, getenv func(string) string, envVar, dir, name string) string {
	if goos == "windows" {
		if getenv(envVar) != "" {
			return `%` + envVar + `%\` + name
		}
		return `%USERPROFILE%\` + dir + `\` + name
	}
	if getenv(envVar) != "" {
		return "$" + envVar + "/" + name
	}
	return "~/" + dir + "/" + name
}

// executablePath is the absolute path of the running program.
func (c *command) executablePath() (string, error) {
	exe, err := c.Executable()
	if err == nil && !filepath.IsAbs(exe) {
		exe, err = filepath.Abs(exe)
	}
	if err != nil {
		return "", &failure{"cannot find the path of this program to put into the command: " + err.Error()}
	}
	return exe, nil
}

// mcpServerCommand is the command a client starts as the MCP server. It names the
// program at exe, the running executable, by its absolute path: a client starts the server without a
// shell, and neither a Windows client (Codex reports "program not found" for the bare
// name although the executable is on the user's PATH) nor a client of a Linux session
// that is not a login shell (which has no ~/.local/bin on its PATH) can be relied on to
// find it by name. A node whose data directory is not the default is named in it by
// --home: the client starts the server with an environment of its own (Codex passes an
// allow-list of variables), so SIDERA_HOME would not reach it.
func (c *command) mcpServerCommand(exe string) string {
	words := []string{shellWord(runtime.GOOS, exe)}
	if home, err := c.dataDir(); err == nil && !c.isDefaultDataDir(home) {
		words = append(words, "--home", shellWord(runtime.GOOS, home))
	}
	return strings.Join(append(words, "mcp"), " ")
}

// warnIfTemporary warns when the program at exe is in a temporary directory, as the
// program that `go run` builds is: it is deleted when the command ends, so the path the
// printed commands name would not last.
func (c *command) warnIfTemporary(exe string) {
	for _, dir := range []string{os.TempDir(), c.Getenv("GOTMPDIR")} {
		if rel, err := filepath.Rel(dir, exe); dir != "" && err == nil && filepath.IsLocal(rel) {
			fmt.Fprintf(c.Stderr, "warning: this program runs from %s, a temporary directory, so the path in the commands below will not last. Run the command from an installed sidera.\n", exe)
			return
		}
	}
}

// isDefaultDataDir reports whether home is where a node of this user keeps its data
// when neither --home nor SIDERA_HOME says otherwise.
func (c *command) isDefaultDataDir(home string) bool {
	byDefault, err := datahome.Resolve("", datahome.Host{
		OS: runtime.GOOS, UserHomeDir: os.UserHomeDir, Lstat: os.Lstat,
		Getenv: func(name string) string {
			if name == datahome.Env {
				return ""
			}
			return c.Getenv(name)
		},
	})
	return err == nil && home == byDefault
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellWord quotes s for the shell a person pastes the command into on the given
// platform: always double quotes on Windows, where they are safe in both cmd and
// PowerShell for every character a user name may hold (', &, ( and ) among them), and
// single quotes elsewhere unless the word is plain.
func shellWord(goos, s string) string {
	if goos == "windows" {
		return `"` + s + `"`
	}
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
