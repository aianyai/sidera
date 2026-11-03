package cli

import "io"

// isTerminal says whether r is a terminal a person types into. Anything that is not a
// file (a pipe, a test's reader) is not.
func isTerminal(r io.Reader) bool {
	f, ok := r.(interface{ Fd() uintptr })
	return ok && fdIsTerminal(f.Fd())
}
