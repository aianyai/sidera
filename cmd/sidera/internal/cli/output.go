package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func (c *command) result(v any) error {
	if c.json {
		return c.encode(v)
	}
	w := tabwriter.NewWriter(c.Stdout, 0, 2, 2, ' ', 0)
	switch out := v.(type) {
	case agentapi.MeResult:
		fmt.Fprintf(w, "address %s\nnode %s\nkind %s\ndisplay_name %s\n", out.Address, out.Node, out.Kind, out.DisplayName)
		printDeclaration(w, out.Declaration)
		names := make([]string, 0, len(out.Frontends))
		for name := range out.Frontends {
			names = append(names, string(name))
		}
		sort.Strings(names)
		for _, name := range names {
			f := out.Frontends[adapterproto.Frontend(name)]
			fmt.Fprintf(w, "frontend %s wait_max_s=%d budget=%d %s\n", name, f.RecvWaitMaxS, f.Budget.Limit, f.Budget.Unit)
		}
	case agentapi.DeclareResult:
		printDeclaration(w, out.Declaration)
	case agentapi.PeersResult:
		fmt.Fprintln(w, "NODE\tSELF")
		for _, node := range out.Nodes {
			fmt.Fprintf(w, "%s\t%t\n", node.Node, node.Self)
		}
	case agentapi.DirectoryResult:
		fmt.Fprintln(w, "ADDRESS\tKIND\tNAME\tACCEPTS")
		for _, ep := range out.Endpoints {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", ep.Address, ep.Kind, ep.DisplayName, strings.Join(ep.Accepts, ","))
		}
		if out.NextCursor != "" {
			fmt.Fprintln(w, "next_cursor", out.NextCursor)
		}
	case agentapi.ProbeResult:
		fmt.Fprintf(w, "reachable %s %s\n", out.Preset, out.Frontend)
	case agentapi.AckResult:
		for _, item := range out.Items {
			fmt.Fprintf(w, "%s:%d %s\n", item.MessageID, item.Part, item.Ack)
		}
	case adapterproto.MessageStatusResult:
		for _, recipient := range out.Recipients {
			fmt.Fprintf(w, "%s %s", recipient.To, recipient.State)
			if recipient.Reason != "" {
				fmt.Fprintf(w, " (%s)", recipient.Reason)
			}
			fmt.Fprintln(w)
		}
		if out.ControlReply != nil {
			raw, err := jsonText(out.ControlReply)
			if err != nil {
				return err
			}
			fmt.Fprintln(w, "control_reply", raw)
		}
	default:
		return fmt.Errorf("cli: unsupported human result %T", v)
	}
	return w.Flush()
}

func jsonText(v any) (string, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	err := e.Encode(v)
	return strings.TrimSuffix(b.String(), "\n"), err
}

func printDeclaration(w *tabwriter.Writer, d agentapi.Declaration) {
	fmt.Fprintf(w, "paused %t\nrefuse_multi_recipient %t\nrestrict_senders %t\naccept_from %s\nhidden_from_directory %t\n", d.Paused, d.RefuseMultiRecipient, d.RestrictSenders, strings.Join(d.AcceptFrom, ","), d.HiddenFromDirectory)
}
