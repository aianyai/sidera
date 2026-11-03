//go:build darwin || linux

package cli

import "golang.org/x/sys/unix"

func fdIsTerminal(fd uintptr) bool {
	_, err := unix.IoctlGetTermios(int(fd), ioctlReadTermios)
	return err == nil
}
