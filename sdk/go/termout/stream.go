// Package termout is the shared implementation of the human-readable terminal output
// of Sidera programs: for the core CLI, the adapter programs and any third-party
// adapter that wants its output to look and behave alike.
//
// A [Stream] wraps one output (standard output and standard error each get their
// own) and knows three facts about it: whether it is decorated, whether it is
// colored and how wide it is. Everything else builds on them:
//
//   - styles, and marker lines that say ok, warning or failure ([Stream.Bold],
//     [Stream.OK] and the others);
//   - aligned layouts: a key/value block, a table and a list whose descriptions
//     wrap, which measure text by the columns it takes on a terminal
//     ([DisplayWidth]) and not by its bytes;
//   - paragraph wrapping ([Wrap]) and indenting ([Indent]);
//   - sanitizing of text the program did not write itself ([SanitizeLine],
//     [SanitizeText]);
//   - the written form of times, byte counts and durations.
//
// Decorated means a person is looking at a terminal, or asked to see the
// decorations anyway: lines get markers. Colored means escape sequences are written:
// every style, the markers' colors included. Colored implies decorated, so output
// has three states: plain, decorated (markers only) and colored (markers and styles).
// The states carry the same words: take the markers and styles away and every line
// still means the same.
//
// The environment is never read here. [Decide] and [Open] take a lookup function,
// so a program passes its own and a test passes a map. No variable that merely
// announces an agent (AI_AGENT, CLAUDECODE) takes part: they are unauthenticated
// hints, and the format follows the stream and the color variables alone.
package termout

import (
	"fmt"
	"io"
)

// ColorMode is the owner's say over color: the value of a --color flag.
type ColorMode int

const (
	// ColorAuto decides from the environment and from whether the stream is a
	// terminal. It is the zero value.
	ColorAuto ColorMode = iota
	// ColorAlways decorates and colors even a stream that is not a terminal.
	ColorAlways
	// ColorNever colors nothing; a terminal stream is still decorated.
	ColorNever
)

// ParseColorMode reads the value of a --color flag: auto, always or never.
func ParseColorMode(s string) (ColorMode, error) {
	switch s {
	case "auto":
		return ColorAuto, nil
	case "always":
		return ColorAlways, nil
	case "never":
		return ColorNever, nil
	}
	return ColorAuto, fmt.Errorf("%q is not a color mode: use auto, always or never", s)
}

// String is the flag spelling of the mode.
func (m ColorMode) String() string {
	switch m {
	case ColorAlways:
		return "always"
	case ColorNever:
		return "never"
	}
	return "auto"
}

// Decide is the rule for one stream. terminal says whether it is a terminal; getenv
// reads the environment (nil reads nothing).
//
// Color is decided by the first rule that matches:
//
//  1. mode never: no color;
//  2. mode always: color;
//  3. NO_COLOR set and not empty: no color;
//  4. CLICOLOR_FORCE or FORCE_COLOR forces: color. A variable forces when it is set
//     and is neither empty, "0" nor "false": Node.js defines FORCE_COLOR=0 as
//     switching color off, and the programs that run agents live in that world;
//  5. CLICOLOR is "0" or TERM is "dumb": no color;
//  6. otherwise color exactly when the stream is a terminal.
//
// A stream is decorated when it is a terminal or colored, so a forced color (by
// mode or by the environment) brings the markers with it, and a stream that is
// colored is always decorated. NO_COLOR removes color and nothing else: a terminal
// stays decorated.
func Decide(terminal bool, mode ColorMode, getenv func(string) string) (decorated, colored bool) {
	env := func(name string) string {
		if getenv == nil {
			return ""
		}
		return getenv(name)
	}
	forces := func(name string) bool {
		v := env(name)
		return v != "" && v != "0" && v != "false"
	}
	switch {
	case mode == ColorNever:
		colored = false
	case mode == ColorAlways:
		colored = true
	case env("NO_COLOR") != "":
		colored = false
	case forces("CLICOLOR_FORCE"), forces("FORCE_COLOR"):
		colored = true
	case env("CLICOLOR") == "0", env("TERM") == "dumb":
		colored = false
	default:
		colored = terminal
	}
	return terminal || colored, colored
}

// fallbackWidth is the width of a stream that is not a terminal, and of a terminal
// that does not say how wide it is.
const fallbackWidth = 80

// maxTextWidth bounds the line length of wrapped prose, however wide the terminal.
const maxTextWidth = 100

// Stream is one output of a program with what is known about it. It is an
// [io.Writer] that writes to the output it wraps. Build one with [Open] for a real
// output, or with [New] when the facts are known (tests, and programs that decide
// them differently).
type Stream struct {
	w         io.Writer
	decorated bool
	colored   bool
	width     int
}

// New builds a stream from the facts, touching nothing in the operating system. A
// width below 1 means 80. A colored stream is meant to be decorated too, as
// [Decide] has it.
func New(w io.Writer, decorated, colored bool, width int) *Stream {
	if width < 1 {
		width = fallbackWidth
	}
	return &Stream{w: w, decorated: decorated, colored: colored, width: width}
}

// Open builds the stream of a real output, the way a program does once at start-up:
// it asks whether w is a terminal and how wide, applies [Decide], and on Windows
// switches the console to read escape sequences before a colored stream is used. A
// console that refuses leaves the stream decorated and not colored. Color forced on
// a stream that is not a console is not checked: whatever reads it reads
// the sequences.
func Open(w io.Writer, mode ColorMode, getenv func(string) string) *Stream {
	terminal := IsTerminal(w)
	decorated, colored := Decide(terminal, mode, getenv)
	width := 0
	if terminal {
		width = terminalColumns(w)
		if colored && !enableColor(w.(descriptor).Fd()) {
			colored = false
		}
	}
	return New(w, decorated, colored, width)
}

// Write writes to the wrapped output unchanged.
func (s *Stream) Write(p []byte) (int, error) { return s.w.Write(p) }

// Decorated says whether markers are wanted. A stream that is colored is decorated.
func (s *Stream) Decorated() bool { return s.decorated }

// Colored says whether escape sequences are written: styles and the colors of the
// markers.
func (s *Stream) Colored() bool { return s.colored }

// Width is the width of the terminal in columns, and 80 for a stream that is not
// a terminal.
func (s *Stream) Width() int { return s.width }

// TextWidth is the width for wrapped prose: [Stream.Width], at most 100.
func (s *Stream) TextWidth() int { return min(s.width, maxTextWidth) }
