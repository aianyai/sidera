package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

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
		text = renderPeers(c.out, out)
	case agentapi.AckResult:
		text = renderAck(c.out, out)
	case adapterproto.MessageStatusResult:
		text = renderStatus(c.out, time.Now(), out)
	case agentapi.ProbeResult:
		text = fmt.Sprintf("reachable %s %s\n", out.Preset, out.Frontend)
	default:
		return fmt.Errorf("cli: unsupported human result %T", v)
	}
	_, err := io.WriteString(c.out, text)
	return err
}

// jsonText is a value as one line of JSON, for the management commands' tables.
func jsonText(v any) (string, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	err := e.Encode(v)
	return strings.TrimSuffix(b.String(), "\n"), err
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
