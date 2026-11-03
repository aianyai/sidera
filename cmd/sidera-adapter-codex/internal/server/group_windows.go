//go:build windows

package server

import "os"

// signalGroup ends the server process; Windows has no process groups to signal,
// so what the server started is not reached.
func signalGroup(p *os.Process, _ bool) error { return p.Kill() }

// groupGone cannot be told on this platform.
func groupGone(int) bool { return true }
