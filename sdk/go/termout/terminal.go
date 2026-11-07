package termout

// descriptor is what a file has that says which terminal it is attached to.
type descriptor interface{ Fd() uintptr }

// IsTerminal says whether v is a file attached to a terminal. Anything that is not
// a file (a pipe's other end held by a test, a buffer, a reader wrapped by a
// program) is not. It is the single terminal check of every Sidera program: stdin
// before a question, stdout and stderr before decorating. A Git-Bash or mintty
// console is a pipe to Windows and is reported as not a terminal.
func IsTerminal(v any) bool {
	f, ok := v.(descriptor)
	return ok && fdIsTerminal(f.Fd())
}

// terminalColumns is the width of the terminal v is attached to, or 0 when v is not
// one or the system does not say.
func terminalColumns(v any) int {
	f, ok := v.(descriptor)
	if !ok {
		return 0
	}
	return fdColumns(f.Fd())
}
