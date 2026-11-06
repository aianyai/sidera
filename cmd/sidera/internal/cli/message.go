package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/internal/node/apiwire"
)

type bodyFlags struct{ text, file string }

func (b *bodyFlags) add(f *flag.FlagSet) {
	f.StringVar(&b.text, "body", "", "message text")
	f.StringVar(&b.file, "body-file", "", "file containing the message, or - for stdin")
}

func present(f *flag.FlagSet, name string) bool {
	found := false
	f.Visit(func(v *flag.Flag) {
		if v.Name == name {
			found = true
		}
	})
	return found
}

func addPresent(f *flag.FlagSet, in map[string]any, flagName, member string, value any) {
	if present(f, flagName) {
		in[member] = value
	}
}

func (c *command) body(f *flag.FlagSet, b bodyFlags) (string, error) {
	text, file := present(f, "body"), present(f, "body-file")
	if text && file {
		return "", usage("choose --body, --body-file, or standard input")
	}
	body := b.text
	if !text {
		var r io.Reader = c.Stdin
		if file && b.file != "-" {
			handle, err := os.Open(b.file)
			if err != nil {
				return "", apiwire.Malformed("cannot open the body file", "body")
			}
			defer handle.Close()
			r = handle
		}
		data, err := io.ReadAll(r)
		if err != nil {
			return "", apiwire.Malformed("cannot read the message body", "body")
		}
		body = string(data)
	}
	return body, nil
}

// messageRequest checks the arguments of a send, reply or control command, reads
// its body and builds its call.
func (c *command) messageRequest(args []string) (agentRequest, error) {
	f := c.arguments.flags
	m := &c.arguments.message
	p, err := parse(f, args[1:])
	if err != nil {
		return agentRequest{}, err
	}
	switch args[0] {
	case "send":
		if len(p) != 0 || !present(f, "to") {
			return agentRequest{}, usage("send needs --to <address>")
		}
	case "reply":
		if len(p) != 1 {
			return agentRequest{}, usage("reply needs a message identifier")
		}
	case "control":
		if len(p) != 2 {
			return agentRequest{}, usage("control needs an address and a signal")
		}
	}
	in := map[string]any{}
	if args[0] != "control" || present(f, "body") || present(f, "body-file") {
		text, err := c.body(f, m.body)
		if err != nil {
			return agentRequest{}, err
		}
		in["body"] = text
	}
	addPresent(f, in, "content-type", "content_type", m.content)
	addPresent(f, in, "thread", "thread", m.thread)
	addPresent(f, in, "key", "idempotency_key", m.key)
	addPresent(f, in, "accept-before", "accept_before_s", m.accept)
	switch args[0] {
	case "send":
		in["to"] = strings.Split(m.to, ",")
	case "reply":
		in["message_id"] = p[0]
	case "control":
		in["to"], in["signal"] = p[0], p[1]
		addPresent(f, in, "preset", "preset", m.preset)
	}
	return agentRequest{method: args[0], params: in, show: c.showSent}, nil
}

// showSent writes the answer of a send, reply or control.
func (c *command) showSent(out any) error {
	if c.json {
		return c.encode(out)
	}
	var err error
	switch sent := out.(type) {
	case agentapi.SendResult:
		_, err = fmt.Fprintf(c.Stdout, "sent %s to %s\n", sent.MessageID, strings.Join(sent.To, ","))
	case agentapi.ControlResult:
		_, err = fmt.Fprintf(c.Stdout, "sent %s to %s\n", sent.MessageID, sent.To)
	}
	return err
}

type messageFlags struct {
	body                             bodyFlags
	content, key, to, thread, preset string
	accept                           int64
}

func (m *messageFlags) add(f *flag.FlagSet, name string) {
	m.body.add(f)
	f.StringVar(&m.content, "content-type", "", "content type")
	f.StringVar(&m.key, "key", "", "explicit idempotency key")
	if name == "send" {
		f.StringVar(&m.to, "to", "", "one recipient")
	}
	if name != "control" {
		f.StringVar(&m.thread, "thread", "", "thread identifier")
	}
	if name != "reply" {
		f.Int64Var(&m.accept, "accept-before", 0, "delivery deadline in seconds")
	}
	if name == "control" {
		f.StringVar(&m.preset, "preset", "", "session preset")
	}
}
