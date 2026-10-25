//go:build unix

package fsxtest

import "syscall"

// DiskFull is the fault a test injects for a full volume: the error the platform
// itself returns for one, so that code which recognises a full disk
// (fsx.IsNoSpace) meets the real value. ENOSPC here, ERROR_DISK_FULL on Windows.
var DiskFull error = syscall.ENOSPC
