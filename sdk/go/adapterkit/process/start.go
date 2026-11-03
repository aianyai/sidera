package process

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Spec describes a process to start detached from the adapter.
type Spec struct {
	// Argv[0] is the executable, an absolute path, started as written: it is not
	// looked up in PATH, and a relative path is refused because a start in a
	// systemd scope would make it absolute against the working directory or look a
	// bare name up, and the two ways of starting would then run different programs.
	Argv []string
	// Env is the whole environment (see package environ); nil inherits the
	// adapter's. On Linux a start in a systemd scope needs the XDG_RUNTIME_DIR and
	// DBUS_SESSION_BUS_ADDRESS the adapter itself has, to reach the user manager:
	// when Env leaves either out, that start is made in a session of its own without
	// a scope, and Started.Detachment says so.
	Env []string
	Dir string // working directory; empty is the adapter's
	// Stdin, Stdout, Stderr: nil is the null device. A regular file is expected; a
	// pipe would tie the process to the adapter. The caller closes its copies after
	// Start returns.
	Stdin, Stdout, Stderr *os.File
	// Interruptible: the process can be stopped with Interrupt. Only Windows
	// distinguishes: such a process gets a hidden console of its own, any other gets
	// none. Before the first such start the adapter itself stops ignoring Ctrl-C
	// (SetConsoleCtrlHandler(NULL, FALSE)), because a process inherits that setting
	// and an ignored Ctrl-C would make the interrupt of a child silently do nothing;
	// the adapter's own setting stays changed for the rest of its life.
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
	proc *os.Process
}

// Kill ends the process by force: SIGKILL on Unix, TerminateProcess on Windows.
// Only the process itself is ended, not what it started; on Unix a caller that
// wants the whole group signals the negated PID (the process leads a group of its
// own). Once the process has ended and been reaped the error wraps
// os.ErrProcessDone.
func (s Started) Kill() error {
	if s.proc == nil {
		return errors.New("process: nothing started")
	}
	return s.proc.Kill()
}

// Start starts spec detached: on Unix in a session and process group of its own
// (its PID is the group's ID), and on Linux in a systemd user scope when the user
// manager is usable; on Windows outside the adapter's job when the job allows it.
// Falling back never fails the start; the fallback shows in Detachment. An error
// that wraps ErrStartedUnidentified means the process runs; any other error means
// nothing started, which is also the case on a platform without a process table
// (ErrUnsupported). Safe for concurrent use.
func Start(spec Spec) (Started, error) {
	if len(spec.Argv) == 0 {
		return Started{}, errors.New("process: nothing to start")
	}
	if !filepath.IsAbs(spec.Argv[0]) {
		return Started{}, fmt.Errorf("process: the executable %q is not an absolute path", spec.Argv[0])
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
		proc:       cmd.Process,
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
// anything has started; Started.Detachment is authoritative for each start. On Linux
// it is decided once per process, on the first call to it or to Start, and that
// call runs two commands with a limit of 5 s each, so it can take up to about ten
// seconds; a start whose Env lacks the variables of the user manager is made
// without a scope whatever this says. On Windows it is read from the adapter's job
// on every call, and only the job the adapter is directly in is read: an outer job
// of a nest that forbids breakaway is not seen here and shows in the Detachment of
// a start that was refused. Elsewhere it is constant.
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
