package termout

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Text that the program did not write itself (a remote peer's name or message, what
// an adapter or a client tool printed) can carry bytes a terminal acts on: escape
// sequences that clear the screen, rewrite lines, set the window title or write
// to the clipboard, and characters that reorder or hide text. Before such text
// reaches a stream it goes through one of the functions below, which show those
// characters as visible escapes instead. Text that a source has already checked
// against the class (display names, identifiers, enumerations) needs no second
// pass for it.
//
// The class is the person-text rule of adapterproto
// ([adapterproto.RefusedInPersonText], the one definition of it, called and not
// copied): the C0 controls, DEL, the controls U+0080 to U+009F (which some
// terminals read as the two-byte escape sequences they abbreviate), the line and
// paragraph separators, the characters that reorder text, and the invisible
// format characters, which a person cannot see but an agent reading the same text
// does, so hidden instructions can ride on them; they are escaped so that a person
// sees what an agent would read. This is a rule of display only; what the program
// accepts as input is decided where the input enters.
// U+200D (zero-width joiner) is left alone: it holds emoji sequences together.

// SanitizeLine makes text from outside safe to show in a field of a line. Every
// character of the class above, line breaks and tabs included, becomes a visible
// escape in Go's spelling: \n, \r and \t; \x1b for the ASCII controls and DEL;
// \u009b and \u202e for other characters up to U+FFFF; \U000e0041 above. A byte that is
// not valid UTF-8 is written \xff. Everything else, CJK and emoji among it, passes
// unchanged. A backslash is not doubled, so the escapes are for the eye and are
// not meant to be parsed back.
func SanitizeLine(s string) string { return sanitize(s, false) }

// SanitizeText is [SanitizeLine] for text that has lines, a message body: line breaks
// (\n and \r\n) and tabs stay as they are. A carriage return that is not part of
// \r\n is escaped, since on its own it moves the cursor to the start of the line and
// lets the text after it overwrite the text before.
func SanitizeText(s string) string { return sanitize(s, true) }

func sanitize(s string, multiline bool) string {
	// Most text is clean; return it without copying.
	clean := true
	for i := 0; i < len(s) && clean; {
		r, size := utf8.DecodeRuneInString(s[i:])
		clean = !(adapterproto.RefusedInPersonText(r) || r == utf8.RuneError && size == 1)
		i += size
	}
	if clean {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case multiline && (r == '\n' || r == '\t'):
			b.WriteRune(r)
		case multiline && r == '\r' && strings.HasPrefix(s[i:], "\r\n"):
			b.WriteString("\r\n")
			size = 2
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case !adapterproto.RefusedInPersonText(r):
			b.WriteString(s[i : i+size])
		case r < 0x80:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
		i += size
	}
	return b.String()
}
