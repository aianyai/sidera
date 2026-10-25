package ledger

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Log is a ledger read back: every entry of every writer.
//
// For a Memory the entries are in the order they arrived, a total order across
// writers. For a directory they are ordered by time, then by the script step the
// fixture announced (steps in the order the fixture ran them, an entry of an
// unknown step before every known one), then writer, process and sequence. That is
// a partial order: across writers inside one step only the time is known, and on a
// test clock that stands still it says nothing. Judge a directory ledger by counts
// and by the order inside one writer.
type Log struct {
	entries []Entry
}

// Entries returns every entry in the order described on Log.
func (l *Log) Entries() []Entry { return append([]Entry(nil), l.entries...) }

// Kind returns the entries of one kind.
func (l *Log) Kind(kind string) []Entry {
	return l.Select(func(e Entry) bool { return e.Kind == kind })
}

// Writer returns the entries of one writer name, across its processes, each
// process in sequence order.
func (l *Log) Writer(name string) []Entry {
	var out []Entry
	for _, e := range l.entries {
		if e.Writer == name {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].PID != out[j].PID {
			return out[i].PID < out[j].PID
		}
		return out[i].Seq < out[j].Seq
	})
	return out
}

// Select returns the entries for which keep is true.
func (l *Log) Select(keep func(Entry) bool) []Entry {
	var out []Entry
	for _, e := range l.entries {
		if keep(e) {
			out = append(out, e)
		}
	}
	return out
}

// Count returns how many entries satisfy keep.
func (l *Log) Count(keep func(Entry) bool) int { return len(l.Select(keep)) }

// PIDs returns the ids of the processes that wrote entries, ascending, without 0,
// which stands for the test process: what a fixture ends when it cleans up.
func (l *Log) PIDs() []int64 {
	seen := map[int64]bool{}
	for _, e := range l.entries {
		if e.PID > 0 {
			seen[e.PID] = true
		}
	}
	out := make([]int64, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Steps returns the names of the steps the fixture announced, in order.
func (l *Log) Steps() []string {
	var out []string
	for _, e := range l.Writer("fixture") {
		if e.Kind == KindFixtureStep {
			out = append(out, e.Attrs.Name)
		}
	}
	return out
}

// stream is one file: the entries of one writer in one process.
type stream struct {
	name    string
	writer  string
	pid     int64
	entries []Entry
	tail    []byte // a last line without its newline, if any
}

// splitName derives the writer name and pid a stream file name stands for.
func splitName(name string) (writer string, pid int64, err error) {
	base, ok := strings.CutSuffix(name, ".jsonl")
	if !ok {
		return "", 0, fmt.Errorf("%s is not a stream file name", name)
	}
	i := strings.LastIndexByte(base, '-')
	if i < 0 {
		return "", 0, fmt.Errorf("%s is not a stream file name: no pid", name)
	}
	pid, err = strconv.ParseInt(base[i+1:], 10, 64)
	if err != nil || pid < 0 {
		return "", 0, fmt.Errorf("%s is not a stream file name: bad pid", name)
	}
	writer = base[:i]
	if writer != "fixture" {
		prefix, rest, ok := strings.Cut(writer, "_")
		if !ok {
			return "", 0, fmt.Errorf("%s is not a stream file name: no writer kind", name)
		}
		writer = prefix + "/" + rest
	}
	if !ValidWriter(writer) {
		return "", 0, fmt.Errorf("%s is not a stream file name: %q is not a writer", name, writer)
	}
	return writer, pid, nil
}

// scanStream decodes the complete lines of one file and checks each against the
// file's name; it leaves a last line without a newline in tail.
func scanStream(name string, data []byte) (stream, error) {
	writer, pid, err := splitName(name)
	if err != nil {
		return stream{}, err
	}
	s := stream{name: name, writer: writer, pid: pid}
	rest := data
	for n := 1; len(rest) > 0; n++ {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			s.tail = rest
			break
		}
		e, err := Decode(rest[:i])
		if err != nil {
			return stream{}, fmt.Errorf("%s:%d: %w", name, n, err)
		}
		if e.Writer != writer {
			return stream{}, fmt.Errorf("%s:%d: writer %s is not the file's writer %s", name, n, e.Writer, writer)
		}
		if e.PID != pid {
			return stream{}, fmt.Errorf("%s:%d: pid %d is not the file's pid %d", name, n, e.PID, pid)
		}
		if want := int64(len(s.entries)) + 1; e.Seq != want {
			return stream{}, fmt.Errorf("%s:%d: seq %d, want %d: an entry is missing or repeated", name, n, e.Seq, want)
		}
		s.entries = append(s.entries, e)
		rest = rest[i+1:]
	}
	return s, nil
}

// settle applies the one forgiveness a reader has: the last line of a process
// that was killed may be cut. A complete entry that lost only its newline is
// kept; a cut one is dropped. Without a kill the last line must be whole.
func (s stream) settle(killed bool) ([]Entry, error) {
	if s.tail == nil {
		return s.entries, nil
	}
	if !killed {
		return nil, fmt.Errorf("%s: the last line has no newline and no process was recorded as killed", s.name)
	}
	e, err := Decode(s.tail)
	if err != nil {
		return s.entries, nil
	}
	if e.Writer != s.writer || e.PID != s.pid || e.Seq != int64(len(s.entries))+1 {
		return nil, fmt.Errorf("%s: the unterminated last line is not the next entry of the stream", s.name)
	}
	return append(s.entries, e), nil
}

// parseStream reads one file's content; killed says whether the process that
// wrote it is recorded as killed.
func parseStream(name string, data []byte, killed bool) ([]Entry, error) {
	s, err := scanStream(name, data)
	if err != nil {
		return nil, err
	}
	return s.settle(killed)
}

// ReadDir reads a file ledger. Every file is validated: strict lines, the
// writer and pid of its name, sequence numbers from 1 without a gap. A process
// that the fixture recorded as killed (fixture.kill or fixture.crash) may leave
// a cut last line.
func ReadDir(dir string) (*Log, error) {
	infos, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var streams []stream
	for _, info := range infos {
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".jsonl") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, info.Name()))
		if err != nil {
			return nil, err
		}
		s, err := scanStream(info.Name(), data)
		if err != nil {
			return nil, err
		}
		streams = append(streams, s)
	}
	killed := map[int64]bool{}
	for _, s := range streams {
		for _, e := range s.entries {
			if e.Kind == KindFixtureKill || e.Kind == KindFixtureCrash {
				for _, pid := range e.Attrs.PIDs {
					killed[pid] = true
				}
			}
		}
	}
	var all []Entry
	for _, s := range streams {
		entries, err := s.settle(killed[s.pid])
		if err != nil {
			return nil, err
		}
		all = append(all, entries...)
	}
	// The fixture announces its steps in order; an entry of an earlier step comes
	// before one of a later step even when the test clock stood still between them.
	// An entry carries the name of its step and nothing else, so a name that two
	// announcements share would put the entries of the second among those of the
	// first: step names are unique, and a ledger that repeats one is refused.
	stepIndex := map[string]int{}
	for _, e := range all {
		if e.Kind == KindFixtureStep {
			if _, repeated := stepIndex[e.Attrs.Name]; repeated {
				return nil, fmt.Errorf("ledger: the fixture announced the step %q twice: step names are unique, because an entry names its step and nothing else", e.Attrs.Name)
			}
			stepIndex[e.Attrs.Name] = len(stepIndex) + 1
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		switch {
		case a.AtMS != b.AtMS:
			return a.AtMS < b.AtMS
		case stepIndex[a.Step] != stepIndex[b.Step]:
			return stepIndex[a.Step] < stepIndex[b.Step]
		case a.Writer != b.Writer:
			return a.Writer < b.Writer
		case a.PID != b.PID:
			return a.PID < b.PID
		}
		return a.Seq < b.Seq
	})
	return &Log{entries: all}, nil
}

