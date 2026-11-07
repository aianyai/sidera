package termout

import "golang.org/x/sys/windows"

func fdIsTerminal(fd uintptr) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(fd), &mode) == nil
}

func fdColumns(fd uintptr) int {
	var info windows.ConsoleScreenBufferInfo
	if windows.GetConsoleScreenBufferInfo(windows.Handle(fd), &info) != nil {
		return 0
	}
	return int(info.Window.Right-info.Window.Left) + 1
}

// enableColor turns on the console's reading of escape sequences and says whether
// it is on. An older console refuses; the caller then writes no colors.
func enableColor(fd uintptr) bool {
	h := windows.Handle(fd)
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
