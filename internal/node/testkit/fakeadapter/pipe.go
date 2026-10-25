package fakeadapter

import (
	"context"
	"io"
	"sync"
)

// PipeProcess is a fake adapter that runs in the test process and is supervised the
// way a child process is: it has an output and an input to speak the protocol on, a
// process id (0: it has none of its own), a Kill and a Wait. An engine-layer fixture
// hands it to the adapter host as the "process" it started, which gives the
// determinism of one process and the supervision paths of two.
type PipeProcess struct {
	stdout *io.PipeReader // what the adapter writes
	stdin  *io.PipeWriter // what the daemon writes

	cancel context.CancelFunc
	done   chan struct{}
	err    error

	once sync.Once
}

// StartPipe starts Serve on a pair of pipes in a goroutine that ends when the
// process is killed, when ctx ends or when the daemon closes its end.
func StartPipe(ctx context.Context, cfg Config) *PipeProcess {
	ctx, cancel := context.WithCancel(ctx)
	adapterIn, daemonOut := io.Pipe() // the daemon writes, the adapter reads
	daemonIn, adapterOut := io.Pipe() // the adapter writes, the daemon reads
	p := &PipeProcess{stdout: daemonIn, stdin: daemonOut, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		p.err = Serve(ctx, adapterIn, adapterOut, cfg)
		// Both ends close when the adapter is over, so the daemon's peer sees the
		// end of the stream as it would when a child exits.
		_ = adapterOut.Close()
		_ = adapterIn.Close()
	}()
	return p
}

// Stream returns the adapter's standard output, to read from, and its standard input,
// to write to.
func (p *PipeProcess) Stream() (io.Reader, io.WriteCloser) { return p.stdout, p.stdin }

// PID is 0: the adapter is not a process of its own.
func (p *PipeProcess) PID() int { return 0 }

// Kill ends the adapter now: its context ends and both ends of the pipes close, as
// if the process had died. It may be called more than once.
func (p *PipeProcess) Kill() error {
	p.once.Do(func() {
		p.cancel()
		_ = p.stdin.Close()
		_ = p.stdout.Close()
	})
	return nil
}

// Wait returns when the adapter has ended, with what Serve returned: nil when the
// daemon closed the stream, the context's error after a Kill.
func (p *PipeProcess) Wait() error {
	<-p.done
	return p.err
}
