// Package agentview writes the results that every program showing an endpoint's view of the
// network prints for people: the delivery state of a message and the control reply to it, a
// page of the directory, the list of peer nodes, the answer to a send, and the quoting of a
// word in a command to copy. `sidera` and the console adapter print the same results; one
// implementation keeps them from drifting, and a third-party adapter with a command line of
// its own gets the same words and layout by calling it.
//
// A [View] is a layout over a [termout.Stream], which decides the markers and colors. Its
// methods take the results as the adapter protocol names them and return the text. What is
// specific to the calling program is in the View: how a command of the program is spelled,
// which the commands it tells the reader to run are built with.
//
// The conventions are those of the command-line reference: a line that says what was done,
// blocks of labelled lines, tables with the identifier in the last column, protocol words
// written as the protocol names them, identifiers whole. Everything printed here is an
// identifier, an enumeration or a name checked where it enters the node, so nothing goes
// through the sanitizer; text that no source validates must, if a result one day carries it.
package agentview

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/termout"
)

// View is the output of one program on one stream.
type View struct {
	// Out is the stream the text will be written to.
	Out *termout.Stream
	// Command spells a command of the calling program as a person types it, from the words
	// after the program's name: the command, then its arguments, those that can hold any
	// text already quoted for the shell with [ShellWord]. It adds what the program was run
	// with that selects the node and the instance it acts on (a data directory, an instance
	// name), so that the hint reaches the same one as the command that printed it. The
	// hints that tell the reader what to run next rely on the program having the commands
	// `status <message-id>` and `directory [--node <id>] --cursor <cursor>`.
	Command func(args ...string) string
}

// Peers is the list of nodes the endpoint can address. The self flag is left out: the node
// itself is the row whose trust is "self".
func (v View) Peers(r adapterproto.PeersResult) string {
	rows := make([][]string, len(r.Nodes))
	for i, node := range r.Nodes {
		connection := "not connected"
		if node.Connected {
			connection = "connected"
		}
		rows[i] = []string{string(node.Trust), connection, node.Node}
	}
	text := v.Out.Table([]string{"Trust", "Connection", "Node"}, rows)
	if len(r.Nodes) == 1 && r.Nodes[0].Self {
		text += "\nNo other node is trusted directly.\n"
	}
	return text
}

// Directory is one page of a directory. node is the node that was asked ("" for this one):
// the cursor belongs to it, so the command that reads the next page names it too. The page
// must be one that was given; a directory that gave none (its outcome is not DirectoryOK)
// is a failure for the caller to report.
func (v View) Directory(r adapterproto.DirectoryResult, node string) string {
	var text string
	if len(r.Endpoints) == 0 {
		text = "No endpoints.\n"
	} else {
		rows := make([][]string, len(r.Endpoints))
		for i, ep := range r.Endpoints {
			accepts := make([]string, len(ep.Accepts))
			for j, a := range ep.Accepts {
				accepts[j] = string(a)
			}
			rows[i] = []string{string(ep.Kind), ep.DisplayName, strings.Join(accepts, ", "), string(ep.Address)}
		}
		text = v.Out.Table([]string{"Kind", "Name", "Accepts", "Address"}, rows)
	}
	if r.NextCursor != "" {
		args := []string{"directory"}
		if node != "" {
			args = append(args, "--node", node)
		}
		args = append(args, "--cursor", ShellWord(runtime.GOOS, r.NextCursor))
		text += "\nMore endpoints follow. Run " + v.Out.Command(v.Command(args...)) + " for the next page.\n"
	}
	return text
}

// SentKind is what was sent, which decides how the answer to the send is worded.
type SentKind int

const (
	// SentMessage is a message to the addresses given.
	SentMessage SentKind = iota
	// SentReply is a reply to a message that was received.
	SentReply
	// SentControl is a control signal, whose reply is read with the status command.
	SentControl
)

// Sent is the answer to a send, a reply or a control signal: the node accepted the message,
// which is not the same as its being delivered, so the line that says what to run next is
// part of the answer. A replay is a request the node had already carried out under the same
// key, which is not a new message. to are the recipients, left out when the sender does not
// know them (the node chose the recipient of a reply).
func (v View) Sent(kind SentKind, id adapterproto.MessageID, to []string, replayed bool) string {
	headline, next := "Message accepted for delivery.", "to see whether it was delivered"
	switch kind {
	case SentReply:
		headline = "Reply accepted for delivery."
	case SentControl:
		headline, next = "Control signal accepted for delivery.", "to read the control reply"
	}
	block := v.Out.Block([]termout.Field{
		{Label: "Message", Value: string(id)},
		{Label: "To", Value: strings.Join(to, "\n")},
	})
	text := v.Out.OK(headline+"\n"+block+"Run "+v.Out.Command(v.Command("status", string(id)))+" "+next+".") + "\n"
	if replayed {
		text += v.Out.Warn("This request was accepted before under the same key; nothing was sent again.") + "\n"
	}
	return text
}

// Status is the delivery of each recipient and, once there is one, the control reply. now is
// what the age of a status reply is counted from. The session endpoint of a created session
// is written as the reply carries it: the node that made it knows no address more complete.
func (v View) Status(now time.Time, r adapterproto.MessageStatusResult) string {
	rows := make([][]string, len(r.Recipients))
	for i, rec := range r.Recipients {
		state := string(rec.State)
		if rec.Reason != "" {
			state += " (" + string(rec.Reason) + ")"
		}
		rows[i] = []string{state, string(rec.To)}
	}
	text := v.Out.Table([]string{"State", "To"}, rows)
	if r.ControlReply != nil {
		fields := v.controlReplyFields(now, *r.ControlReply)
		text += "\n" + v.Out.Bold("Control reply: "+string(r.ControlReply.Signal)) + "\n" + termout.Indent(v.Out.Block(fields), 2)
	}
	return text
}

// controlReplyFields are the fields of the reply to the signal, whichever it was.
func (v View) controlReplyFields(now time.Time, reply adapterproto.ControlReply) []termout.Field {
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
	presets := make([]termout.Field, len(st.Presets))
	for i, p := range st.Presets {
		availability := "not available"
		if p.Available {
			availability = "available"
		}
		presets[i] = termout.Field{Label: p.Name, Value: availability}
	}
	return []termout.Field{
		{Label: "Activity", Value: st.Activity.String()},
		{Label: "Queued messages", Value: fmt.Sprint(st.Queued)},
		{Label: "Conditions", Value: strings.Join(conditions, ", ")},
		{Label: "As of", Value: v.Out.Time(time.Unix(st.AsOf, 0), now)},
		{Label: "Presets", Value: strings.TrimSuffix(v.Out.List(presets, nil, 0), "\n")},
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
