package fsxtest

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// Match selects the calls a rule applies to. A call is selected when every
// non-zero field matches.
//
// Steps are numbered by the Faulty's own counter, the same way fsx.Observe
// numbers them, so Hit n is the n-th time a (transaction, role, step) key is
// reached through this Faulty. Steps of reserved transactions are not numbered,
// as with fsx.Observe: a rule with a Hit never selects them. Path is compared with the path the call works
// on: for a file, the name it was opened with; for a rename, either the source
// or the target.
//
// Reader calls (ReadFile, ReadDir, Stat, Available) have no tag or step: a
// rule selects them only if it names a Path and no other field. Such a rule
// also selects the write steps on that path.
//
// Steps tagged with a reserved transaction (fsx.TxLog, fsx.TxCleanup) are
// selected only by a rule that names that transaction exactly, so a rule for
// "any remove" does not fire on failure-path cleanup.
type Match struct {
	Tx, Role string   // empty matches any
	Step     fsx.Step // zero matches any
	Hit      int      // zero matches every occurrence; n matches the n-th of that key
	Path     string   // empty matches any
}

func (m Match) selects(p fsx.Point, paths ...string) bool {
	if p.Tag.Reserved() && m.Tx != p.Tag.Tx {
		return false
	}
	if (m.Tx != "" && m.Tx != p.Tag.Tx) || (m.Role != "" && m.Role != p.Tag.Role) ||
		(m.Step != 0 && m.Step != p.Step) || (m.Hit != 0 && m.Hit != p.Hit) {
		return false
	}
	if m.Path == "" {
		return true
	}
	for _, path := range paths {
		if path == m.Path {
			return true
		}
	}
	return false
}

func (m Match) selectsRead(path string) bool {
	return m.Path != "" && m.Path == path && m.Tx == "" && m.Role == "" && m.Step == 0 && m.Hit == 0
}

// Rule is one fault. It fires on every call it selects.
type Rule struct {
	match   Match
	err     error
	written int  // for a short write: bytes written before the error
	short   bool // a short write rather than a plain failure
	hits    atomic.Int64
}

// Hits is how many calls the rule has failed so far.
func (r *Rule) Hits() int { return int(r.hits.Load()) }

func (r *Rule) String() string {
	return fmt.Sprintf("rule{tx=%q role=%q step=%v hit=%d path=%q err=%v}",
		r.match.Tx, r.match.Role, r.match.Step, r.match.Hit, r.match.Path, r.err)
}

// Faulty is an fsx.FS that fails the calls selected by its rules and passes
// every other call to the FS it wraps. If several rules select a call, the
// first one added fires.
//
// A test using Faulty fails if any rule never fired: a fault that was never
// injected proves nothing about how the code under test handles it.
type Faulty struct {
	t       testing.TB
	base    fsx.FS
	counter fsx.Counter

	mu    sync.Mutex
	rules []*Rule
}

var _ fsx.FS = (*Faulty)(nil)

// NewFaulty wraps base. At the end of the test it reports every rule that
// never fired.
func NewFaulty(t testing.TB, base fsx.FS) *Faulty {
	f := &Faulty{t: t, base: base}
	t.Cleanup(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, r := range f.rules {
			if r.Hits() == 0 {
				t.Errorf("fsxtest: %v never fired: the fault was not injected", r)
			}
		}
	})
	return f
}

// Fail makes the calls selected by m return err without running.
func (f *Faulty) Fail(m Match, err error) *Rule { return f.add(&Rule{match: m, err: err}) }

// FailShortWrite makes the writes selected by m write their first written
// bytes (or all of them, if fewer) to the file and then return err, the way a
// full disk leaves half a record. m.Step must be fsx.StepWrite.
func (f *Faulty) FailShortWrite(m Match, written int, err error) *Rule {
	f.t.Helper()
	if m.Step != fsx.StepWrite {
		f.t.Fatalf("fsxtest: FailShortWrite needs Match.Step == StepWrite, got %v", m.Step)
	}
	return f.add(&Rule{match: m, err: err, written: written, short: true})
}

func (f *Faulty) add(r *Rule) *Rule {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, r)
	return r
}

