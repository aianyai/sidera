//go:build !darwin && !linux && !windows

package termout

// On a platform this package has no terminal code for, no stream is a terminal: it
// has no width and gets no escape sequences unless a program asks for them with
// --color=always or the environment forces them. Output stays correct, only plain.

func fdIsTerminal(uintptr) bool { return false }

func fdColumns(uintptr) int { return 0 }

func enableColor(uintptr) bool { return true }
