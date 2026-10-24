package txn

import (
	"fmt"
	"sync"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// Writer is the only holder of a writable file system. Writes go through the
// Tx that Begin opens; reads through the Reader.
type Writer struct {
	base  fsx.FS // for reads
	fs    fsx.FS // base, observed: every write step passes through reach
	table *Table
	obs   Observer

	counter fsx.Counter // numbers the steps and the commit points

	mu   sync.Mutex
	hits []int          // per key of the table
	open map[*Decl]bool // declarations with an open Tx
}

// New returns a Writer over fs for the declarations of table. obs may be nil.
// Counts and ordinals belong to the Writer: one per process, and one per test.
func New(fs fsx.FS, table *Table, obs Observer) *Writer {
	w := &Writer{
		base:  fs,
		table: table,
		obs:   obs,
		hits:  make([]int, len(table.keys)),
		open:  map[*Decl]bool{},
	}
	w.fs = fsx.Observe(fs, &w.counter, w.reach)
	return w
}

// reach counts a point and tells the observer. A point whose key the table does
// not hold means a step ran that no declaration expands to; that is a defect in
// the layer, not in a caller, and it must be loud.
func (w *Writer) reach(p fsx.Point) error {
	i, ok := w.table.index[p.Key]
	if !ok {
		panic(fmt.Sprintf("txn: the step %v is not in the table: a declaration and the steps its operation runs disagree", p))
	}
	w.mu.Lock()
	w.hits[i]++
	w.mu.Unlock()
	if w.obs != nil {
		w.obs.Reached(p, w.table.keys[i].Kind)
	}
	return nil
}

// commitPoint reaches the commit point of a transaction.
func (w *Writer) commitPoint(tx string) {
	w.reach(w.counter.Next(fsx.Key{Tag: fsx.Tag{Tx: tx}, Step: fsx.StepCommit}))
}

// Reader returns the read side of the file system, and nothing else: a type
// assertion on it reaches no write method.
func (w *Writer) Reader() fsx.Reader { return reader{w.base} }

// reader has only the methods of fsx.Reader, and its field is not exported, so
// neither an assertion nor a selector gets from it to the writable file system.
type reader struct{ r fsx.Reader }

func (r reader) ReadFile(p string) ([]byte, error)       { return r.r.ReadFile(p) }
func (r reader) ReadDir(dir string) ([]fsx.Entry, error) { return r.r.ReadDir(dir) }
func (r reader) Stat(p string) (fsx.Info, error)         { return r.r.Stat(p) }
func (r reader) Available(dir string) (uint64, error)    { return r.r.Available(dir) }

// Begin opens a transaction of a declaration of the table. It panics when the
// declaration is not the very one the table was built from, or when a Tx of it
// is still open: crash points are numbered per key, and two Tx of one
// declaration advancing the same keys would make the numbering depend on
// scheduling. Declarations that differ may run at once.
func (w *Writer) Begin(d *Decl) *Tx {
	c, ok := w.table.byDecl[d]
	if !ok {
		name := "<nil>"
		if d != nil {
			name = d.Name
		}
		panic(fmt.Sprintf("txn: the declaration %q is not in the table; every transaction must be declared, and listed in the program's table", name))
	}
	return w.begin(c)
}

func (w *Writer) begin(c *compiled) *Tx {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.open[c.decl] {
		panic(fmt.Sprintf("txn: a transaction of %q is already open; end it with Commit or Close first", c.name))
	}
	w.open[c.decl] = true
	return &Tx{w: w, d: c, cur: -1, files: map[string]*appendFile{}, unsynced: map[string]bool{}}
}

func (w *Writer) release(c *compiled) {
	w.mu.Lock()
	delete(w.open, c.decl)
	w.mu.Unlock()
}

// Coverage is every key of the table with how many times this Writer reached
// it, zeros included, in the order of Table.Keys. Each key carries the kind of
// its transaction, so a caller can count the two paths separately.
func (w *Writer) Coverage() []Count {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Count, len(w.table.keys))
	for i, k := range w.table.keys {
		out[i] = Count{Key: k, Hits: w.hits[i]}
	}
	return out
}
