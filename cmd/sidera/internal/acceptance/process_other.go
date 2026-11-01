//go:build !windows

package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Unix fixtures retain their existing direct-process ownership. Detached native
// runtimes are stopped by the fixture's control listener before this scope ends.
type processScope struct {
	mu        sync.Mutex
	closed    bool
	processes []*process
	err       error
}

func newProcessScope() (*processScope, error) { return &processScope{}, nil }

func (s *processScope) start(spec processSpec) (*process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("acceptance: process scope is closed")
	}
	if err := spec.validate(); err != nil {
		return nil, err
	}
	in, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	cmd := exec.Command(spec.exe, spec.args...)
	cmd.Dir, cmd.Env = spec.dir, append([]string{}, spec.env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, spec.stdout, spec.stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &process{native: cmd.Process, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	s.processes = append(s.processes, p)
	return p, nil
}

func (s *processScope) close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, p := range s.processes {
			if !p.exited() {
				if err := p.native.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
					s.err = errors.Join(s.err, err)
				}
			}
		}
		for _, p := range s.processes {
			select {
			case <-p.done:
			case <-cleanup.Done():
				s.err = errors.Join(s.err, cleanup.Err())
			}
		}
	}
	return errors.Join(ctx.Err(), s.err)
}
