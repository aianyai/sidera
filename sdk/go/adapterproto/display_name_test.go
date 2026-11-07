package adapterproto_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// TestDisplayNameRule holds the whole rule of a display name, of a
// description and of the name and version of a probed runtime: the characters they refuse, and the length of a display name in
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
	// controls, U+2060 to U+206F the word joiner, the invisible operators, the
	// isolates and the deprecated format characters, and the unassigned U+2065
	// between them) is refused at both ends and in the middle; the character on
	// either side of it is a name like any other. Beside the invisible
	// characters, U+200C and U+200D (which spell Persian and Indic words and join
	// emoji), U+00AD and U+180E stay accepted.
	for _, r := range []struct{ first, last rune }{
		{0x0000, 0x001F}, {0x007F, 0x009F}, {0x061C, 0x061C}, {0x200B, 0x200B}, {0x200E, 0x200F},
		{0x2028, 0x202E}, {0x2060, 0x206F}, {0xFEFF, 0xFEFF}, {0xE0000, 0xE00FF},
		// The characters that fill a place with nothing: the combining grapheme
		// joiner, the Hangul and Khmer fillers, the shorthand and musical format
		// controls, and the reserved default-ignorable ranges.
		{0x034F, 0x034F}, {0x115F, 0x1160}, {0x17B4, 0x17B5}, {0x3164, 0x3164}, {0xFFA0, 0xFFA0},
		{0xFFF0, 0xFFF8}, {0x1BCA0, 0x1BCA3}, {0x1D173, 0x1D17A}, {0xE01F0, 0xE0FFF},
	} {
		for _, c := range []rune{r.first, (r.first + r.last) / 2, r.last} {
			rows = append(rows, row{fmt.Sprintf("U+%04X is refused", c), "a" + string(c) + "b", false, false, false})
		}
		if r.first > 0 {
			rows = append(rows, row{fmt.Sprintf("U+%04X below the range is accepted", r.first-1), "a" + string(r.first-1) + "b", true, true, false})
		}
		rows = append(rows, row{fmt.Sprintf("U+%04X above the range is accepted", r.last+1), "a" + string(r.last+1) + "b", true, true, false})
	}
	for _, c := range personTextSequences {
		rows = append(rows, row{c.name, c.in, c.nameOK, c.descOK, false})
	}
	rows = append(rows,
		row{"a name that ends with an escape", "name\x1b[2J", false, false, false},
		row{"a name with a right-to-left override", "name\u202etxt.exe", false, false, false},
		row{"a name with an operating system command ended by BEL", "\x1b]0;title\x07name", false, false, false},
		row{"a name with a C1 control sequence introducer", "name\u009b2J", false, false, false},
		row{"a name with a next line", "two\u0085lines", false, false, false},
		row{"a name with an isolate", "name\u2066txt", false, false, false},
		// Unassigned, and listed by Unicode as default-ignorable like its neighbours.
		row{"a name with the unassigned code point between the operators and the isolates", "name\u2065txt", false, false, false},
		row{"a name with a DEL", "name\x7f", false, false, false},
		row{"a name with a zero width space", "name\u200bname", false, false, false},
		row{"a name with a byte order mark", "\ufeffname", false, false, false},
		row{"a name with a tag character", "name\U000e0041", false, false, false},
		row{"a description with a zero width space", "a\nname\u200b", false, false, false},
		row{"a name with a zero width non-joiner", "\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645", true, true, false},
		row{"a name with a soft hyphen", "co\u00adoperate", true, true, false},
		row{"a name with a Mongolian vowel separator", "\u1820\u180e\u1821", true, true, false},
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
			// The name and the version a probe reports reach other nodes' terminals
			// through the directory, so each is held to the characters of a display
			// name. Their length is 1 to 64, which bounds_test covers.
			if err := (adapterproto.RuntimeInfo{Name: c.in, Version: "1"}).Validate(); (err == nil) != c.ok {
				t.Errorf("RuntimeInfo.Validate(name %q) = %v, accepted should be %v", c.in, err, c.ok)
			}
			if err := (adapterproto.RuntimeInfo{Name: "n", Version: c.in}).Validate(); (err == nil) != c.ok {
				t.Errorf("RuntimeInfo.Validate(version %q) = %v, accepted should be %v", c.in, err, c.ok)
			}
			// The text a node says about itself reaches the terminals of the nodes that
			// trust it the same way, field by field.
			for field, set := range map[string]func(*adapterproto.PeerProfile){
				"name":           func(p *adapterproto.PeerProfile) { p.Name = c.in },
				"host_name":      func(p *adapterproto.PeerProfile) { p.HostName = c.in },
				"platform":       func(p *adapterproto.PeerProfile) { p.Platform = c.in },
				"system":         func(p *adapterproto.PeerProfile) { p.System = c.in },
				"system_build":   func(p *adapterproto.PeerProfile) { p.SystemBuild = c.in },
				"sidera_version": func(p *adapterproto.PeerProfile) { p.SideraVersion = c.in },
			} {
				profile := adapterproto.PeerProfile{AsOf: 1}
				set(&profile)
				// An empty field is an absent one, which the optional form accepts.
				if err := profile.Validate(); (err == nil) != (c.ok || c.in == "") {
					t.Errorf("PeerProfile.Validate(%s %q) = %v", field, c.in, err)
				}
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

// personTextSequences are the rules of a text taken as a whole, beyond the
// characters one by one: a variation selector needs a base character before it
// and is never repeated, and a name draws at least one visible character. nameOK
// is the verdict for a display name and for every other name-like field, descOK
// for a description, which may be blank.
var personTextSequences = []struct {
	name, in       string
	nameOK, descOK bool
}{
	{"a normal name", "Owner console", true, true},
	{"an emoji with its variation selector", "\u2764\ufe0f", true, true},
	{"an emoji sequence joined by a zero width joiner", "\u2764\ufe0f\u200d\U0001F525", true, true},
	{"a keycap", "1\ufe0f\u20e3", true, true},
	{"an ideographic variation sequence", "\u8fbb\U000e0100", true, true},
	{"a Mongolian letter with a free variation selector", "\u1820\u180b", true, true},
	{"every kind of selector once, each after its own base", "a\ufe0fb\U000e0100c\u180bd\u180ce\u180df\u180f", true, true},
	{"a selector after a zero width joiner", "a\u200d\ufe0f", true, true},
	{"a selector first", "\ufe0fa", false, false},
	{"a supplementary selector first", "\U000e0100a", false, false},
	{"a Mongolian selector first", "\u180ba", false, false},
	{"the Mongolian selector U+180F first", "\u180fa", false, false},
	{"two selectors in a row", "a\ufe0f\ufe0f", false, false},
	{"two selectors in a row in the middle", "a\ufe0f\ufe0fb", false, false},
	{"two selectors of different blocks in a row", "a\ufe0f\U000e0100", false, false},
	{"a Mongolian selector after a selector", "a\ufe0f\u180b", false, false},
	{"a run of selectors after an emoji", "\u2764" + strings.Repeat("\ufe0f", 8), false, false},
	{"a selector alone", "\ufe0f", false, false},
	{"a selector after a base after a run is still a run", "a\ufe0f\ufe0fb\ufe0f", false, false},
	{"selectors apart", "a\ufe0fb\ufe0f", true, true},
	{"a Hangul filler alone", "\u3164", false, false},
	{"a Hangul filler in a name", "a\u3164b", false, false},
	{"a halfwidth Hangul filler", "ab\uffa0", false, false},
	{"only spaces", "   ", false, true},
	{"only a no-break space", "\u00a0", false, true},
	{"only an ideographic space", "\u3000", false, true},
	{"only an en space", "\u2002", false, true},
	{"only a braille blank", "\u2800", false, true},
	{"only a zero width joiner", "\u200d", false, true},
	{"only zero width non-joiners", "\u200c\u200c", false, true},
	{"only a soft hyphen", "\u00ad", false, true},
	{"only a Mongolian vowel separator", "\u180e", false, true},
	{"only a joiner and a selector", "\u200d\ufe0f", false, true},
	{"only blanks and invisible characters mixed", " \u200d\u00ad\u2800\u3000\u180e", false, true},
	{"a visible character among blanks", " \u2800 a\u200d ", true, true},
	{"a visible character after a joiner", "\u200da", true, true},
	{"a single character", "x", true, true},
}

// TestPatternsFollowThePredicates: the schema states the rules as a regular
// expression and the validators apply them in Go, two derivations of one table.
// They must judge alike: every code point, in the middle of a name, whatever its
// plane (so a table edit that one gets wrong is caught for any range), and every
// text of the sequence table, which holds the rules that only a whole text
// shows.
func TestPatternsFollowThePredicates(t *testing.T) {
	for _, c := range []struct {
		definition string
		refused    func(rune) bool
		validate   func(string) error
		sequenceOK func(nameOK, descOK bool) bool
	}{
		{"DisplayName", adapterproto.RefusedInPersonText, adapterproto.ValidateDisplayName, func(nameOK, _ bool) bool { return nameOK }},
		{"Description", adapterproto.RefusedInDescription, adapterproto.ValidateDescription, func(_, descOK bool) bool { return descOK }},
	} {
		re := regexp.MustCompile(adapterproto.Patterns()[c.definition])
		for r := rune(0); r <= unicode.MaxRune; r++ {
			if r >= 0xD800 && r <= 0xDFFF {
				continue // a surrogate is not a character, and a Go string cannot hold one
			}
			// Between two letters, so that a variation selector has its base and a
			// blank has a visible neighbour: only the class itself decides.
			in := "a" + string(r) + "b"
			// A character alone is judged by the rules of a whole text, which the
			// predicate does not know (a blank has nothing to see, a variation
			// selector has no base), so pattern and validator are compared with each
			// other: this guards the table of blanks, the rule for a selector at the
			// start and the merging of the classes into the pattern.
			alone := string(r)
			if got, want := re.MatchString(alone), c.validate(alone) == nil; got != want {
				t.Fatalf("%s: U+%04X alone is accepted by the pattern %v, by the validator %v", c.definition, r, got, want)
			}
			if got := !re.MatchString(in); got != c.refused(r) {
				t.Fatalf("%s: U+%04X is refused by the pattern %v, by the predicate %v", c.definition, r, got, c.refused(r))
			}
			if got := c.validate(in) != nil; got != c.refused(r) {
				t.Fatalf("%s: U+%04X is refused by the validator %v, by the predicate %v", c.definition, r, got, c.refused(r))
			}
		}
		for _, s := range personTextSequences {
			want := c.sequenceOK(s.nameOK, s.descOK)
			if got := re.MatchString(s.in); got != want {
				t.Errorf("%s: the pattern accepts %q = %v, want %v (%s)", c.definition, s.in, got, want, s.name)
			}
		}
	}
}
