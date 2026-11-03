package update

import (
	"os"
	"os/signal"
	"syscall"
)

// holdSignals keeps an interrupt, a termination request or, on Unix, a hangup (what
// an upgrade over SSH receives when the connection drops) from ending the process
// until the returned function is called, which says whether one arrived. The console's
// Ctrl-C on Windows is held as an interrupt. The replacement of the programs is not atomic across
// a crash: an interrupt between its two renames of one program would leave the
// program missing under its name. A signal that arrives is kept for the caller to
// act on once the replacement is complete or undone.
func holdSignals() (release func() (arrived bool)) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	return func() bool {
		signal.Stop(ch)
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
}
