package adapterhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// execWaitDelay is how long after the program has ended its streams are kept
// open for what a process it started still holds: an adapter starts native
// sessions that outlive it, and one of them that kept the adapter's standard
// output open would otherwise keep Wait from ever returning.
const execWaitDelay = 2 * time.Second

// ExecLauncher starts an adapter as a child process of the daemon.
type ExecLauncher struct {
	// Env is added to the daemon's own environment for the process; a fixture
	// uses it to tell a fake adapter where its configuration is.
	Env []string
}

var _ Launcher = ExecLauncher{}

// Launch starts the program the manifest names, in the directory of the
// installed package, with the manifest's arguments. The process is not tied to
// ctx: the host decides when it ends. Its standard error goes to inst.Stderr.
// Its standard output is delivered through a pipe that is closed after the
// process has been waited for, so that every byte it wrote is read before the
// stream ends.
func (l ExecLauncher) Launch(_ context.Context, inst Instance) (Process, error) {
	entry := inst.Adapter.Manifest.Entry
	cmd := exec.Command(filepath.Join(inst.Dir, filepath.FromSlash(entry.Program)), entry.Args...)
	cmd.Env = append(os.Environ(), l.Env...)
	cmd.Stderr = inst.Stderr
	cmd.WaitDelay = execWaitDelay
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("adapterhost: standard input of %s: %w", entry.Program, err)
	}
	stdoutR, stdoutW := io.Pipe()
	cmd.Stdout = stdoutW
	if err := cmd.Start(); err != nil {
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, fmt.Errorf("adapterhost: start %s: %w", entry.Program, err)
	}
	p := &execProcess{cmd: cmd, stdin: stdin, stdout: stdoutR, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		_ = stdoutW.Close()
		close(p.done)
	}()
	return p, nil
}

type execProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *io.PipeReader
	done   chan struct{}
	err    error // set before done is closed
}

func (p *execProcess) Stream() (io.Reader, io.WriteCloser) { return p.stdout, p.stdin }

func (p *execProcess) PID() int { return p.cmd.Process.Pid }

// Kill ends the process; it does nothing once the process has ended, whether or
// not it has been waited for.
func (p *execProcess) Kill() error {
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) && !killFoundItEnded(err) {
		return err
	}
	return nil
}

func (p *execProcess) Wait() error {
	<-p.done
	return p.err
}
