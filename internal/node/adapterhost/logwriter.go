package adapterhost

import (
	"bytes"
	"sync"

	"github.com/aianyai/sidera/internal/node/logfile"
)

// maxLogLine is the longest part of a line kept waiting for its end; a longer one
// is written as it is.
const maxLogLine = 64 << 10

// logWriter is what an adapter's standard error is written to. A log takes one
// line per write and rotates between writes, so the bytes the process writes are
// cut into lines here; a partial line waits for its end. After Close it takes
// what the process still writes and drops it: a process that outlives its log
// must not be blocked or failed by it. A write that the log refuses is dropped
// too: the log is diagnostic. A logWriter without a log discards everything.
type logWriter struct {
	dst *logfile.Log

	mu     sync.Mutex
	buf    []byte
	closed bool
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.dst == nil {
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		_, _ = w.dst.Write(w.buf[:i+1])
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > maxLogLine {
		_, _ = w.dst.Write(w.buf)
		w.buf = nil
	}
	return len(p), nil
}

// Close writes the partial line that is left, with its end, and closes the log.
func (w *logWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	if w.dst == nil {
		return
	}
	if len(w.buf) > 0 {
		_, _ = w.dst.Write(append(w.buf, '\n'))
		w.buf = nil
	}
	_ = w.dst.Close()
}
