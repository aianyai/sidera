package adapterhost

import (
	"errors"
	"syscall"
)

// killFoundItEnded reports whether an error of os.Process.Kill says that the
// process had already ended, so that there was nothing left to end. On Windows
// two errors say so:
//   - ERROR_ACCESS_DENIED: "After a process has terminated, call to
//     TerminateProcess with open handles to the process fails with
//     ERROR_ACCESS_DENIED" (the documentation of TerminateProcess). The daemon
//     created the process and holds its handle with every access right until
//     Wait, so a refusal can mean nothing else; a second Kill of a process that
//     is still ending gets the same answer.
//   - EINVAL: once Wait has returned, package os has released the handle and
//     refuses to use it.
func killFoundItEnded(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, syscall.EINVAL)
}
