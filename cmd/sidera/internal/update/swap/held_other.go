//go:build !windows

package swap

// isHeld is false: a running program can be removed, and its inode lives on
// until it ends, so no removal fails because of it.
func isHeld(error) bool { return false }
