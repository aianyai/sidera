//go:build !windows

package adapterhost

// killFoundItEnded reports whether an error of os.Process.Kill says that the
// process had already ended. Elsewhere than on Windows, os says so with
// os.ErrProcessDone alone, which Kill tests itself, and a process that has ended
// but was not waited for still takes the signal.
func killFoundItEnded(error) bool { return false }
