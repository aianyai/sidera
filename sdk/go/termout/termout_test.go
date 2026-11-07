package termout_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/termout"
)

func env(pairs ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return func(k string) string { return m[k] }
}

func TestDecide(t *testing.T) {
	for _, tc := range []struct {
		name      string
		terminal  bool
		mode      termout.ColorMode
		env       func(string) string
		decorated bool
		colored   bool
	}{
		{"terminal, nothing set", true, termout.ColorAuto, env(), true, true},
		{"pipe, nothing set", false, termout.ColorAuto, env(), false, false},
		{"nil lookup reads nothing", true, termout.ColorAuto, nil, true, true},

		{"never on a terminal", true, termout.ColorNever, env(), true, false},
		{"never beats FORCE_COLOR", false, termout.ColorNever, env("FORCE_COLOR", "1"), false, false},
		{"always on a pipe", false, termout.ColorAlways, env(), true, true},
		{"always beats NO_COLOR", false, termout.ColorAlways, env("NO_COLOR", "1"), true, true},
		{"always beats TERM=dumb", true, termout.ColorAlways, env("TERM", "dumb"), true, true},

		{"NO_COLOR on a terminal removes color only", true, termout.ColorAuto, env("NO_COLOR", "1"), true, false},
		{"NO_COLOR=0 still counts", true, termout.ColorAuto, env("NO_COLOR", "0"), true, false},
		{"empty NO_COLOR is unset", true, termout.ColorAuto, env("NO_COLOR", ""), true, true},
		{"NO_COLOR beats CLICOLOR_FORCE", false, termout.ColorAuto, env("NO_COLOR", "1", "CLICOLOR_FORCE", "1"), false, false},
		{"NO_COLOR beats FORCE_COLOR", false, termout.ColorAuto, env("NO_COLOR", "1", "FORCE_COLOR", "1"), false, false},

		{"CLICOLOR_FORCE colors and decorates a pipe", false, termout.ColorAuto, env("CLICOLOR_FORCE", "1"), true, true},
		{"CLICOLOR_FORCE=0 does not force", false, termout.ColorAuto, env("CLICOLOR_FORCE", "0"), false, false},
		{"CLICOLOR_FORCE=false does not force", false, termout.ColorAuto, env("CLICOLOR_FORCE", "false"), false, false},
		{"empty CLICOLOR_FORCE does not force", false, termout.ColorAuto, env("CLICOLOR_FORCE", ""), false, false},
		{"FORCE_COLOR colors and decorates a pipe", false, termout.ColorAuto, env("FORCE_COLOR", "1"), true, true},
		{"FORCE_COLOR=true forces", false, termout.ColorAuto, env("FORCE_COLOR", "true"), true, true},
		{"FORCE_COLOR=0 does not force", false, termout.ColorAuto, env("FORCE_COLOR", "0"), false, false},
		{"FORCE_COLOR=false does not force", false, termout.ColorAuto, env("FORCE_COLOR", "false"), false, false},
		{"FORCE_COLOR=0 leaves a terminal to the rules after it", true, termout.ColorAuto, env("FORCE_COLOR", "0", "TERM", "dumb"), true, false},
		{"FORCE_COLOR beats TERM=dumb", true, termout.ColorAuto, env("FORCE_COLOR", "1", "TERM", "dumb"), true, true},
		{"CLICOLOR_FORCE beats CLICOLOR=0", false, termout.ColorAuto, env("CLICOLOR_FORCE", "1", "CLICOLOR", "0"), true, true},

		{"CLICOLOR=0 on a terminal", true, termout.ColorAuto, env("CLICOLOR", "0"), true, false},
		{"CLICOLOR=1 changes nothing", true, termout.ColorAuto, env("CLICOLOR", "1"), true, true},
		{"TERM=dumb on a terminal", true, termout.ColorAuto, env("TERM", "dumb"), true, false},
		{"TERM=xterm changes nothing", true, termout.ColorAuto, env("TERM", "xterm-256color"), true, true},

		{"agent hints take no part", false, termout.ColorAuto, env("AI_AGENT", "x", "CLAUDECODE", "1"), false, false},
		{"agent hints take no part on a terminal", true, termout.ColorAuto, env("AI_AGENT", "x", "CLAUDECODE", "1"), true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decorated, colored := termout.Decide(tc.terminal, tc.mode, tc.env)
			if decorated != tc.decorated || colored != tc.colored {
				t.Fatalf("Decide = decorated %v, colored %v; want %v, %v", decorated, colored, tc.decorated, tc.colored)
			}
		})
	}
}

