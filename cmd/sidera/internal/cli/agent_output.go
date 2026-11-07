package cli

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/termout"
)

// The human output of the agent commands. Every function here is a layout: it takes the
// stream it will be written to, which decides the markers and colors, and the result, and
// returns the text. The conventions they share:
//
//   - A command that changes something says what was done on its first line, a command that
//     reads starts with what it read.
//   - What describes one thing is a block of labelled lines; what lists many is a table, in
//     the order identity-last: the columns that say something come first and the identifier
//     last, so that a long one pushes nothing.
//   - Identifiers are written whole. Nothing is shortened to fit a width.
//   - Words that are part of the protocol (delivery states, outcomes, reasons, conditions,
//     actions such as session.create) are written as the protocol names them: agents look
//     them up in the guide and pass some back to a command. Booleans become words.
//   - Everything that is printed here is text the program or a validated source made: names
//     are checked where they enter, identifiers and enumerations are ASCII or fixed sets. So
//     nothing here goes through the sanitizer; text that is not validated at its source
//     must, when a command starts to print it.
//
// The results that the console adapter prints in the same way (the delivery status and
// control reply of a message, the directory, the peers and the answer to a send) are laid
// out by the SDK package agentview, which `sidera` calls through [command.view]; this file
// holds the rest and the mapping of the Agent interface's results to the types that package
// takes.

// renderMe is `me`.
func renderMe(s *termout.Stream, r agentapi.MeResult) string {
	fields := []termout.Field{
		{Label: "Address", Value: r.Address},
		{Label: "Node", Value: r.Node},
		{Label: "Name", Value: r.DisplayName},
		{Label: "Kind", Value: r.Kind},
	}
	fields = append(fields, declarationFields(r.Declaration)...)
	names := make([]string, 0, len(r.Frontends))
	for name := range r.Frontends {
		names = append(names, string(name))
	}
	slices.Sort(names)
	lines := make([]string, len(names))
	for i, name := range names {
		f := r.Frontends[adapterproto.Frontend(name)]
		lines[i] = fmt.Sprintf("%s (waits up to %s, %d %s per page)", name, termout.Duration(time.Duration(f.RecvWaitMaxS)*time.Second), f.Budget.Limit, f.Budget.Unit)
	}
	frontends := strings.Join(lines, "\n")
	if frontends == "" {
		frontends = "none"
	}
	return s.Block(append(fields, termout.Field{Label: "Frontends", Value: frontends}))
}

// renderProbe is `probe`: the node accepted the token, and the preset and front end it
// belongs to.
func renderProbe(s *termout.Stream, r agentapi.ProbeResult) string {
	return s.OK("The node is reachable.\n" + s.Block([]termout.Field{
		{Label: "Preset", Value: r.Preset},
		{Label: "Front end", Value: string(r.Frontend)},
	}))
}

// renderDeclared is `declare`: what the declaration is now.
func renderDeclared(s *termout.Stream, r agentapi.DeclareResult) string {
	return s.OK("Declaration updated.\n" + s.Block(declarationFields(r.Declaration)))
}

// declarationFields is the declaration in words, the same for `me` and `declare`. The
// sender list is in force only while senders are restricted; a list that is kept without
// the restriction is shown and said not to be in use, since setting one and forgetting the
// restriction is the likely mistake.
func declarationFields(d agentapi.Declaration) []termout.Field {
	receiving, shared, directory := "accepting", "accepted", "listed"
	if d.Paused {
		receiving = "paused"
	}
	if d.RefuseMultiRecipient {
		shared = "refused"
	}
	if d.HiddenFromDirectory {
		directory = "hidden"
	}
	var senders string
	switch {
	case d.RestrictSenders && len(d.AcceptFrom) == 0:
		senders = "nobody"
	case d.RestrictSenders:
		senders = "only the senders on the list"
	case len(d.AcceptFrom) == 0:
		senders = "anyone"
	default:
		senders = "anyone (the sender list is saved and not in use)"
	}
	return []termout.Field{
		{Label: "Receiving", Value: receiving},
		{Label: "Senders", Value: senders},
		{Label: "Sender list", Value: strings.Join(d.AcceptFrom, "\n")},
		{Label: "Shared messages", Value: shared},
		{Label: "Directory", Value: directory},
	}
}

// renderAck is `ack`: one row for each part, the outcome first. An outcome that is not an
// acknowledgement (the part was not handed out, the lease is not valid, the delivery had
// already ended) is yellow on a colored stream, so a refusal is seen among the rows that
// went through.
func renderAck(s *termout.Stream, r agentapi.AckResult) string {
	if len(r.Items) == 0 {
		return "No parts were acknowledged.\n"
	}
	rows := make([][]string, len(r.Items))
	for i, item := range r.Items {
		outcome := string(item.Ack)
		if !item.Ack.Acknowledged() {
			outcome = s.Yellow(outcome)
		}
		rows[i] = []string{outcome, fmt.Sprint(item.Part), string(item.MessageID)}
	}
	return s.Table([]string{"Outcome", "Part", "Message"}, rows)
}

// peersOf is the answer to `peers` as the adapter protocol writes it, which is what the
// view of agent results takes.
func peersOf(r agentapi.PeersResult) adapterproto.PeersResult {
	nodes := make([]adapterproto.PeerNode, len(r.Nodes))
	for i, n := range r.Nodes {
		nodes[i] = adapterproto.PeerNode{Node: n.Node, Self: n.Self, Trust: adapterproto.PeerTrust(n.Trust), Connected: n.Connected}
	}
	return adapterproto.PeersResult{Nodes: nodes}
}

// directoryPageOf is the answer to `directory` as the adapter protocol writes a page that
// was given.
func directoryPageOf(r agentapi.DirectoryResult) adapterproto.DirectoryResult {
	endpoints := make([]adapterproto.DirectoryEntry, len(r.Endpoints))
	for i, ep := range r.Endpoints {
		accepts := make([]adapterproto.EndpointAction, len(ep.Accepts))
		for j, a := range ep.Accepts {
			accepts[j] = adapterproto.EndpointAction(a)
		}
		endpoints[i] = adapterproto.DirectoryEntry{
			Address: adapterproto.Address(ep.Address), Kind: adapterproto.EndpointKind(ep.Kind),
			DisplayName: ep.DisplayName, Description: ep.Description, Accepts: accepts,
		}
	}
	return adapterproto.DirectoryResult{Outcome: adapterproto.DirectoryOK, Endpoints: endpoints, NextCursor: r.NextCursor}
}
