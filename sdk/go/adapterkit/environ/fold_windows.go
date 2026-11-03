//go:build windows

package environ

// foldCase says that environment variable names are compared without regard to
// case, as Windows does.
const foldCase = true
