package proc

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

type osLauncher struct{ table Table }

// NewLauncher returns the launcher of the operating system, which reads the
// identity of what it starts from table.
func NewLauncher(table Table) Launcher { return osLauncher{table} }

func (l osLauncher) Start(spec Launch) (Started, error) {
	if len(spec.Argv) == 0 {
		return Started{}, fmt.Errorf("proc: nothing to start")
	}
	in, err := os.Open(spec.Stdin)
	if err != nil {
		return Started{}, err
	}
	defer in.Close()
	out, err := os.OpenFile(spec.Stdout, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return Started{}, err
	}
	defer out.Close()
	errf, err := os.OpenFile(spec.Stderr, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return Started{}, err
	}
	defer errf.Close()

	cmd := &exec.Cmd{
		Path: spec.Argv[0], Args: spec.Argv, Env: spec.Env, Dir: spec.Dir,
		Stdin: in, Stdout: out, Stderr: errf,
		// A session of its own: no controlling terminal, a process group of its
		// own, and a Ctrl-C in the daemon's terminal does not reach it.
		SysProcAttr: &syscall.SysProcAttr{Setsid: true},
	}
	if err := cmd.Start(); err != nil {
		return Started{}, err
	}
	// The identity is read before the process is waited for: until it is reaped
	// even a process that ended at once is still in the table, with its start.
	started := Started{PGID: cmd.Process.Pid}
	info, lookupErr := l.table.Lookup(cmd.Process.Pid)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if lookupErr != nil {
		return Started{}, fmt.Errorf("%w: process %d: %w", ErrStartedUnidentified, cmd.Process.Pid, lookupErr)
	}
	started.Info, started.Done = info, done
	return started, nil
}

type osSignaller struct{}

// NewSignaller returns the signaller of the operating system.
func NewSignaller() Signaller { return osSignaller{} }

func (osSignaller) Interrupt(pid int) error { return syscall.Kill(pid, syscall.SIGINT) }
