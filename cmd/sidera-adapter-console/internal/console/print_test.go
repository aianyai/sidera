package console

import (
	"bytes"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/termout"
)

// A message body comes from a peer, or from an agent a peer's prompt injection
// controls. Printed raw it would act in the owner's terminal, and shaped like a header
// it would pass for the start of another message. printMessage therefore prints it
// through termout.SanitizeText and indents every line of it; the classes of characters
// are tested there, this guards the wiring: an escape sequence is shown as text, the line
// breaks and tabs of a body stay, and nothing of a body starts in the first column.
func TestPrintMessageSanitizesAndIndentsTheBody(t *testing.T) {
	const header = "From     node-3/agent\nMessage  m-1\n\n"
	for _, tc := range []struct{ name, body, want string }{
		{"escape sequence", "a\x1b[2J\x1b]0;owned\ab", `  a\x1b[2J\x1b]0;owned\x07b` + "\n\n"},
		{"line breaks and tabs stay", "one\ntwo\r\n\tthree", "  one\n  two\r\n  \tthree\n\n"},
		{"a forged header stays in the body", "ok\nFrom     node-9/boss\nMessage  m-9", "  ok\n  From     node-9/boss\n  Message  m-9\n\n"},
		{"a body that ends in a line break", "done\n", "  done\n\n"},
	} {
		var out bytes.Buffer
		c := &client{out: termout.New(&out, false, false, 0)}
		c.printMessage(adapterproto.PageMessage{ID: "m-1", From: "node-3/agent", Body: tc.body})
		if want := header + tc.want; out.String() != want {
			t.Errorf("%s: printed %q, want %q", tc.name, out.String(), want)
		}
	}
}
