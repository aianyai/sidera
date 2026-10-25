//go:build windows

package fsxtest

import "golang.org/x/sys/windows"

// DiskFull is the fault a test injects for a full volume: the error the platform
// itself returns for one, so that code which recognises a full disk
// (fsx.IsNoSpace) meets the real value. ERROR_DISK_FULL here, ENOSPC on Unix.
var DiskFull error = windows.ERROR_DISK_FULL