// CheckConfirmations is the invariant that an effect is on record before it is
// confirmed: every native.create.returned that names a native session names the
// runtime's own entry for it (writer and sequence number, which the adapter had
// only once the runtime's answer carried them back), and that entry exists, is
// the creation of that session for that attempt. The two writers are separate
// processes whose clocks stand still on a test clock, so this reference, and not
// the order of the lines, is what proves the runtime wrote first.
func (l *Log) CheckConfirmations() error {
	var problems []string
	for _, e := range l.Kind(KindNativeCreateReturned) {
		if e.Attrs.NativeRef == "" {
			continue
		}
		who := fmt.Sprintf("%s seq %d (attempt %s)", e.Writer, e.Seq, e.Attrs.Attempt)
		var named *Entry
		for _, r := range l.Writer(e.Attrs.RuntimeWriter) {
			if r.Seq == e.Attrs.RuntimeSeq {
				named = &r
				break
			}
		}
		switch {
		case named == nil:
			problems = append(problems, fmt.Sprintf("%s confirmed %s, but %s has no entry %d", who, e.Attrs.NativeRef, e.Attrs.RuntimeWriter, e.Attrs.RuntimeSeq))
		case named.Kind != KindRuntimeSessionCreated:
			problems = append(problems, fmt.Sprintf("%s names %s entry %d, which is %s and not %s", who, e.Attrs.RuntimeWriter, e.Attrs.RuntimeSeq, named.Kind, KindRuntimeSessionCreated))
		case named.Attrs.Attempt != e.Attrs.Attempt || named.Attrs.NativeRef != e.Attrs.NativeRef:
			problems = append(problems, fmt.Sprintf("%s names %s entry %d, which is the creation of another attempt or session", who, e.Attrs.RuntimeWriter, e.Attrs.RuntimeSeq))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("ledger: effects confirmed before they were recorded: %s", strings.Join(problems, "; "))
}
