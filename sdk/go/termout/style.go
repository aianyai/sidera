package termout

// The eight basic colors are the only ones used: they read on every terminal and
// follow the user's palette. Color and weight only strengthen a meaning that the
// words already carry.

// Bold makes text bold: block titles, table headers and the labels of a block.
func (s *Stream) Bold(text string) string { return s.sgr("1", text) }

// Dim makes text less prominent: relative times and the explanation in a hint.
func (s *Stream) Dim(text string) string { return s.sgr("2", text) }

// Green colors text green.
func (s *Stream) Green(text string) string { return s.sgr("32", text) }

// Yellow colors text yellow.
func (s *Stream) Yellow(text string) string { return s.sgr("33", text) }

// Red colors text red.
func (s *Stream) Red(text string) string { return s.sgr("31", text) }

// Cyan colors text cyan: the name of a command.
func (s *Stream) Cyan(text string) string { return s.sgr("36", text) }

// sgr wraps one line of text in a Select Graphic Rendition sequence. A stream that is
// not colored, and an empty text, come back unchanged.
func (s *Stream) sgr(code, text string) string {
	if !s.colored || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// Command writes the name of a command, in backticks so that it reads as one in plain
// text, cyan when colored: `sidera adapter list`.
func (s *Stream) Command(name string) string { return s.Cyan("`" + name + "`") }

// MarkerWidth is the columns a marker and the space after it take. Text under a
// marker line is indented by it on every stream, whether or not the marker is shown, so
// that the lines read as one item; prose that goes under a marker wraps at
// [Stream.TextWidth] minus MarkerWidth.
const MarkerWidth = 2

// OK is a line that reports success: "✓ text" on a decorated stream, the marker
// green when colored; the text alone otherwise. A text of several lines keeps its
// first line after the marker and indents the others by [MarkerWidth], on any stream,
// so that the lines read as one item. Blank lines and the final line break get no
// spaces.
func (s *Stream) OK(text string) string { return s.marked("✓", s.Green, text) }

// Warn is a line that asks for attention: "! text", the marker yellow. See [Stream.OK].
func (s *Stream) Warn(text string) string { return s.marked("!", s.Yellow, text) }

// Fail is a line that reports failure: "✗ text", the marker red. See [Stream.OK].
func (s *Stream) Fail(text string) string { return s.marked("✗", s.Red, text) }

func (s *Stream) marked(marker string, color func(string) string, text string) string {
	text = hang(text, MarkerWidth)
	if !s.decorated {
		return text
	}
	return color(marker) + " " + text
}
