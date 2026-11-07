package termout

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/width"
)

// DisplayWidth is the number of terminal columns s takes: East Asian wide and
// fullwidth characters count 2, combining marks and other zero-width characters 0,
// control characters 0, and ANSI color sequences (the ones this package writes) 0.
// It is the measure every alignment here uses; neither len nor text/tabwriter
// gives it, since they count the bytes of a color sequence and give every code
// point width 1.
//
// Three cases follow what terminals draw, not the width tables. An emoji
// presentation selector (U+FE0F) after a one-column character makes it two. A skin
// tone modifier (U+1F3FB to U+1F3FF) is drawn onto the emoji before it and adds
// nothing. The Hangul vowels and final consonants that make a syllable out of
// decomposed text (U+1160 to U+11FF, U+D7B0 to U+D7FF) add nothing, so a syllable
// written decomposed, as macOS writes file names, takes the two columns of the one
// written composed. East Asian ambiguous width counts 1, as in a terminal set for
// Western text. A sequence of emoji joined by a zero-width joiner is counted by its
// parts and so overestimates.
func DisplayWidth(s string) int {
	n, previous := 0, 0
	for i := 0; i < len(s); {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			i++ // the final byte
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		w := runeWidth(r)
		if r == 0xfe0f && previous == 1 {
			w = 1
			previous = 2
		} else {
			previous = w
		}
		n += w
	}
	return n
}

