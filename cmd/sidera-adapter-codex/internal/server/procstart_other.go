//go:build !windows

package server

// processStart tells nothing on this platform: a second server for the socket
// path cannot run, so no check of a recorded server is needed.
func processStart(int) (int64, bool) { return 0, false }
