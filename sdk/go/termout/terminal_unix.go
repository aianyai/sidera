//go:build darwin || linux

package termout

import "golang.org/x/sys/unix"

func fdIsTerminal(fd uintptr) bool {
	_, err := unix.IoctlGetTermios(int(fd), ioctlReadTermios)
	return err == nil
}

func fdColumns(fd uintptr) int {
	size, err := unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ)
	if err != nil {
		return 0
	}
	return int(size.Col)
}

// enableColor is a no-op here: a Unix terminal reads escape sequences as it is.
func enableColor(uintptr) bool { return true }