// check numbers the step and returns the rule that fails it, if any.
func (f *Faulty) check(tag fsx.Tag, step fsx.Step, paths ...string) *Rule {
	// Like fsx.Observe, number no step of a reserved transaction: its point keeps
	// ordinal zero, which no rule with an ordinal selects.
	p := fsx.Point{Key: fsx.Key{Tag: tag, Step: step}}
	if !tag.Reserved() {
		p = f.counter.Next(p.Key)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rules {
		if r.match.selects(p, paths...) {
			r.hits.Add(1)
			return r
		}
	}
	return nil
}

func (f *Faulty) checkRead(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rules {
		if r.match.selectsRead(path) {
			r.hits.Add(1)
			return r.err
		}
	}
	return nil
}

func (f *Faulty) ReadFile(p string) ([]byte, error) {
	if err := f.checkRead(p); err != nil {
		return nil, err
	}
	return f.base.ReadFile(p)
}

func (f *Faulty) ReadDir(dir string) ([]fsx.Entry, error) {
	if err := f.checkRead(dir); err != nil {
		return nil, err
	}
	return f.base.ReadDir(dir)
}

func (f *Faulty) Stat(p string) (fsx.Info, error) {
	if err := f.checkRead(p); err != nil {
		return fsx.Info{}, err
	}
	return f.base.Stat(p)
}

func (f *Faulty) Available(dir string) (uint64, error) {
	if err := f.checkRead(dir); err != nil {
		return 0, err
	}
	return f.base.Available(dir)
}

func (f *Faulty) Lock(p string) (func() error, error) { return f.base.Lock(p) }

func (f *Faulty) CreateTemp(tag fsx.Tag, dir string, mode fsx.Mode) (fsx.File, error) {
	if r := f.check(tag, fsx.StepCreate, dir); r != nil {
		return nil, r.err
	}
	file, err := f.base.CreateTemp(tag, dir, mode)
	if err != nil {
		return nil, err
	}
	return &faultyFile{File: file, f: f, tag: tag}, nil
}

func (f *Faulty) OpenAppend(tag fsx.Tag, p string) (fsx.File, error) {
	if r := f.check(tag, fsx.StepOpen, p); r != nil {
		return nil, r.err
	}
	file, err := f.base.OpenAppend(tag, p)
	if err != nil {
		return nil, err
	}
	return &faultyFile{File: file, f: f, tag: tag}, nil
}

func (f *Faulty) Rename(tag fsx.Tag, from, to string) error {
	if r := f.check(tag, fsx.StepRename, from, to); r != nil {
		return r.err
	}
	return f.base.Rename(tag, from, to)
}

func (f *Faulty) SyncDir(tag fsx.Tag, dir string) error {
	if r := f.check(tag, fsx.StepSyncDir, dir); r != nil {
		return r.err
	}
	return f.base.SyncDir(tag, dir)
}

func (f *Faulty) Remove(tag fsx.Tag, p string) error {
	if r := f.check(tag, fsx.StepRemove, p); r != nil {
		return r.err
	}
	return f.base.Remove(tag, p)
}

func (f *Faulty) RemoveAll(tag fsx.Tag, dir string) error {
	if r := f.check(tag, fsx.StepRemoveAll, dir); r != nil {
		return r.err
	}
	return f.base.RemoveAll(tag, dir)
}

func (f *Faulty) Mkdir(tag fsx.Tag, dir string) error {
	if r := f.check(tag, fsx.StepMkdir, dir); r != nil {
		return r.err
	}
	return f.base.Mkdir(tag, dir)
}

func (f *Faulty) RenameDir(tag fsx.Tag, from, to string) error {
	if r := f.check(tag, fsx.StepRenameDir, from, to); r != nil {
		return r.err
	}
	return f.base.RenameDir(tag, from, to)
}

// faultyFile attributes Write and Sync to the tag the file was opened with.
type faultyFile struct {
	fsx.File
	f   *Faulty
	tag fsx.Tag
}

func (ff *faultyFile) Write(p []byte) (int, error) {
	r := ff.f.check(ff.tag, fsx.StepWrite, ff.Name())
	if r == nil {
		return ff.File.Write(p)
	}
	if !r.short {
		return 0, r.err
	}
	n, err := ff.File.Write(p[:min(r.written, len(p))])
	if err != nil {
		return n, err
	}
	return n, r.err
}

func (ff *faultyFile) Sync() error {
	if r := ff.f.check(ff.tag, fsx.StepSync, ff.Name()); r != nil {
		return r.err
	}
	return ff.File.Sync()
}
