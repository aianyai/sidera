//go:build !windows

package environ

// foldCase says that environment variable names are compared without regard to
// case; elsewhere they are compared as written.
const foldCase = false
