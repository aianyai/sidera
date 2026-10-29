//go:build crashtest && !windows

package daemon

import (
	"os"
	"syscall"
)

func killCrashProcess() {
	if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
		panic(err)
	}
	select {}
}
