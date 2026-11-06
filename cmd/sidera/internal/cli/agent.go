package cli

import (
	"strconv"
	"strings"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// agentRequest is an agent command whose arguments have been checked: the one
// method it calls and with what, and how the answer is written. Checking comes
// before the client is opened, so a usage error is reported before any call and
// whatever is wrong with the data directory or the socket does not hide it.
type agentRequest struct {
	method string
	params map[string]any
	show   func(answer any) error
}

// agentRequest checks the arguments of an agent command other than `mcp` and builds
// its call.
func (c *command) agentRequest(args []string) (agentRequest, error) {
	switch args[0] {
	case "me", "peers", "directory", "declare", "probe":
		return c.queryRequest(args)
	case "send", "reply", "control":
		return c.messageRequest(args)
	case "status":
		p, err := parse(c.arguments.flags, args[1:])
		if err != nil {
			return agentRequest{}, err
		}
		if len(p) != 1 {
			return agentRequest{}, usage("status needs a message identifier")
		}
		return agentRequest{method: "status", params: map[string]any{"message_id": p[0]}, show: c.result}, nil
	case "recv":
		return c.recvRequest(args)
	case "ack":
		return c.ackRequest(args)
	}
	return agentRequest{}, usage("unknown command: " + args[0])
}

func (c *command) recvRequest(args []string) (agentRequest, error) {
	f := c.arguments.flags
	in := map[string]any{"frontend": "cli"}
	p, err := parse(f, args[1:])
	if err != nil {
		return agentRequest{}, err
	}
	if len(p) != 0 {
		return agentRequest{}, usage("recv takes no positional arguments")
	}
	addPresent(f, in, "wait", "wait_s", c.arguments.recv.wait)
	addPresent(f, in, "msg", "msg", c.arguments.recv.msg)
	addPresent(f, in, "part", "part", c.arguments.recv.part)
	addPresent(f, in, "ack", "ack_lease", c.arguments.recv.ack)
	return agentRequest{method: "recv", params: in, show: func(page any) error {
		if c.afterRecv != nil {
			c.afterRecv()
		}
		_, err := c.Stdout.Write(page.([]byte))
		return err
	}}, nil
}

func (c *command) ackRequest(args []string) (agentRequest, error) {
	p, err := parse(c.arguments.flags, args[1:])
	if err != nil {
		return agentRequest{}, err
	}
	if len(p) < 1 {
		return agentRequest{}, usage("ack needs a lease")
	}
	in := map[string]any{"lease": p[0]}
	var items []adapterproto.PartRef
	for _, value := range p[1:] {
		id, part, ok := strings.Cut(value, ":")
		k, err := strconv.Atoi(part)
		if !ok || err != nil {
			return agentRequest{}, usage("ack items are message-id:part")
		}
		items = append(items, adapterproto.PartRef{MessageID: adapterproto.MessageID(id), Part: k})
	}
	if len(items) > 0 {
		in["items"] = items
	}
	return agentRequest{method: "ack", params: in, show: c.result}, nil
}
