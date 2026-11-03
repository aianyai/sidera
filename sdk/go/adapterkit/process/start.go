package process

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Spec describes a process to start detached from the adapter.
type Spec struct {
	// Argv[0] is the executable, started as written: it is not looked up in PATH.
	Argv []string
	// Env is the whole environment (see package environ); nil inherits the
	// adapter's. On Linux it must keep XDG_RUNTIME_DIR and DBUS_SESSION_BUS_ADDRESS,
	// which systemd-run needs to reach the user manager.
	Env []string
	Dir string // working directory; empty is the adapter's
	// Stdin, Stdout, Stderr: nil is the null device. A regular file is expected; a
	// pipe would tie the process to the adapter. The caller closes its copies after
	// Start returns.
	Stdin, Stdout, Stderr *os.File
	// Interruptible: the process can be stopped with Interrupt. Only Windows
	// distinguishes: such a process gets a hidden console of its own, any other gets
	// none.
	Interruptible bool
}

// Detachment says how far a detached process escapes the adapter.
type Detachment struct {
	// OutlivesDaemon: the process survives the adapter and the daemon ending, by a
	// crash, a kill or a stop.
	OutlivesDaemon bool
	// Caveat, when set, says in words what can still end the process (or why it does
	// not outlive the daemon), for a probe or a log. Empty means no known limit.
	Caveat string
}

// Started is a process Start started.
type Started struct {
	Process    // identity read right after the start; Args nil
	Container  adapterproto.Container
	Detachment Detachment
	// Done receives the error of waiting for the process (nil for exit status 0)
	// once, after it has ended and been reaped. The kit always waits, so no zombie
	// is left behind.
	Done <-chan error
	// Handle is the started process, for Kill. Never call Wait or Release on it.
	Handle *os.Process
}

// Start starts spec detached: on Unix in a session and process group of its own
// (its PID is the group's ID), and on Linux in a systemd user scope when the user
// manager is usable; on Windows outside the adapter's job when the job allows it.
// Falling back never fails the start; the fallback shows in Detachment. An error
// that wraps ErrStartedUnidentified means the process runs; any other error means
// nothing started. Safe for concurrent use.
func Start(spec Spec) (Started, error) {
	if len(spec.Argv) == 0 {
		return Started{}, errors.New("process: nothing to start")
	}
	stdio, closeStdio, err := openStdio(spec)
	if err != nil {
		return Started{}, err
	}
	defer closeStdio()

	cmd, container, detachment, err := launch(spec, stdio)
	if err != nil {
		return Started{}, err
	}
	// The identity is read before the process is waited for: until it is reaped even
	// a process that ended at once is still in the table, with its start.
	p, _, lookupErr := lookup(cmd.Process.Pid, true)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	started := Started{
		Process:    Process{Identity: Identity{PID: cmd.Process.Pid}},
		Container:  container,
		Detachment: detachment,
		Done:       done,
		Handle:     cmd.Process,
	}
	if lookupErr != nil {
		return started, fmt.Errorf("%w: process %d: %w", ErrStartedUnidentified, cmd.Process.Pid, lookupErr)
	}
	if p.StartKey == "" {
		return started, fmt.Errorf("%w: process %d is not in the process table", ErrStartedUnidentified, cmd.Process.Pid)
	}
	started.Process = p
	return started, nil
}

// HostDetachment is what Start achieves on this host, for a probe to report before
// anything has started. It is decided once per process, on the first call to it or
// to Start. On Windows it is read from the adapter's job, and Started.Detachment is
// authoritative for each start.
func HostDetachment() Detachment { return hostDetachment() }

// stdio is the three standard streams a start gives its process.
type stdio struct{ in, out, err *os.File }

// openStdio fills the streams Spec leaves nil with the null device. The returned
// func closes the files this call opened, never the caller's.
func openStdio(spec Spec) (stdio, func(), error) {
	s := stdio{spec.Stdin, spec.Stdout, spec.Stderr}
	var opened []*os.File
	closeOpened := func() {
		for _, f := range opened {
			_ = f.Close()
		}
	}
	open := func(slot **os.File, flag int) error {
		if *slot != nil {
			return nil
		}
		f, err := os.OpenFile(os.DevNull, flag, 0)
		if err != nil {
			return err
		}
		opened = append(opened, f)
		*slot = f
		return nil
	}
	for _, o := range []struct {
		slot **os.File
		flag int
	}{{&s.in, os.O_RDONLY}, {&s.out, os.O_WRONLY}, {&s.err, os.O_WRONLY}} {
		if err := open(o.slot, o.flag); err != nil {
			closeOpened()
			return stdio{}, nil, fmt.Errorf("process: open the null device: %w", err)
		}
	}
	return s, closeOpened, nil
}

// command prepares the process argv describes, in the way the platform asks for.
// Path is argv[0] as written, so nothing is looked up in PATH.
func command(spec Spec, argv []string, s stdio, attr *syscall.SysProcAttr) *exec.Cmd {
	return &exec.Cmd{
		Path: argv[0], Args: argv, Env: spec.Env, Dir: spec.Dir,
		Stdin: s.in, Stdout: s.out, Stderr: s.err,
		SysProcAttr: attr,
	}
}