func runeWidth(r rune) int {
	switch {
	case r < 0x20, r >= 0x7f && r < 0xa0:
		return 0
	case r >= 0x1160 && r <= 0x11ff, r >= 0xd7b0 && r <= 0xd7ff, r >= 0x1f3fb && r <= 0x1f3ff:
		return 0
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}

// pad is the spaces that bring text of display width have up to want columns.
func pad(have, want int) string {
	if have >= want {
		return ""
	}
	return strings.Repeat(" ", want-have)
}

// Indent puts columns spaces before every line of text that has something on it.
// A blank line stays empty and the final line break stays the last character, so the
// result has no trailing spaces whether text ends in a line break or not. It is how
// a block goes under a title or a marker. A columns below 1 returns text unchanged.
func Indent(text string, columns int) string {
	if columns < 1 {
		return text
	}
	prefix := strings.Repeat(" ", columns)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

// hang indents every line of text but the first by columns: the first line follows
// something that is already on the line (a marker, a label), the others line up under
// it. Like [Indent] it writes no trailing spaces.
func hang(text string, columns int) string {
	first, rest, found := strings.Cut(text, "\n")
	if !found {
		return text
	}
	return first + "\n" + Indent(rest, columns)
}

// Field is one line of a block or a list: a label and its value.
type Field struct {
	Label string
	// Value may be styled, and may have several lines; the later lines line up under
	// the first.
	Value string
}

// Block lays out fields as a key/value block: the labels (bold on a colored stream)
// aligned on the left, then two spaces, then the values. A field whose value is empty
// is left out, which makes Block the one place of the rule "write none or omit the
// line"; a caller who wants "none" passes it. Each line ends in a line break and no
// line has trailing spaces. Labels are measured by [DisplayWidth] and padded before
// they are styled, so wide or colored text lines up. Nothing is wrapped: a value
// may be an identifier or a path, which must stay whole.
func (s *Stream) Block(fields []Field) string {
	filled := make([]Field, 0, len(fields))
	for _, f := range fields {
		if f.Value != "" {
			filled = append(filled, f)
		}
	}
	return s.List(filled, s.Bold, 0)
}

// List lays out fields as two columns for help and long prose: the labels on the
// left, aligned, then two spaces, then the descriptions, whose further lines line up
// under the first. label styles the label column (for example [Stream.Cyan] for
// command names); nil leaves labels as they are. A description is wrapped to the
// room that is left of width, breaking at spaces as [Wrap] does, and continues under
// its own column; a width below 1 wraps nothing. width is the columns the whole list
// may take, so a caller that will indent the list passes [Stream.TextWidth] minus the
// indent. A field with an empty value is written as its label alone. Values that must
// not wrap, identifiers and paths among them, belong in [Stream.Block].
func (s *Stream) List(fields []Field, label func(string) string, width int) string {
	if label == nil {
		label = func(text string) string { return text }
	}
	labelWidth := 0
	for _, f := range fields {
		labelWidth = max(labelWidth, DisplayWidth(f.Label))
	}
	column := labelWidth + 2
	var b strings.Builder
	for _, f := range fields {
		if f.Value == "" {
			b.WriteString(label(f.Label) + "\n")
			continue
		}
		value := f.Value
		if width > 0 {
			value = Wrap(value, max(width-column, 1))
		}
		b.WriteString(label(f.Label) + pad(DisplayWidth(f.Label), labelWidth) + "  " + hang(value, column) + "\n")
	}
	return b.String()
}

// Table lays out rows under a header. The header is written in upper case (bold on a
// colored stream); columns are separated by two spaces and each is as wide as its
// widest cell; an empty cell is written "-"; nothing is truncated or wrapped, so a
// long cell pushes the columns after it right, which is why identifiers belong in
// the last column. Each line ends in a line break and the last column is not padded.
//
// A cell may be styled but is one line, with one exception: the last cell of a row
// may have several lines, which continue under the last column (a note under the
// state it explains). Text that came from outside the program must have passed
// through [SanitizeLine] first. Every row must have as many cells as the header, and
// only a last cell may hold a line break; Table panics otherwise, since that is a
// mistake in the program.
func (s *Stream) Table(header []string, rows [][]string) string {
	last := len(header) - 1
	cells := make([][]string, 0, len(rows)+1)
	head := make([]string, len(header))
	for i, h := range header {
		if strings.Contains(h, "\n") {
			panic("termout: a table header has a line break")
		}
		head[i] = strings.ToUpper(h)
	}
	cells = append(cells, head)
	for _, row := range rows {
		if len(row) != len(header) {
			panic("termout: a table row has a different number of cells than the header")
		}
		cells = append(cells, make([]string, len(row)))
		for i, c := range row {
			if i < last && strings.Contains(c, "\n") {
				panic("termout: a table cell other than the last has a line break")
			}
			if c == "" {
				c = "-"
			}
			cells[len(cells)-1][i] = c
		}
	}
	widths := make([]int, len(header))
	for _, row := range cells {
		for i, c := range row[:last] {
			widths[i] = max(widths[i], DisplayWidth(c))
		}
	}
	lastColumn := 2 * last // where the last column starts
	for _, w := range widths[:last] {
		lastColumn += w
	}
	var b strings.Builder
	for n, row := range cells {
		for i, c := range row {
			if i > 0 {
				b.WriteString("  ")
			}
			switch {
			case n == 0:
				b.WriteString(s.Bold(c))
			case i == last:
				b.WriteString(hang(c, lastColumn))
			default:
				b.WriteString(c)
			}
			if i < last {
				b.WriteString(pad(DisplayWidth(c), widths[i]))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Wrap breaks the lines of text that are wider than width columns at spaces and tabs
// (and only there: a no-break space or an ideographic space holds its word together).
// A line that fits is left alone, spaces and all; a longer line is rewrapped word by
// word, its leading spaces and tabs repeated on every new line as they were (a tab
// counts as one column), and a word wider than width stays whole on a line of its own.
// Line breaks in text stay. A width below 1 returns text unchanged. Use it for help
// and explanations, not for tables and identifiers; [Stream.TextWidth] is the width
// to pass.
func Wrap(text string, width int) string {
	if width < 1 {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = wrapLine(line, width)
	}
	return strings.Join(lines, "\n")
}

func isBlank(r rune) bool { return r == ' ' || r == '\t' }

func wrapLine(line string, width int) string {
	if DisplayWidth(line) <= width {
		return line
	}
	words := strings.FieldsFunc(line, isBlank)
	indent := line[:len(line)-len(strings.TrimLeftFunc(line, isBlank))]
	var b strings.Builder
	b.WriteString(indent)
	used := len(indent)
	for i, w := range words {
		ww := DisplayWidth(w)
		switch {
		case i == 0:
		case used+1+ww > width:
			b.WriteString("\n" + indent)
			used = len(indent)
		default:
			b.WriteString(" ")
			used++
		}
		b.WriteString(w)
		used += ww
	}
	return b.String()
}
