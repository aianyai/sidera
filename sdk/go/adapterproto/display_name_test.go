package adapterproto_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// TestDisplayNameRule holds the whole rule of a display name and of a
// description: the characters they refuse, and the length of a display name in
// characters. A text with a control or a bidirectional formatting character is
// refused, never cleaned, because it would reach a terminal as an escape
// sequence, a line break or reordered text.
//
// The characters are the rule of every text shown to a person, so the table has
// one column for the display name and one for the description, which has its own
// length and differs in one way only: it may span lines and be indented, so line
// feed and tab are allowed in it.
func TestDisplayNameRule(t *testing.T) {
	type row struct {
		name string
		in   string
		// ok is whether a display name accepts the text, descOK whether a description does.
		ok, descOK bool
		// nameOnly marks a row about the length of a display name.
		nameOnly bool
	}
	rows := []row{
		{"plain ASCII", "Owner console", true, true, false},
		{"non-Latin text", "\u00dcn\u00efcode \u65e5\u672c\u8a9e \u0645\u0631\u062d\u0628\u0627", true, true, false},
		{"spaces and punctuation", " (a/b) - c: d ", true, true, false},
		{"an emoji joined with a zero width joiner", "\U0001F468\u200d\U0001F469", true, true, false},
		{"the most characters", strings.Repeat("\u00fc", adapterproto.MaxDisplayName), true, true, true},
		{"one character over", strings.Repeat("\u00fc", adapterproto.MaxDisplayName+1), false, true, true},
		{"empty", "", false, false, false},
	}
	// Each range (U+2028 to U+202E joins the two separators and the embedding
	// controls) is refused at both ends and in the middle; the character on either
	// side of it is a name like any other.
	for _, r := range []struct{ first, last rune }{
		{0x0000, 0x001F}, {0x007F, 0x009F}, {0x061C, 0x061C}, {0x200E, 0x200F},
		{0x2028, 0x202E}, {0x2066, 0x2069},
	} {
		for _, c := range []rune{r.first, (r.first + r.last) / 2, r.last} {
			rows = append(rows, row{fmt.Sprintf("U+%04X is refused", c), "a" + string(c) + "b", false, false, false})
		}
		if r.first > 0 {
			rows = append(rows, row{fmt.Sprintf("U+%04X below the range is accepted", r.first-1), "a" + string(r.first-1) + "b", true, true, false})
		}
		rows = append(rows, row{fmt.Sprintf("U+%04X above the range is accepted", r.last+1), "a" + string(r.last+1) + "b", true, true, false})
	}
	rows = append(rows,
		row{"a name that ends with an escape", "name\x1b[2J", false, false, false},
		row{"a name with a right-to-left override", "name\u202etxt.exe", false, false, false},
		row{"a name with an operating system command ended by BEL", "\x1b]0;title\x07name", false, false, false},
		row{"a name with a C1 control sequence introducer", "name\u009b2J", false, false, false},
		row{"a name with a next line", "two\u0085lines", false, false, false},
		row{"a name with an isolate", "name\u2066txt", false, false, false},
		row{"a name with a DEL", "name\x7f", false, false, false},
		// Line feed and tab are the two characters of the refused class that a
		// description keeps; the characters on either side of them, and the
		// carriage return that goes with a line feed on some systems, stay refused.
		row{"a line feed", "two\nlines", false, true, false},
		row{"a line feed first", "\nname", false, true, false},
		row{"a line feed last", "name\n", false, true, false},
		row{"a tab", "a\tb", false, true, false},
		row{"lines and an indent", "Reads:\n\tmessages\n\tfiles", false, true, false},
		row{"U+0008 next to the tab is refused", "a\bb", false, false, false},
		row{"U+000B next to the line feed is refused", "a\vb", false, false, false},
		row{"a carriage return", "a\rb", false, false, false},
		row{"a carriage return and line feed", "a\r\nb", false, false, false},
		row{"a line feed and an escape sequence", "a\n\x1b[2J", false, false, false},
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
			if c.nameOnly {
				return
			}
			// A description is only present when it is at least one character.
			if err := adapterproto.ValidateDescription(c.in); (err == nil) != c.descOK {
				t.Errorf("ValidateDescription(%q) = %v, accepted should be %v", c.in, err, c.descOK)
			}
			present := c.descOK || c.in == ""
			if err := adapterproto.ValidateOptionalDescription(c.in); (err == nil) != present {
				t.Errorf("ValidateOptionalDescription(%q) = %v", c.in, err)
			}
			p = adapterproto.RegisterParams{Key: "k", DisplayName: "n", Description: c.in}
			if err := p.Validate(); (err == nil) != present {
				t.Errorf("RegisterParams.Validate(description %q) = %v", c.in, err)
			}
			e := entry()
			e.Description = c.in
			if err := e.Validate(); (err == nil) != present {
				t.Errorf("DirectoryEntry.Validate(description %q) = %v", c.in, err)
			}
		})
	}
}

// TestDescriptionLength: a description is 1 to MaxDescription characters, counted
// in characters and not in bytes.
func TestDescriptionLength(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		ok   bool
	}{
		{"the most characters", strings.Repeat("\u00fc", adapterproto.MaxDescription), true},
		{"one character over", strings.Repeat("\u00fc", adapterproto.MaxDescription+1), false},
		{"empty", "", false},
	} {
		if err := adapterproto.ValidateDescription(c.in); (err == nil) != c.ok {
			t.Errorf("%s: ValidateDescription = %v, accepted should be %v", c.name, err, c.ok)
		}
	}
}

// TestPatternsFollowThePredicates: the schema states the rule as a regular
// expression and the validators apply it as a predicate on one code point, two
// derivations of one table. Every code point must be judged the same by both, so
// a table edit that one of them gets wrong is caught for any range, whatever its
// plane.
func TestPatternsFollowThePredicates(t *testing.T) {
	for _, c := range []struct {
		definition string
		refused    func(rune) bool
	}{
		{"DisplayName", adapterproto.RefusedInPersonText},
		{"Description", adapterproto.RefusedInDescription},
	} {
		re := regexp.MustCompile(adapterproto.Patterns()[c.definition])
		for r := rune(0); r <= unicode.MaxRune; r++ {
			if r >= 0xD800 && r <= 0xDFFF {
				continue // a surrogate is not a character, and a Go string cannot hold one
			}
			if got := !re.MatchString(string(r)); got != c.refused(r) {
				t.Fatalf("%s: U+%04X is refused by the pattern %v, by the predicate %v", c.definition, r, got, c.refused(r))
			}
		}
	}
}
