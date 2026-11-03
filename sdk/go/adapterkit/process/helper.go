package process

import (
	"os"
	"runtime"
	"sync/atomic"
)

// helperArg is the first argument of an invocation of the adapter's own executable
// as the helper that sends Ctrl-C.
const helperArg = "--sidera-adapterkit-helper"

// helperHandled records that the adapter's main called HandleHelperInvocation.
// Interrupt refuses to run without it on every platform, so that a missing call
// shows in a test on any machine and not only when a Windows interrupt first runs.
var helperHandled atomic.Bool

// HandleHelperInvocation does a helper's work and ends the process when this
// process is a helper that Interrupt started from the adapter's own executable;
// otherwise it returns at once and does nothing. An adapter that calls Interrupt
// calls it as the first statement of main; Interrupt fails on every platform until
// it has been called. On platforms without a helper it always returns at once.
func HandleHelperInvocation() {
	helperHandled.Store(true)
	if runtime.GOOS != "windows" || len(os.Args) < 2 || os.Args[1] != helperArg {
		return
	}
	os.Exit(runHelper(os.Args[2:], os.Stdout, os.Stderr))
}
