package cli

import (
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/agentview"
)

// view is the layout of the agent results that `sidera` shares with the console adapter,
// written on the standard output stream. The commands it names in its hints are sidera's,
// spelled for the data directory this command ran on.
func (c *command) view() agentview.View {
	return agentview.View{Out: c.out, Command: c.hint}
}

// hint spells a `sidera` command for the data directory this command ran on (see
// [sideraCommand]). The command has already connected to that directory, so it resolves.
func (c *command) hint(args ...string) string {
	home, err := c.dataDir()
	return sideraCommand(home, err != nil || c.isDefaultDataDir(home), args...)
}

// sideraCommand is a `sidera` command as a person types it, from the words after the
// program's name (those that can hold any text already quoted): a node whose data directory
// is not the default is named by --home, as the command a client starts for the MCP server
// names it. It is the one rule for the commands this program tells a person to run.
func sideraCommand(home string, homeIsDefault bool, args ...string) string {
	words := []string{"sidera"}
	if !homeIsDefault {
		words = append(words, "--home", agentview.ShellWord(runtime.GOOS, home))
	}
	return strings.Join(append(words, args...), " ")
}

// result writes the human form of an agent command's answer, or the answer itself with
// --json. The layouts are in agent_output.go.
func (c *command) result(v any) error {
	if c.json {
		return c.encode(v)
	}
	var text string
	switch out := v.(type) {
	case agentapi.MeResult:
		text = renderMe(c.out, out)
	case agentapi.DeclareResult:
		text = renderDeclared(c.out, out)
	case agentapi.PeersResult:
		text = c.view().Peers(peersOf(out))
	case agentapi.AckResult:
		text = renderAck(c.out, out)
	case adapterproto.MessageStatusResult:
		text = c.view().Status(time.Now(), out)
	case agentapi.ProbeResult:
		text = renderProbe(c.out, out)
	default:
		return fmt.Errorf("cli: unsupported human result %T", v)
	}
	_, err := io.WriteString(c.out, text)
	return err
}

// version is `version`: what this program was built from, which needs no node.
func (c *command) version(args []string) error {
	p, err := parse(c.arguments.flags, args)
	if err != nil {
		return err
	}
	if len(p) != 0 {
		return usage("version takes no arguments")
	}
	if c.json {
		return c.encode(c.Release)
	}
	_, err = fmt.Fprintln(c.Stdout, c.Release.Describe("sidera"))
	return err
}
