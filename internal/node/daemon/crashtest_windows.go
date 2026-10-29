//go:build crashtest && windows

package daemon

import "golang.org/x/sys/windows"

func killCrashProcess() {
	if err := windows.TerminateProcess(windows.CurrentProcess(), 3); err != nil {
		panic(err)
	}
	select {}
}
