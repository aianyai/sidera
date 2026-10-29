package acceptance

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

// process owns exactly one Wait, installed as soon as Start succeeds. Output
// goes to files: a surviving runtime cannot keep an output-copy pipe open.
type process struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error // published by closing done
}

func startProcess(cmd *exec.Cmd) (*process, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &process{cmd: cmd, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	return p, nil
}

func (p *process) wait(ctx context.Context) error {
	select {
	case <-p.done:
		return p.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *process) killAndWait() (bool, error) {
	err := p.cmd.Process.Kill()
	killed := err == nil
	if errors.Is(err, os.ErrProcessDone) {
		err = nil
	}
	<-p.done
	return killed, err
}

func (p *process) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}
