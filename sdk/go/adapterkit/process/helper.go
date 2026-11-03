package process

import (
	"os"
	"runtime"
)

// helperArg is the first argument of an invocation of the adapter's own executable
// as the helper that sends Ctrl-C.
const helperArg = "--sidera-adapterkit-helper"

// HandleHelperInvocation does a helper's work and ends the process when this
// process is a helper that Interrupt started from the adapter's own executable;
// otherwise it returns at once and does nothing. An adapter that calls Interrupt
// calls it as the first statement of main. On platforms without a helper it always
// returns at once.
func HandleHelperInvocation() {
	if runtime.GOOS != "windows" || len(os.Args) < 2 || os.Args[1] != helperArg {
		return
	}
	os.Exit(runHelper(os.Args[2:], os.Stdout, os.Stderr))
}
