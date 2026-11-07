package cli

import (
	"flag"
	"io"
	"strings"

	"github.com/aianyai/sidera/internal/node/agentapi"
)

// queryRequest checks the arguments of an identity, declaration, directory, peers
// or probe command and builds its call.
func (c *command) queryRequest(args []string) (agentRequest, error) {
	f := c.arguments.flags
	q := &c.arguments.query
	p, err := parse(f, args[1:])
	if err != nil {
		return agentRequest{}, err
	}
	if len(p) != 0 {
		return agentRequest{}, usage(args[0] + " takes no positional arguments")
	}
	in := map[string]any{}
	switch args[0] {
	case "directory":
		addPresent(f, in, "node", "node", q.node)
		addPresent(f, in, "cursor", "cursor", q.cursor)
		addPresent(f, in, "limit", "limit", q.limit)
	case "declare":
		addPresent(f, in, "paused", "paused", q.paused)
		addPresent(f, in, "refuse-multi-recipient", "refuse_multi_recipient", q.multi)
		addPresent(f, in, "restrict-senders", "restrict_senders", q.restrict)
		addPresent(f, in, "hidden", "hidden_from_directory", q.hidden)
		senders := []string{}
		if q.from != "" {
			senders = strings.Split(q.from, ",")
		}
		addPresent(f, in, "accept-from", "accept_from", senders)
	case "probe":
		in["frontend"] = "cli"
	}
	show := c.result
	if args[0] == "directory" {
		show = func(answer any) error { return c.showDirectory(answer, q.node) }
	}
	return agentRequest{method: args[0], params: in, show: show}, nil
}

// showDirectory writes a directory page; node is the one that was asked, "" for this node.
func (c *command) showDirectory(answer any, node string) error {
	if c.json {
		return c.encode(answer)
	}
	_, err := io.WriteString(c.out, c.view().Directory(directoryPageOf(answer.(agentapi.DirectoryResult)), node))
	return err
}

type queryFlags struct {
	node, cursor, from              string
	limit                           int
	paused, multi, restrict, hidden bool
}

func (q *queryFlags) add(f *flag.FlagSet, name string) {
	if name == "directory" {
		f.StringVar(&q.node, "node", "", "node to list")
		f.StringVar(&q.cursor, "cursor", "", "continuation cursor")
		f.IntVar(&q.limit, "limit", 0, "page limit")
	}
	if name == "declare" {
		f.BoolVar(&q.paused, "paused", false, "pause receiving")
		f.BoolVar(&q.multi, "refuse-multi-recipient", false, "refuse shared messages")
		f.BoolVar(&q.restrict, "restrict-senders", false, "restrict senders")
		f.BoolVar(&q.hidden, "hidden", false, "hide from directory")
		f.StringVar(&q.from, "accept-from", "", "comma-separated sender addresses; empty clears")
	}
}
