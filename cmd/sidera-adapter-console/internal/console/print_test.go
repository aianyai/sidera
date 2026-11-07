package console

import (
	"bytes"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A message body comes from a peer, or from an agent a peer's prompt injection
// controls. Printed raw it would act in the owner's terminal, so printMessage prints it
// through termout.SanitizeText. The classes of characters are tested there; this guards
// the wiring: an escape sequence is shown as text, and the line breaks and tabs of a
// body stay.
func TestPrintMessageSanitizesTheBody(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"escape sequence", "a\x1b[2J\x1b]0;owned\ab", `a\x1b[2J\x1b]0;owned\x07b`},
		{"line breaks and tabs stay", "one\ntwo\r\n\tthree", "one\ntwo\r\n\tthree"},
	} {
		var out bytes.Buffer
		c := &client{env: Env{Stdout: &out}}
		c.printMessage(adapterproto.PageMessage{ID: "m-1", From: "node-3/agent", Body: tc.body})
		if want := "from node-3/agent  id m-1\n" + tc.want + "\n\n"; out.String() != want {
			t.Errorf("%s: printed %q, want %q", tc.name, out.String(), want)
		}
	}
}
