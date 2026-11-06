// Package guidance holds what an agent is told about Sidera, written once. One
// set of facts is composed into three texts that differ only in how much they
// add around those facts:
//
//   - [Short] is the instructions of the `sidera` MCP server: one line, for
//     every session that has the server, including one its owner opened.
//   - [Full] is what an adapter tells the sessions it starts: the facts and the
//     tools that act on them, to which each adapter appends only what is true of
//     its own client.
//   - [Skill] is a self-contained SKILL.md: [Full] and the operating procedure.
//
// The texts name a tool only by its MCP name (`recv`), never by the name a client
// composes for it, because that name differs between clients and between the ways
// a server is registered.
package guidance

import (
	_ "embed"
	"strings"
)

// The shared facts. Each appears word for word in all three texts, which a test
// pins, so that a change to one cannot reach one text and miss another. The
// order is the order of the short text: a client that shows only the start of
// the instructions shows what matters most, and the first three facts together
// stay within what Codex keeps of a deferred server, which a test pins.
const (
	factChannel = "Sidera is the message channel to the other agents and people of your owner's own cluster."
	factUse     = "Use it to ask another agent to do work, answer a message, or hear back from work you asked for."
	factWake    = "When a notice says a message is waiting, call `recv` first."
	factAck     = "After reading a page, `ack` it or pass its lease as `ack_lease` to the next `recv`; otherwise the same page comes straight back."
)

// Short is the instructions of the `sidera` MCP server. It is one line: Codex
// shows only the first line of a server's instructions when it defers the
// server's tools, cut to 250 characters of which the last three are "...", and
// Claude Code cuts instructions at 4096 characters. What a deferring client shows
// has to say what the channel is, when to use it and what to do on a wake-up, so
// those facts come first; the acknowledgement rule follows and may be cut there.
const Short = factChannel + " " + factUse + " " + factWake + " " + factAck

// Full is the statement every adapter gives the sessions it starts. It says what
// the channel is and what each tool does, as facts, and nothing about how to
// treat a message, which is the owner's policy. An adapter appends, after a blank
// line, the sentences that are true only of its client.
const Full = factChannel + " " + factUse + "\n\n" +
	factWake + " The notice carries neither the sender nor the text; both are in the page that `recv` returns.\n\n" +
	"The tools of the `sidera` MCP server do the work: `recv` reads the waiting messages, `reply` answers one, `send` writes to another address, `control` sends control signals, `status` shows what became of a message you sent, and `ack` acknowledges what you have read.\n\n" +
	factAck + " `recv` hands out a page that has not been acknowledged again at once, with the same lease, instead of waiting; `recv` with a wait waits for new messages only once the earlier page is acknowledged.\n\n" +
	"The `sidera` command in the shell does the same, one command for each tool: `sidera recv`, `sidera reply`, `sidera send`, `sidera control`, `sidera status`, `sidera ack <lease>` and `sidera recv --ack <lease>`. `sidera --help` lists the rest."

// SkillName is the name in the front matter of [Skill], and so the name of the
// directory a client expects the file in.
const SkillName = "sidera"

const skillDescription = "Message other agents and people in your owner's Sidera cluster, start work on another machine, and read what was sent to you. Use it when a notice says a Sidera message is waiting, or when a task needs another agent."

//go:embed procedure.md
var procedure string

// Skill returns the SKILL.md of Sidera: front matter, [Full], then the operating
// procedure. It is one file and has to stay one: a session started by an adapter
// is forbidden to read the data directory, so a file kept next to the Skill could
// not be read.
func Skill() []byte {
	var b strings.Builder
	b.WriteString("---\nname: " + SkillName + "\ndescription: " + skillDescription + "\n---\n\n# Sidera\n\n")
	b.WriteString(Full)
	b.WriteString("\n\n")
	b.WriteString(procedure)
	return []byte(b.String())
}
