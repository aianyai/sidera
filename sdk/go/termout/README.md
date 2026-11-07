# termout

The shared implementation of the human-readable terminal output of Sidera programs:
for `sidera`, the adapter programs and any third-party adapter that wants to look
and behave alike. The package comment is the overview; this file gives the rules a
change has to keep. Imports: the standard library, `adapterproto` (the person-text
rule), `golang.org/x/sys` (terminal checks) and `golang.org/x/text/width` (East Asian
width).

## Entry points

| Name | What it is for |
| ---- | -------------- |
| `Open(w, mode, getenv)` | The `Stream` of a real output, asked once at start-up |
| `New(w, decorated, colored, width)` | A `Stream` from known facts; what tests use |
| `Decide(terminal, mode, getenv)` | The decoration and color rule, with no I/O |
| `ParseColorMode` | The value of a `--color` flag |
| `IsTerminal(v)` | Whether a file is a terminal; the check before a question on stdin |
| `Stream.Bold`, `Dim`, `Green`, `Yellow`, `Red`, `Cyan`, `Command` | Styles; `Command` writes a command name |
| `Stream.OK`, `Warn`, `Fail`, `MarkerWidth` | Marker lines, and the columns a marker takes |
| `Stream.Block`, `Stream.List`, `Stream.Table`, `Indent`, `DisplayWidth` | Aligned layouts |
| `Wrap`, `Stream.TextWidth` | Prose |
| `SanitizeLine`, `SanitizeText` | Text from outside the program |
| `Stream.Time`, `Timestamp`, `Duration`, `Bytes` | Written forms of values |

## Rules

- **The environment is injected.** Nothing here calls `os.Getenv`; the program passes a
  lookup and a test passes a map.
- **Decorated and colored are two facts, and colored implies decorated.** A terminal, or
  anything that forces color, is decorated: its lines get markers. Color follows `Decide`'s
  order and brings every style with it. `NO_COLOR` removes color and not markers. A variable
  that only announces an agent plays no part. `FORCE_COLOR` and `CLICOLOR_FORCE` force
  only when set to something other than empty, `0` or `false`, because Node.js reads
  `FORCE_COLOR=0` as "no color". Output therefore has three states and no more: plain,
  decorated, colored.
- **Width is display width.** Alignment pads by `DisplayWidth` and styles afterwards, so
  wide characters and colored cells line up. `text/tabwriter` cannot do this: it counts
  the bytes of a color sequence and gives every code point width 1. Decomposed Hangul, a
  variation selector and a skin tone modifier are counted as a terminal draws them.
- **Layouts do not wrap what is not prose.** `Block` and `Table` never wrap or truncate:
  their values are identifiers and paths. `List` wraps its descriptions at the width it is
  given, under their own column, for help and long explanations.
- **Layouts leave no trailing whitespace.** Everything that indents (`Indent`, a marker's
  continuation lines, a table's last cell) skips empty lines and the final line break.
  A block that goes under a marker or a title is indented with `Indent`.
- **Foreign text is sanitized by its caller, once, at the point of output.** The class of
  characters (`isUnsafe`) is the person-text rule of `adapterproto`
  (`RefusedInPersonText`, called, not copied), plus invisible format characters (tag
  characters, zero-width space, word joiner and the like) that a person cannot see and an
  agent reading the same text can. The first part is whatever the protocol refuses in a
  display name; the second is a display rule only. U+200D stays, to keep emoji sequences
  whole.
- **Windows.** A colored console stream first has `ENABLE_VIRTUAL_TERMINAL_PROCESSING`
  switched on; when the console refuses, the stream is decorated and not colored. Git-Bash
  and mintty are pipes to Windows and count as not a terminal.
- **Other platforms.** Where there is no terminal code (anything but darwin, linux and
  windows) no stream is a terminal and has no width; output is plain unless forced.

Tests are black-box tables: the color decision (with the property that colored implies
decorated), widths and alignment, one row per character class of the sanitizers, and the
boundaries of the value forms.
