package peer

import (
	"errors"
	"fmt"
	"io"
	"time"
)

// writeLoop is the only writer to the transport. Every line is written with
// one Write call so that lines never interleave.
//
// A stalled Write cannot be interrupted by a deadline on every platform
// (anonymous pipes on Windows have none), so each write is guarded by a timer
// that closes the transport when it fires; closing makes the blocked Write
// return.
func (p *Peer) writeLoop() {
	defer p.wg.Done()
	for {
		select {
		case it := <-p.out:
			if err := p.writeLine(it.line); err != nil {
				p.terminate(fmt.Errorf("peer: write: %w", err), nil)
				p.closeTransport()
				return
			}
			if it.last {
				p.closeTransport()
				return
			}
		case <-p.done:
			return
		}
	}
}

func (p *Peer) writeLine(line []byte) error {
	watchdog := time.AfterFunc(p.params.WriteTimeout, func() {
		p.terminate(ErrWriteTimeout, nil)
		p.closeTransport()
	})
	defer watchdog.Stop()
	_, err := p.w.Write(line)
	return err
}

// send queues a line and waits for room. It is for responses and for the
// error replies the read loop owes, which must not be dropped. A full queue
// stops the read loop, which is the backpressure on a flooding counterpart;
// the write timeout bounds the wait, because a queue that stays full means the
// writer is stuck.
func (p *Peer) send(line []byte) {
	select {
	case p.out <- outItem{line: line}:
	case <-p.done:
	}
}

// readLoop reads, classifies and dispatches inbound lines until the
// connection ends.
func (p *Peer) readLoop(tracked bool) {
	if tracked {
		defer p.wg.Done()
	}
	fr := newFrameReader(p.r, p.params.MaxLineBytes, readBufferSize)
	for {
		line, err := fr.next()
		if err != nil {
			p.readFailed(err)
			return
		}
		p.handleLine(line)
		if p.isClosed() {
			return
		}
	}
}

func (p *Peer) readFailed(err error) {
	switch {
	case errors.Is(err, ErrLineTooLong):
		// Reply, then close; never skip to the next newline, which would
		// resynchronize in the middle of the oversized message.
		p.terminate(ErrLineTooLong, errorLine(nil, NewError(p.dialect.LineTooLongCode, "line too long")))
	case err == io.EOF:
		p.terminate(ErrRemoteClosed, nil)
	default:
		p.terminate(fmt.Errorf("peer: read: %w", err), nil)
	}
}
