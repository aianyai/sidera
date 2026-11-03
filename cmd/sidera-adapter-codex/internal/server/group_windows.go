//go:build windows

package server

import "github.com/aianyai/sidera/sdk/go/adapterkit/process"

// signalGroup ends the server process; Windows has no process groups to signal,
// so what the server started is not reached.
func signalGroup(s process.Started, _ bool) error { return s.Kill() }

// groupGone cannot be told on this platform.
func groupGone(int) bool { return true }
