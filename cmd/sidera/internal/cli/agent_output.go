package cli

import (
	"fmt"
	"runtime"
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

// renderPeers is `peers`. The self flag is left out: the node itself is the row whose trust
// is "self".
func renderPeers(s *termout.Stream, r agentapi.PeersResult) string {
	rows := make([][]string, len(r.Nodes))
	for i, node := range r.Nodes {
		connection := "not connected"
		if node.Connected {
			connection = "connected"
		}
		rows[i] = []string{node.Trust, connection, node.Node}
	}
	text := s.Table([]string{"Trust", "Connection", "Node"}, rows)
	if len(r.Nodes) == 1 && r.Nodes[0].Self {
		text += "\nNo other node is trusted directly.\n"
	}
	return text
}

// renderDirectory is `directory`. node is the node that was asked ("" for this one): the
// cursor belongs to it, so the command that reads the next page names it too.
func renderDirectory(s *termout.Stream, r agentapi.DirectoryResult, node string) string {
	var text string
	if len(r.Endpoints) == 0 {
		text = "No endpoints.\n"
	} else {
		rows := make([][]string, len(r.Endpoints))
		for i, ep := range r.Endpoints {
			rows[i] = []string{ep.Kind, ep.DisplayName, strings.Join(ep.Accepts, ", "), ep.Address}
		}
		text = s.Table([]string{"Kind", "Name", "Accepts", "Address"}, rows)
	}
	if r.NextCursor != "" {
		command := "sidera directory"
		if node != "" {
			command += " --node " + node
		}
		command += " --cursor " + shellWord(runtime.GOOS, r.NextCursor)
		text += "\nMore endpoints follow. Run " + s.Command(command) + " for the next page.\n"
	}
	return text
}

// sentKind is how a command that sends something words its answer.
type sentKind struct {
	// headline says what happened.
	headline string
	// next says what to run the status command for.
	next string
}

var (
	messageSent = sentKind{"Message accepted for delivery.", "to see whether it was delivered"}
	replySent   = sentKind{"Reply accepted for delivery.", "to see whether it was delivered"}
	controlSent = sentKind{"Control signal accepted for delivery.", "to read the control reply"}
)

// renderSent is the answer of `send`, `reply` and `control`: the node accepted the message,
// which is not the same as its being delivered, so the line that says what to run next is
// part of the answer. A replay is a request the node had already carried out under the same
// key, which is not a new message.
func renderSent(s *termout.Stream, kind sentKind, id adapterproto.MessageID, to []string, replayed bool) string {
	block := s.Block([]termout.Field{
		{Label: "Message", Value: string(id)},
		{Label: "To", Value: strings.Join(to, "\n")},
	})
	text := s.OK(kind.headline+"\n"+block+"Run "+s.Command("sidera status "+string(id))+" "+kind.next+".") + "\n"
	if replayed {
		text += s.Warn("This request was accepted before under the same key; nothing was sent again.") + "\n"
	}
	return text
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
		switch item.Ack {
		case adapterproto.ItemRecorded, adapterproto.ItemCompleted, adapterproto.ItemDuplicate:
		default:
			outcome = s.Yellow(outcome)
		}
		rows[i] = []string{outcome, fmt.Sprint(item.Part), string(item.MessageID)}
	}
	return s.Table([]string{"Outcome", "Part", "Message"}, rows)
}

// renderStatus is `status <message-id>`: the delivery of each recipient and, once there is
// one, the control reply. now is what the age of a status reply is counted from.
func renderStatus(s *termout.Stream, now time.Time, r adapterproto.MessageStatusResult) string {
	rows := make([][]string, len(r.Recipients))
	for i, rec := range r.Recipients {
		state := string(rec.State)
		if rec.Reason != "" {
			state += " (" + string(rec.Reason) + ")"
		}
		rows[i] = []string{state, string(rec.To)}
	}
	text := s.Table([]string{"State", "To"}, rows)
	if r.ControlReply != nil {
		fields := controlReplyFields(s, now, *r.ControlReply)
		text += "\n" + s.Bold("Control reply: "+string(r.ControlReply.Signal)) + "\n" + termout.Indent(s.Block(fields), 2)
	}
	return text
}

// controlReplyFields are the fields of the reply to the signal, whichever it was.
func controlReplyFields(s *termout.Stream, now time.Time, reply adapterproto.ControlReply) []termout.Field {
	switch {
	case reply.Create != nil:
		return []termout.Field{
			{Label: "Outcome", Value: reply.Create.Outcome.String()},
			{Label: "Session endpoint", Value: string(reply.Create.SessionEndpoint)},
			{Label: "Reason", Value: reasonText(reply.Create.Reason)},
		}
	case reply.Close != nil:
		return []termout.Field{
			{Label: "Outcome", Value: reply.Close.Outcome.String()},
			{Label: "Reason", Value: reasonText(reply.Close.Reason)},
		}
	case reply.Interrupt != nil:
		return []termout.Field{
			{Label: "Outcome", Value: reply.Interrupt.Outcome.String()},
			{Label: "Reason", Value: reasonText(reply.Interrupt.Reason)},
		}
	}
	st := reply.Status
	conditions := make([]string, len(st.Conditions))
	for i, c := range st.Conditions {
		conditions[i] = c.String()
	}
	if len(conditions) == 0 {
		conditions = []string{"none"}
	}
	width := 0
	for _, p := range st.Presets {
		width = max(width, len(p.Name))
	}
	presets := make([]string, len(st.Presets))
	for i, p := range st.Presets {
		availability := "not available"
		if p.Available {
			availability = "available"
		}
		presets[i] = fmt.Sprintf("%-*s  %s", width, p.Name, availability)
	}
	return []termout.Field{
		{Label: "Activity", Value: st.Activity.String()},
		{Label: "Queued messages", Value: fmt.Sprint(st.Queued)},
		{Label: "Conditions", Value: strings.Join(conditions, ", ")},
		{Label: "As of", Value: s.Time(time.Unix(st.AsOf, 0), now)},
		{Label: "Presets", Value: strings.Join(presets, "\n")},
	}
}

// reasonText is the name of a reason, or nothing when the reply has none.
func reasonText[R interface {
	~uint8
	fmt.Stringer
}](reason R) string {
	if reason == 0 {
		return ""
	}
	return reason.String()
}
