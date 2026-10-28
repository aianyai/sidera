package cli

import (
	"context"
	"flag"
	"strings"

	"github.com/aianyai/sidera/internal/node/agentapi"
)

func (c *command) query(ctx context.Context, client *agentapi.Client, args []string) error {
	f := c.arguments.flags
	q := &c.arguments.query
	p, err := parse(f, args[1:])
	if err != nil {
		return err
	}
	if len(p) != 0 {
		return usage(args[0] + " takes no positional arguments")
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
	out, err := client.Call(ctx, args[0], in)
	if err != nil {
		return err
	}
	return c.result(out)
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
