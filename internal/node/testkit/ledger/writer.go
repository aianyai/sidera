package ledger

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Factory opens the writer of one process for one writer name. Both backends
// implement it, so a fake takes a Factory and does not care where entries go.
type Factory interface {
	// Open returns the writer named writer for the process pid (0 for a writer in
	// the test process). now is the shared test clock.
	Open(writer string, pid int, now func() time.Time) (*Writer, error)
}

// sink is where a Writer puts the lines it has encoded.
type sink interface {
	// write stores one line, without its newline, and returns only when it is
	// as durable as the sink makes it.
	write(line []byte) error
	close() error
}

// stepCell is the step the fixture last announced, shared by every writer of a
// backend so one announcement stamps them all.
type stepCell struct {
	mu   sync.Mutex
	step string
}

func (c *stepCell) set(s string) {
	c.mu.Lock()
	c.step = s
	c.mu.Unlock()
}

func (c *stepCell) get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.step
}

// Writer appends entries of one writer name in one process. It is safe for
// concurrent use.
type Writer struct {
	writer string
	pid    int64
	now    func() time.Time
	step   *stepCell
	sink   sink

	mu  sync.Mutex
	seq int64
}

func newWriter(writer string, pid int, now func() time.Time, step *stepCell, s sink, seq int64) (*Writer, error) {
	if !ValidWriter(writer) {
		return nil, fmt.Errorf("ledger: %q is not a writer name", writer)
	}
	if pid < 0 {
		return nil, fmt.Errorf("ledger: pid %d is negative", pid)
	}
	if now == nil {
		return nil, errors.New("ledger: a writer needs the test clock")
	}
	return &Writer{writer: writer, pid: int64(pid), now: now, step: step, sink: s, seq: seq}, nil
}

// Append writes one entry. The writer numbers it, stamps it with the test clock
// and the current step, validates it and hands it to the backend; a refused or
// failed entry consumes no sequence number.
func (w *Writer) Append(kind string, attrs Attrs) error {
	_, err := w.AppendSeq(kind, attrs)
	return err
}

// AppendSeq is Append and returns the sequence number the entry got, so that a
// writer in another process can be told which entry of this one proves an effect
// (see Attrs.RuntimeSeq).
func (w *Writer) AppendSeq(kind string, attrs Attrs) (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	e := Entry{
		V: Version, Writer: w.writer, PID: w.pid, Seq: w.seq + 1,
		AtMS: w.now().UnixMilli(), Step: w.step.get(), Kind: kind, Attrs: attrs,
	}
	line, err := Encode(e)
	if err != nil {
		return 0, fmt.Errorf("ledger: %s: %w", w.writer, err)
	}
	if err := w.sink.write(line); err != nil {
		return 0, fmt.Errorf("ledger: %s: %w", w.writer, err)
	}
	w.seq++
	return w.seq, nil
}

// Name returns the writer name.
func (w *Writer) Name() string { return w.writer }

// Close releases the backend's resources. Appending afterwards fails for a file
// backend.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sink.close()
}

func streamName(writer string, pid int64) string {
	return strings.ReplaceAll(writer, "/", "_") + "-" + strconv.FormatInt(pid, 10) + ".jsonl"
}

// --- memory ---

// Memory is the in-process backend. All its writers share one process, so it
// keeps the order in which entries arrived, which is a total order across
// writers; it does not sync because the test process is not killed.
type Memory struct {
	mu      sync.Mutex
	step    stepCell
	streams map[string]*memStream // by stream name
	order   []string
	arrived []arrival
}

type memStream struct {
	lines [][]byte
	seq   int64
}

type arrival struct {
	stream string
	index  int
}

// NewMemory returns an empty in-process ledger.
func NewMemory() *Memory { return &Memory{streams: map[string]*memStream{}} }

var _ Factory = (*Memory)(nil)

// SetStep announces the script step; entries written from now on carry it.
func (m *Memory) SetStep(step string) { m.step.set(step) }

// Open returns the writer of a stream. Opening a stream that exists continues
// its sequence: it is the same writer, restarted in the same process.
func (m *Memory) Open(writer string, pid int, now func() time.Time) (*Writer, error) {
	name := streamName(writer, int64(pid))
	m.mu.Lock()
	s, ok := m.streams[name]
	if !ok {
		s = &memStream{}
		m.streams[name] = s
		m.order = append(m.order, name)
	}
	seq := s.seq
	m.mu.Unlock()
	return newWriter(writer, pid, now, &m.step, memSink{m: m, name: name, s: s}, seq)
}

type memSink struct {
	m    *Memory
	name string
	s    *memStream
}

func (k memSink) write(line []byte) error {
	k.m.mu.Lock()
	defer k.m.mu.Unlock()
	k.s.lines = append(k.s.lines, append([]byte(nil), line...))
	k.s.seq++
	k.m.arrived = append(k.m.arrived, arrival{k.name, len(k.s.lines) - 1})
	return nil
}

func (memSink) close() error { return nil }

// Log reads everything written so far, validating it like a file ledger.
func (m *Memory) Log() (*Log, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	parsed := map[string][]Entry{}
	for _, name := range m.order {
		var data []byte
		for _, l := range m.streams[name].lines {
			data = append(append(data, l...), '\n')
		}
		entries, err := parseStream(name, data, false)
		if err != nil {
			return nil, err
		}
		parsed[name] = entries
	}
	var all []Entry
	for _, a := range m.arrived {
		all = append(all, parsed[a.stream][a.index])
	}
	return &Log{entries: all}, nil
}

// --- files ---

// Dir is the file backend: one file per writer and process, "<writer>-<pid>.jsonl"
// with the slash of the writer name replaced by an underscore, each entry synced
// before Append returns. Processes of one test share a directory; no two share a
// file.
type Dir struct {
	dir      string
	step     stepCell
	syncFile func(*os.File) error
}

// NewDir returns a file backend rooted at dir, which is created on first use.
func NewDir(dir string) *Dir { return &Dir{dir: dir, syncFile: (*os.File).Sync} }

var _ Factory = (*Dir)(nil)

// SetStep announces the script step; entries written by this process from now on
// carry it.
func (d *Dir) SetStep(step string) { d.step.set(step) }

// Open creates the file of the stream. A file that exists is an error: two
// writers would share one sequence.
func (d *Dir) Open(writer string, pid int, now func() time.Time) (*Writer, error) {
	if !ValidWriter(writer) {
		return nil, fmt.Errorf("ledger: %q is not a writer name", writer)
	}
	if err := os.MkdirAll(d.dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(d.dir, streamName(writer, int64(pid))), os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	w, err := newWriter(writer, pid, now, &d.step, &fileSink{f: f, sync: d.syncFile}, 0)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return w, nil
}

type fileSink struct {
	f    *os.File
	sync func(*os.File) error
}

func (k *fileSink) write(line []byte) error {
	if _, err := k.f.Write(append(append(make([]byte, 0, len(line)+1), line...), '\n')); err != nil {
		return err
	}
	return k.sync(k.f)
}

func (k *fileSink) close() error { return k.f.Close() }
