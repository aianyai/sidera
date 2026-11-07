package adapterkit

import (
	"strings"

	"github.com/aianyai/sidera/sdk/go/termout"
)

// Help is what an adapter program says about itself: the commands it has, in groups, and
// what each one does. The kit renders it for `--help` and for a usage error, and renders
// the commands it runs itself (`serve`, `package` and `version`) as one more group, so that
// no program writes them down again.
type Help struct {
	// Usage is the synopsis after "Usage: ", such as "sidera-adapter-codex <command>".
	Usage string
	// Groups are the commands of the program's own, by kind.
	Groups []HelpGroup
	// Footer is closing prose, wrapped to the width of the terminal.
	Footer string
}

// HelpGroup is a titled list of commands, or of flags.
type HelpGroup struct {
	Title   string
	Entries []HelpEntry
}

// HelpEntry is one command or flag and what it does.
type HelpEntry struct {
	// Name is the command, or the flag with its value (`--home <directory>`).
	Name string
	// Arguments are the command's arguments, written under its purpose; empty for none.
	Arguments string
	// Purpose says what the command does, in a sentence or two. The kit wraps it.
	Purpose string
}

// kitGroup is the group of the commands the kit runs, the same for every program.
var kitGroup = HelpGroup{Title: "Adapter program", Entries: []HelpEntry{
	{Name: "serve", Purpose: "The adapter role; the daemon starts it, a person does not."},
	{Name: "package", Arguments: "--goos <os> --goarch <arch> --binary <file> --out <directory>",
		Purpose: "Write an adapter package for `sidera adapter install`."},
	{Name: "version", Arguments: "[--json]", Purpose: "Print the version, the commit and the Go version of this program."},
}}

// Text is the help of the program as it reads on s: its own groups, then the commands of
// the kit. A group title is bold and a command name cyan on a colored stream; the
// purposes are wrapped at the width of s.
func (p Program) Text(s *termout.Stream) string {
	h := p.Help
	h.Groups = append(append([]HelpGroup(nil), h.Groups...), kitGroup)
	return h.render(s)
}

// render lays out h on s.
func (h Help) render(s *termout.Stream) string {
	var b strings.Builder
	b.WriteString("Usage: " + h.Usage + "\n")
	for _, g := range h.Groups {
		fields := make([]termout.Field, len(g.Entries))
		for i, e := range g.Entries {
			value := e.Purpose
			if e.Arguments != "" {
				value += "\n" + e.Arguments
			}
			fields[i] = termout.Field{Label: e.Name, Value: value}
		}
		b.WriteString("\n" + s.Bold(g.Title) + "\n")
		b.WriteString(termout.Indent(s.List(fields, s.Cyan, s.TextWidth()-2), 2))
	}
	if h.Footer != "" {
		b.WriteString("\n" + termout.Wrap(h.Footer, s.TextWidth()) + "\n")
	}
	return b.String()
}
