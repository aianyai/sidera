package process

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"golang.org/x/sys/windows"
)

// launch starts the process outside the adapter's console, with no window, and,
// when the job the adapter runs in allows it, outside that job, so that it
// outlives the adapter and the daemon.
//
// An interruptible process gets a console of its own, which is what Interrupt
// attaches to to send Ctrl-C. Any other gets none.
func launch(spec Spec, s stdio) (*exec.Cmd, adapterproto.Container, Detachment, error) {
	if spec.Interruptible {
		if err := enableCtrlC(); err != nil {
			return nil, adapterproto.Container{}, Detachment{}, err
		}
	}
	cmd := command(spec, spec.Argv, s, attr(spec.Interruptible, true))
	err := cmd.Start()
	detachment := Detachment{OutlivesDaemon: true}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		// The job forbids breakaway, which is the one reason worth a second try
		// without the flag. The process then stays in the job.
		cmd = command(spec, spec.Argv, s, attr(spec.Interruptible, false))
		if err = cmd.Start(); err == nil {
			detachment = stayingDetachment()
		}
	}
	if err != nil {
		return nil, adapterproto.Container{}, Detachment{}, err
	}
	return cmd, adapterproto.Container{Kind: adapterproto.ContainerNone}, detachment, nil
}

func attr(interruptible, breakaway bool) *syscall.SysProcAttr {
	flags := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS)
	if interruptible {
		flags = windows.CREATE_NEW_CONSOLE
	}
	if breakaway {
		flags |= windows.CREATE_BREAKAWAY_FROM_JOB
	}
	return &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
}

// A process inherits "ignore Ctrl-C" from the process that starts it, and a
// process started over SSH or by a service has it set. Without clearing it first
// the interrupt of a child is silently ignored, and the child runs to its end.
var ctrlC struct {
	sync.Mutex
	enabled bool
}

func enableCtrlC() error {
	ctrlC.Lock()
	defer ctrlC.Unlock()
	if !ctrlC.enabled {
		if err := setConsoleCtrlHandler(false); err != nil {
			return fmt.Errorf("process: enable Ctrl-C for the processes to start: %w", err)
		}
		ctrlC.enabled = true
	}
	return nil
}

func hostDetachment() Detachment {
	inJob, flags, err := currentJob()
	switch {
	case err != nil:
		return unreadableJob(err)
	case !inJob || flags&(windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK|windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK) != 0:
		return Detachment{OutlivesDaemon: true}
	}
	return staysInJob(flags)
}

// stayingDetachment is the detachment of a process that could not break away from
// the adapter's job.
func stayingDetachment() Detachment {
	inJob, flags, err := currentJob()
	switch {
	case err != nil:
		return unreadableJob(err)
	case !inJob:
		return Detachment{OutlivesDaemon: true}
	}
	return staysInJob(flags)
}

func staysInJob(flags uint32) Detachment {
	if flags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE != 0 {
		return Detachment{Caveat: "stays in the adapter's job, which ends its processes when it closes"}
	}
	return Detachment{OutlivesDaemon: true,
		Caveat: "stays in the adapter's job (breakaway forbidden), which does not end its processes"}
}

// unreadableJob is what is said when the job cannot be read: nothing is promised.
func unreadableJob(err error) Detachment {
	return Detachment{Caveat: "stays in the adapter's job, and whether the job ends its processes could not be read: " + err.Error()}
}
