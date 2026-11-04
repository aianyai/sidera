package adapterproto_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// TestDisplayNameRule holds the whole rule of a display name: its length in
// characters and the characters it refuses. A name with a control or a
// bidirectional formatting character is refused, never cleaned, because it would
// reach a terminal as a line break, an escape sequence or reordered text.
func TestDisplayNameRule(t *testing.T) {
	type row struct {
		name string
		in   string
		ok   bool
	}
	rows := []row{
		{"plain ASCII", "Owner console", true},
		{"non-Latin text", "\u00dcn\u00efcode \u65e5\u672c\u8a9e \u0645\u0631\u062d\u0628\u0627", true},
		{"spaces and punctuation", " (a/b) - c: d ", true},
		{"an emoji joined with a zero width joiner", "\U0001F468\u200d\U0001F469", true},
		{"the most characters", strings.Repeat("\u00fc", adapterproto.MaxDisplayName), true},
		{"one character over", strings.Repeat("\u00fc", adapterproto.MaxDisplayName+1), false},
		{"empty", "", false},
	}
	// Each range (U+2028 to U+202E joins the two separators and the embedding
	// controls) is refused at both ends and in the middle; the character on either
	// side of it is a name like any other.
	for _, r := range []struct{ first, last rune }{
		{0x0000, 0x001F}, {0x007F, 0x009F}, {0x061C, 0x061C}, {0x200E, 0x200F},
		{0x2028, 0x202E}, {0x2066, 0x2069},
	} {
		for _, c := range []rune{r.first, (r.first + r.last) / 2, r.last} {
			rows = append(rows, row{fmt.Sprintf("U+%04X is refused", c), "a" + string(c) + "b", false})
		}
		if r.first > 0 {
			rows = append(rows, row{fmt.Sprintf("U+%04X below the range is accepted", r.first-1), "a" + string(r.first-1) + "b", true})
		}
		rows = append(rows, row{fmt.Sprintf("U+%04X above the range is accepted", r.last+1), "a" + string(r.last+1) + "b", true})
	}
	rows = append(rows,
		row{"a name that starts with a line break", "\nname", false},
		row{"a name that ends with an escape", "name\x1b[2J", false},
		row{"a name with a right-to-left override", "name\u202etxt.exe", false},
	)
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			if err := adapterproto.ValidateDisplayName(c.in); (err == nil) != c.ok {
				t.Errorf("ValidateDisplayName(%q) = %v, accepted should be %v", c.in, err, c.ok)
			}
			// The optional form is the same rule, except that it lets an empty name be.
			if err := adapterproto.ValidateOptionalDisplayName(c.in); (err == nil) != (c.ok || c.in == "") {
				t.Errorf("ValidateOptionalDisplayName(%q) = %v", c.in, err)
			}
			// An adapter registers a name under the same rule.
			p := adapterproto.RegisterParams{Key: "k", DisplayName: c.in}
			if err := p.Validate(); (err == nil) != c.ok {
				t.Errorf("RegisterParams.Validate(%q) = %v, accepted should be %v", c.in, err, c.ok)
			}
		})
	}
}