// Whatever the inputs, a stream that is colored is decorated: rendering has three
// states (plain, decorated, colored), never "colored without markers".
func TestColoredImpliesDecorated(t *testing.T) {
	values := []string{"", "0", "1", "false", "true", "dumb"}
	for _, terminal := range []bool{false, true} {
		for _, mode := range []termout.ColorMode{termout.ColorAuto, termout.ColorAlways, termout.ColorNever} {
			for _, noColor := range values[:3] {
				for _, force := range values[:5] {
					for _, cliForce := range values[:5] {
						for _, cli := range values[:3] {
							for _, term := range []string{"", "dumb", "xterm"} {
								getenv := env("NO_COLOR", noColor, "FORCE_COLOR", force, "CLICOLOR_FORCE", cliForce, "CLICOLOR", cli, "TERM", term)
								decorated, colored := termout.Decide(terminal, mode, getenv)
								if colored && !decorated {
									t.Fatalf("terminal %v, mode %v, NO_COLOR=%q FORCE_COLOR=%q CLICOLOR_FORCE=%q CLICOLOR=%q TERM=%q: colored and not decorated",
										terminal, mode, noColor, force, cliForce, cli, term)
								}
							}
						}
					}
				}
			}
		}
	}
}

func TestParseColorMode(t *testing.T) {
	for in, want := range map[string]termout.ColorMode{"auto": termout.ColorAuto, "always": termout.ColorAlways, "never": termout.ColorNever} {
		got, err := termout.ParseColorMode(in)
		if err != nil || got != want || got.String() != in {
			t.Errorf("ParseColorMode(%q) = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "Auto", "yes", "true", "always "} {
		if _, err := termout.ParseColorMode(in); err == nil {
			t.Errorf("ParseColorMode(%q) accepted", in)
		}
	}
}

func TestDisplayWidth(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want int
	}{
		{"ASCII", "abc", 3},
		{"empty", "", 0},
		{"CJK counts two", "\u8282\u70b9", 4},
		{"fullwidth Latin counts two", "\uff21\uff22", 4},
		{"Hangul and kana count two", "한あ", 4},
		{"emoji counts two", "😀", 2},
		{"combining mark counts zero", "e\u0301", 1},
		{"zero-width joiner counts zero", "a\u200db", 2},
		{"ambiguous width counts one", "é±", 2},
		{"emoji presentation selector widens a narrow base", "\u2764\ufe0f", 2},
		{"emoji presentation selector after a wide base adds nothing", "😀\ufe0f", 2},
		{"text presentation selector adds nothing", "\u2764\ufe0e", 1},
		{"skin tone modifier counts zero", "👍\U0001f3fd", 2},
		{"every skin tone modifier counts zero", "\U0001f3fb\U0001f3fc\U0001f3fd\U0001f3fe\U0001f3ff", 0},
		{"Hangul syllable", "\ud55c", 2},
		{"decomposed Hangul syllable takes the room of the composed one", "\u1112\u1161\u11ab", 2},
		{"decomposed Hangul without a final", "\u1100\u1161", 2},
		{"Hangul jamo extended-B counts zero", "\u1100\ud7b0\ud7c6\ud7ff", 2},
		{"color sequence counts zero", "\x1b[1mab\x1b[0m", 2},
		{"color sequence with parameters", "\x1b[38;5;196mab\x1b[0m", 2},
		{"unterminated sequence", "ab\x1b[3", 2},
		{"mixed", "\x1b[32m\u8282\u70b9\x1b[0m-1", 6},
	} {
		if got := termout.DisplayWidth(tc.in); got != tc.want {
			t.Errorf("%s: DisplayWidth(%q) = %d, want %d", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestTableAlignsByDisplayWidth(t *testing.T) {
	header := []string{"Name", "State", "Address"}
	rows := [][]string{
		{"console", "enabled", "node1/ep1"},
		{"\u8282\u70b9\u4e00", "", "node1/ep2"},
		{"e\u0301cole", "paused", "node1/ep3"},
	}
	plain := termout.New(nil, false, false, 80)
	want := "NAME     STATE    ADDRESS\n" +
		"console  enabled  node1/ep1\n" +
		"\u8282\u70b9\u4e00   -        node1/ep2\n" +
		"e\u0301cole    paused   node1/ep3\n"
	if got := plain.Table(header, rows); got != want {
		t.Errorf("plain table:\n%q\nwant\n%q", got, want)
	}

	// Colored cells and a colored header take the same room as plain ones.
	colored := termout.New(nil, true, true, 80)
	rows[0][1] = colored.Green("enabled")
	got := colored.Table(header, rows)
	stripped := stripANSI(got)
	if stripped != want {
		t.Errorf("colored table, escapes removed:\n%q\nwant\n%q", stripped, want)
	}
	if !strings.HasPrefix(got, "\x1b[1mNAME\x1b[0m") {
		t.Errorf("header is not bold: %q", got)
	}
}

func TestTableOneColumnAndNoTrailingSpaces(t *testing.T) {
	got := termout.New(nil, false, false, 80).Table([]string{"id", "note"}, [][]string{{"a", ""}, {"bb", "x"}})
	want := "ID  NOTE\na   -\nbb  x\n"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// The last cell may continue on further lines, which start where the last column
// starts; this is how a row carries a note under its state.
func TestTableLastCellMayHaveLines(t *testing.T) {
	header := []string{"instance", "adapter", "probe"}
	rows := [][]string{
		{"console", "console", "passed"},
		{"work", "claude", "passed\n! claude is not logged in"},
	}
	want := "INSTANCE  ADAPTER  PROBE\n" +
		"console   console  passed\n" +
		"work      claude   passed\n" +
		"                   ! claude is not logged in\n"
	if got := termout.New(nil, false, false, 80).Table(header, rows); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
	if got := termout.New(nil, false, false, 80).Table([]string{"a"}, [][]string{{"x\ny"}}); got != "A\nx\ny\n" {
		t.Errorf("one column: %q", got)
	}
}

func TestTablePanicsOnAMistakeInTheProgram(t *testing.T) {
	for name, tc := range map[string]struct {
		header []string
		rows   [][]string
	}{
		"a row of the wrong length":     {[]string{"a", "b"}, [][]string{{"x"}}},
		"a line break in an inner cell": {[]string{"a", "b"}, [][]string{{"x\ny", "z"}}},
		"a line break in the header":    {[]string{"a", "b\nc"}, [][]string{{"x", "z"}}},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			termout.New(nil, false, false, 80).Table(tc.header, tc.rows)
		}()
	}
}

func TestIndent(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		columns  int
		want     string
	}{
		{"nothing", "", 2, ""},
		{"one line", "a", 2, "  a"},
		{"final line break stays and gets no spaces", "a\nb\n", 2, "  a\n  b\n"},
		{"blank lines get no spaces", "a\n\nb", 2, "  a\n\n  b"},
		{"only a line break", "\n", 2, "\n"},
		{"no columns", "a", 0, "a"},
		{"negative columns", "a", -1, "a"},
	} {
		if got := termout.Indent(tc.in, tc.columns); got != tc.want {
			t.Errorf("%s: Indent(%q, %d) = %q, want %q", tc.name, tc.in, tc.columns, got, tc.want)
		}
	}
}

func TestListWrapsDescriptionsUnderTheirColumn(t *testing.T) {
	fields := []termout.Field{
		{"send", "Send one message to one address and wait for nothing at all more"},
		{"recv", ""},
		{"a-longer-name", "x\n\n\u8282\u70b9"},
	}
	want := "send" + spaces(11) + "Send one message to one\n" +
		spaces(15) + "address and wait for\n" +
		spaces(15) + "nothing at all more\n" +
		"recv\n" +
		"a-longer-name  x\n" +
		"\n" +
		spaces(15) + "\u8282\u70b9\n"
	if got := termout.New(nil, false, false, 80).List(fields, nil, 40); got != want {
		t.Errorf("plain list:\n%q\nwant\n%q", got, want)
	}
	// Without a width nothing wraps.
	if got := termout.New(nil, false, false, 80).List(fields[:1], nil, 0); got != "send  "+fields[0].Value+"\n" {
		t.Errorf("unwrapped list: %q", got)
	}
	// The caller styles the labels; styles take no room.
	c := termout.New(nil, true, true, 80)
	got := c.List(fields, c.Cyan, 40)
	if stripANSI(got) != want || !strings.HasPrefix(got, "\x1b[36msend\x1b[0m"+spaces(11)) {
		t.Errorf("colored list: %q", got)
	}
}

func spaces(n int) string { return strings.Repeat(" ", n) }

func TestCommandAndMarkerWidth(t *testing.T) {
	if got := termout.New(nil, true, false, 80).Command("sidera adapter list"); got != "`sidera adapter list`" {
		t.Errorf("Command = %q", got)
	}
	if got := termout.New(nil, true, true, 80).Command("sidera me"); got != "\x1b[36m`sidera me`\x1b[0m" {
		t.Errorf("colored Command = %q", got)
	}
	// The width is what a marker and its space take, and what the lines under a
	// marker are indented by, on every stream.
	for _, decorated := range []bool{false, true} {
		s := termout.New(nil, decorated, false, 80)
		if got := s.OK("a\nb"); !strings.HasSuffix(got, "\n"+spaces(termout.MarkerWidth)+"b") {
			t.Errorf("decorated %v: continuation %q", decorated, got)
		}
	}
	for _, mark := range []func(string) string{
		termout.New(nil, true, false, 80).OK, termout.New(nil, true, false, 80).Warn, termout.New(nil, true, false, 80).Fail,
	} {
		if w := termout.DisplayWidth(strings.TrimSuffix(mark("x"), "x")); w != termout.MarkerWidth {
			t.Errorf("a marker and its space take %d columns, MarkerWidth is %d", w, termout.MarkerWidth)
		}
	}
}

func TestBlockAlignsLabelsContinuesValuesAndLeavesOutEmptyOnes(t *testing.T) {
	fields := []termout.Field{
		{"Node", "node1"},
		{"\u8282\u70b9", "wide label"},
		{"Frontends", "cli\nmcp"},
		{"Alerts", ""}, // a field with nothing to say is left out; a caller who wants "none" passes it
		{"Notes", "one\n\ntwo"},
	}
	want := "Node       node1\n" +
		"\u8282\u70b9       wide label\n" +
		"Frontends  cli\n" +
		"           mcp\n" +
		"Notes      one\n" +
		"\n" +
		"           two\n"
	if got := termout.New(nil, false, false, 80).Block(fields); got != want {
		t.Errorf("plain block:\n%q\nwant\n%q", got, want)
	}
	c := termout.New(nil, true, true, 80)
	if got := stripANSI(c.Block(fields)); got != want {
		t.Errorf("colored block, escapes removed:\n%q\nwant\n%q", got, want)
	}
	if got := c.Block(fields[:1]); got != "\x1b[1mNode\x1b[0m  node1\n" {
		t.Errorf("colored label: %q", got)
	}
}

func TestStylesAndMarkers(t *testing.T) {
	for _, tc := range []struct {
		name               string
		decorated, colored bool
		ok, warn, fail     string
		bold               string
	}{
		{"plain", false, false, "done", "careful", "broken", "x"},
		{"decorated", true, false, "✓ done", "! careful", "✗ broken", "x"},
		{"colored", true, true, "\x1b[32m✓\x1b[0m done", "\x1b[33m!\x1b[0m careful", "\x1b[31m✗\x1b[0m broken", "\x1b[1mx\x1b[0m"},
	} {
		s := termout.New(nil, tc.decorated, tc.colored, 80)
		if got := s.OK("done"); got != tc.ok {
			t.Errorf("%s OK = %q, want %q", tc.name, got, tc.ok)
		}
		if got := s.Warn("careful"); got != tc.warn {
			t.Errorf("%s Warn = %q, want %q", tc.name, got, tc.warn)
		}
		if got := s.Fail("broken"); got != tc.fail {
			t.Errorf("%s Fail = %q, want %q", tc.name, got, tc.fail)
		}
		if got := s.Bold("x"); got != tc.bold {
			t.Errorf("%s Bold = %q, want %q", tc.name, got, tc.bold)
		}
		if got := s.Dim(""); got != "" {
			t.Errorf("%s Dim of nothing = %q", tc.name, got)
		}
		// The later lines of an item keep the marker's indent whether or not it is shown.
		if got := s.Warn("a\nb"); !strings.HasSuffix(got, "\n  b") {
			t.Errorf("%s Warn continuation = %q", tc.name, got)
		}
	}
	s := termout.New(nil, true, true, 80)
	for name, got := range map[string]string{"dim": s.Dim("x"), "green": s.Green("x"), "yellow": s.Yellow("x"), "red": s.Red("x"), "cyan": s.Cyan("x")} {
		want := map[string]string{"dim": "\x1b[2mx\x1b[0m", "green": "\x1b[32mx\x1b[0m", "yellow": "\x1b[33mx\x1b[0m", "red": "\x1b[31mx\x1b[0m", "cyan": "\x1b[36mx\x1b[0m"}[name]
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// A marker line composed with a layout ends where the layout ends: no trailing
// spaces, on a blank line or at the end.
func TestMarkerLinesHaveNoTrailingWhitespace(t *testing.T) {
	for _, decorated := range []bool{false, true} {
		s := termout.New(nil, decorated, false, 80)
		block := s.Block([]termout.Field{{"Node", "n1"}, {"Notes", "a\n\nb"}})
		for _, got := range []string{s.OK("Updated.\n" + block), s.Warn("a\n\nb\n"), s.Fail("x\n")} {
			for _, line := range strings.Split(got, "\n") {
				if line != strings.TrimRight(line, " ") {
					t.Errorf("decorated %v: trailing space in %q of %q", decorated, line, got)
				}
			}
			if !strings.HasSuffix(got, "\n") && strings.Contains(got, "\n") {
				t.Errorf("decorated %v: lost the final line break of %q", decorated, got)
			}
		}
	}
}

func TestWidths(t *testing.T) {
	for _, tc := range []struct{ width, text int }{{0, 80}, {-3, 80}, {60, 60}, {100, 100}, {200, 100}} {
		s := termout.New(nil, true, true, tc.width)
		if s.TextWidth() != tc.text {
			t.Errorf("width %d: TextWidth = %d, want %d", tc.width, s.TextWidth(), tc.text)
		}
	}
}

func TestWrap(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		width    int
		want     string
	}{
		{"fits", "one two three", 20, "one two three"},
		{"fits keeps spaces", "a   b", 20, "a   b"},
		{"breaks at spaces", "one two three four", 9, "one two\nthree\nfour"},
		{"exact width", "abcd efgh", 9, "abcd efgh"},
		{"long word stays whole", "a verylongword b", 5, "a\nverylongword\nb"},
		{"indent repeats", "  one two three four", 10, "  one two\n  three\n  four"},
		{"line breaks stay", "one two three\n\nfour five six", 9, "one two\nthree\n\nfour five\nsix"},
		{"wide characters count two", "\u8282\u70b9 \u8282\u70b9 \u8282\u70b9", 9, "\u8282\u70b9 \u8282\u70b9\n\u8282\u70b9"},
		{"color does not count", "\x1b[1mone\x1b[0m two", 7, "\x1b[1mone\x1b[0m two"},
		{"no width", "one two three", 0, "one two three"},
		{"tab indent is kept", "\tone two three four", 9, "\tone two\n\tthree\n\tfour"},
		{"mixed indent is kept", " \t one two three", 9, " \t one\n \t two\n \t three"},
		{"non-breaking space does not break", "a b\u00a0c d", 3, "a\nb\u00a0c\nd"},
		{"ideographic space does not break", "a b\u3000c d", 3, "a\nb\u3000c\nd"},
		{"tab breaks", "ab\tcd", 3, "ab\ncd"},
	} {
		if got := termout.Wrap(tc.in, tc.width); got != tc.want {
			t.Errorf("%s: Wrap(%q, %d) = %q, want %q", tc.name, tc.in, tc.width, got, tc.want)
		}
	}
}

// The classes of characters that a terminal acts on or that reorder text, one row
// each; the same row for both forms unless the line break or tab is the point.
func TestSanitize(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		line     string // SanitizeLine
		text     string // SanitizeText
	}{
		{"ESC sequence", "a\x1b[2Jb", `a\x1b[2Jb`, `a\x1b[2Jb`},
		{"OSC 52 clipboard write", "\x1b]52;c;ZXZpbA==\a", `\x1b]52;c;ZXZpbA==\x07`, `\x1b]52;c;ZXZpbA==\x07`},
		{"OSC title with ST", "\x1b]0;t\x1b\\", `\x1b]0;t\x1b\`, `\x1b]0;t\x1b\`},
		{"NUL", "a\x00b", `a\x00b`, `a\x00b`},
		{"BEL", "a\ab", `a\x07b`, `a\x07b`},
		{"backspace", "a\bb", `a\x08b`, `a\x08b`},
		{"form feed and vertical tab", "\f\v", `\x0c\x0b`, `\x0c\x0b`},
		{"DEL", "a\x7fb", `a\x7fb`, `a\x7fb`},
		{"8-bit CSI introducer", "a\u009bb", `a\u009bb`, `a\u009bb`},
		{"first and last control of U+0080 to U+009F", "\u0080\u009f", `\u0080\u009f`, `\u0080\u009f`},
		{"Arabic letter mark", "a\u061cb", `a\u061cb`, `a\u061cb`},
		{"LRM and RLM", "\u200e\u200f", `\u200e\u200f`, `\u200e\u200f`},
		{"line and paragraph separator", "a\u2028b\u2029c", `a\u2028b\u2029c`, `a\u2028b\u2029c`},
		{"embeddings and override", "\u202a\u202b\u202c\u202d\u202e", `\u202a\u202b\u202c\u202d\u202e`, `\u202a\u202b\u202c\u202d\u202e`},
		{"isolates", "\u2066\u2067\u2068\u2069", `\u2066\u2067\u2068\u2069`, `\u2066\u2067\u2068\u2069`},
		{"zero width space and no-break mark", "a\u200bb\ufeffc", `a\u200bb\ufeffc`, `a\u200bb\ufeffc`},
		{"invisible operators and word joiner", "\u2060\u2061\u2062\u2063\u2064", `\u2060\u2061\u2062\u2063\u2064`, `\u2060\u2061\u2062\u2063\u2064`},
		{"deprecated format characters", "\u206a\u206b\u206c\u206d\u206e\u206f", `\u206a\u206b\u206c\u206d\u206e\u206f`, `\u206a\u206b\u206c\u206d\u206e\u206f`},
		{"tag characters carry hidden text", "ok\U000e0001\U000e0069\U000e007f", `ok\U000e0001\U000e0069\U000e007f`, `ok\U000e0001\U000e0069\U000e007f`},
		{"line feed", "a\nb", `a\nb`, "a\nb"},
		{"tab", "a\tb", `a\tb`, "a\tb"},
		{"CRLF", "a\r\nb", `a\r\nb`, "a\r\nb"},
		{"lone CR", "a\rb", `a\rb`, `a\rb`},
		{"CR then CRLF", "a\r\r\nb", `a\r\r\nb`, "a\\r\r\nb"},
		{"invalid byte", "a\xffb", `a\xffb`, `a\xffb`},
		{"truncated sequence", "a\xe2\x80", `a\xe2\x80`, `a\xe2\x80`},
		{"overlong encoding", "\xc0\x80", `\xc0\x80`, `\xc0\x80`},

		{"plain ASCII", "hello, world: 100%", "hello, world: 100%", "hello, world: 100%"},
		{"CJK", "\u4f60\u597d\uff0c\u4e16\u754c", "\u4f60\u597d\uff0c\u4e16\u754c", "\u4f60\u597d\uff0c\u4e16\u754c"},
		{"Arabic and Hebrew letters", "مرحبا שלום", "مرحبا שלום", "مرحبا שלום"},
		{"emoji and joined emoji", "😀 👨\u200d👩\u200d👧", "😀 👨\u200d👩\u200d👧", "😀 👨\u200d👩\u200d👧"},
		{"accents and combining marks", "e\u0301 é ñ", "e\u0301 é ñ", "e\u0301 é ñ"},
		{"a real replacement character", "a\ufffdb", "a\ufffdb", "a\ufffdb"},
		{"neighbours of the classes", "\u00a0\u2027\u202f\u2065\u2070\u205f\u200c\u200d\ufe0f\U000dffff\U000e0080", "\u00a0\u2027\u202f\u2065\u2070\u205f\u200c\u200d\ufe0f\U000dffff\U000e0080", "\u00a0\u2027\u202f\u2065\u2070\u205f\u200c\u200d\ufe0f\U000dffff\U000e0080"},
		{"backslashes are not doubled", `C:\dir\n`, `C:\dir\n`, `C:\dir\n`},
		{"empty", "", "", ""},
	} {
		if got := termout.SanitizeLine(tc.in); got != tc.line {
			t.Errorf("%s: SanitizeLine(%q) = %q, want %q", tc.name, tc.in, got, tc.line)
		}
		if got := termout.SanitizeText(tc.in); got != tc.text {
			t.Errorf("%s: SanitizeText(%q) = %q, want %q", tc.name, tc.in, got, tc.text)
		}
	}
}

func TestTimestamp(t *testing.T) {
	at := time.Date(2026, 11, 7, 1, 40, 59, 0, time.UTC)
	if got := termout.Timestamp(at); got != "2026-11-07 01:40 UTC" {
		t.Errorf("Timestamp = %q", got)
	}
	if got := termout.Timestamp(at.In(time.FixedZone("x", -5*3600)).Add(time.Hour)); got != "2026-11-07 02:40 UTC" {
		t.Errorf("Timestamp in another zone = %q", got)
	}
}

func TestTime(t *testing.T) {
	now := time.Date(2026, 11, 7, 1, 40, 30, 0, time.UTC)
	s := termout.New(nil, true, false, 80)
	for _, tc := range []struct {
		name string
		at   time.Time
		want string
	}{
		{"in 30 days", now.AddDate(0, 0, 30), "2026-12-07 01:40 UTC (in 30 days)"},
		{"just under 30 days rounds", now.AddDate(0, 0, 30).Add(-time.Second), "2026-12-07 01:40 UTC (in 30 days)"},
		{"hours ago", now.Add(-2 * time.Hour), "2026-11-06 23:40 UTC (2 h ago)"},
		{"seconds", now.Add(45 * time.Second), "2026-11-07 01:41 UTC (in 45 s)"},
		{"now", now, "2026-11-07 01:40 UTC (now)"},
		{"half a second is now", now.Add(500 * time.Millisecond), "2026-11-07 01:40 UTC (now)"},
		{"shown in UTC whatever the zone", now.In(time.FixedZone("x", 8*3600)).Add(time.Hour), "2026-11-07 02:40 UTC (in 1 h)"},
	} {
		if got := s.Time(tc.at, now); got != tc.want {
			t.Errorf("%s: Time = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := termout.New(nil, true, true, 80).Time(now, now); got != "2026-11-07 01:40 UTC \x1b[2m(now)\x1b[0m" {
		t.Errorf("colored Time = %q", got)
	}
}

func TestDuration(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "0 s"},
		{999 * time.Millisecond, "0 s"},
		{45 * time.Second, "45 s"},
		{59 * time.Second, "59 s"},
		{time.Minute, "1 min"},
		{90 * time.Second, "1 min 30 s"},
		{59*time.Minute + 59*time.Second, "59 min 59 s"},
		{time.Hour, "1 h"},
		{2*time.Hour + 5*time.Minute + 40*time.Second, "2 h 5 min"},
		{23*time.Hour + 59*time.Minute, "23 h 59 min"},
		{24 * time.Hour, "1 day"},
		{25 * time.Hour, "1 day 1 h"},
		{30 * 24 * time.Hour, "30 days"},
		{30*24*time.Hour + 3*time.Hour + 20*time.Minute, "30 days 3 h"},
		{-2 * time.Hour, "2 h"},
	} {
		if got := termout.Duration(tc.d); got != tc.want {
			t.Errorf("Duration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestBytes(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1000, "1.0 kB"},
		{1500, "1.5 kB"},
		{999_949, "999.9 kB"},
		{999_950, "1.0 MB"},
		{268_400_000, "268.4 MB"},
		{32_200_000_000, "32.2 GB"},
		{999_950_000_000, "1.0 TB"},
		{9_223_372_036_854_775_807, "9.2 EB"},
		{-1500, "-1.5 kB"},
		{-999, "-999 B"},
		{-9_223_372_036_854_775_808, "-9.2 EB"},
	} {
		if got := termout.Bytes(tc.n); got != tc.want {
			t.Errorf("Bytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
