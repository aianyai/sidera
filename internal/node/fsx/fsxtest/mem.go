// Package fsxtest provides the test doubles of fsx: Mem, an in-memory file
// system that can crash, Faulty, a wrapper that fails chosen steps, and
// RunConformance, the behaviour every fsx.FS must share.
//
// # What Mem models
//
// Mem keeps, for every file, the current content and how much of it is synced,
// and for every directory the current entries and the entries synced by the
// last SyncDir. A process crash (Clone) loses nothing that a call completed. A
// power loss (CrashClone, CrashCloneWorst) keeps what was synced and chooses
// among the rest; the README of this package lists the rules. They are weaker
// than any journaling or copy-on-write file system and are not the behaviour of
// one file system, so code that survives Mem does not depend on the luck of a
// particular one.
//
// # The distribution of CrashClone
//
// CrashClone(rng) draws, in a fixed order, for the unsynced part of the state:
//
//   - Every directory name whose current binding differs from its synced one
//     keeps the current binding with probability 1/2, otherwise it reverts to
//     the synced binding (or to absent). Names are decided independently,
//     except that the two names of a rename, of a file or of a directory (and
//     every name of a chain of renames that share names), are decided together:
//     a single rename is atomic on journaling and copy-on-write file systems.
//     Different renames, and different names, stay independent. A name rebound
//     several times between two syncs of its directory shows either its synced
//     binding or its last one, never one in between.
//   - Every file with unsynced bytes keeps its full current length with
//     probability 1/2, otherwise a length drawn uniformly from the synced
//     length to the current length inclusive. The part beyond the synced length
//     is cut at 4 KiB block boundaries; each piece is, independently, the real
//     bytes with probability 1/2, otherwise zero bytes or random bytes with
//     probability 1/4 each.
//
// A file written without a SyncDir of its directory is therefore lost (the
// name reverts) with probability 1/2. A file written without a Sync, whose
// directory was synced, is cut or damaged with probability more than 1/2: it is
// intact only if the whole length survived and its single block is real, which
// is at most 3/4 x 1/2. Either bug escapes N fixed seeds of four files with
// probability below 2^(-4N), under 10^-38 for the 32 seeds the counter-example
// check uses.
//
// CrashCloneWorst draws nothing: it keeps exactly the synced state.
package fsxtest

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// node is a file or a directory.
type node struct {
	dir bool

	// File: data[:synced] is the durable content. There is no overwrite, so the
	// durable content is always a prefix of the current content.
	data   []byte
	synced int
	mode   fsx.Mode
	wide   bool // permissions wider than owner-only (SetOwnerOnly)

	// Directory: the current entries, the entries as of the last SyncDir, and the
	// renames since then.
	entries       map[string]*node
	syncedEntries map[string]*node
	moves         []move
}

// move is one rename inside a directory. The two names of a rename are decided
// together in a power loss: the rename is atomic, so it happened or it did not.
type move struct{ from, to string }

func newDir() *node {
	return &node{dir: true, entries: map[string]*node{}, syncedEntries: map[string]*node{}}
}

// Mem is an in-memory fsx.FS that distinguishes synced from unsynced state. It
// is safe for concurrent use.
type Mem struct {
	mu        sync.Mutex
	root      *node
	tempSeq   uint64
	available uint64
	locks     map[string]bool
}

var _ fsx.FS = (*Mem)(nil)

// NewMem returns an empty file system holding only the root directory ".".
func NewMem() *Mem {
	return &Mem{root: newDir(), available: 1 << 40, locks: map[string]bool{}}
}

// SetAvailable sets what Available reports, standing in for the free space of
// a volume.
func (m *Mem) SetAvailable(n uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.available = n
}

// SetOwnerOnly sets what Stat reports as OwnerOnly for path. It panics if the
// path does not exist: it is a test helper.
func (m *Mem) SetOwnerOnly(p string, ownerOnly bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, err := m.resolve("setowneronly", p)
	if err != nil {
		panic(err)
	}
	n.wide = !ownerOnly
}

// Clone returns the state a process crash leaves: every completed call
// survives, because the page cache outlives the process; open files and locks
// are gone. What was synced stays distinguished from what was not, so a power
// loss can still be simulated on the clone.
func (m *Mem) Clone() *Mem {
	m.mu.Lock()
	defer m.mu.Unlock()
	memo := map[*node]*node{}
	return &Mem{
		root:      cloneNode(m.root, memo),
		tempSeq:   m.tempSeq,
		available: m.available,
		locks:     map[string]bool{},
	}
}

func cloneNode(n *node, memo map[*node]*node) *node {
	if c, ok := memo[n]; ok {
		return c
	}
	c := &node{
		dir:    n.dir,
		data:   bytes.Clone(n.data),
		synced: n.synced,
		mode:   n.mode,
		wide:   n.wide,
		moves:  slices.Clone(n.moves),
	}
	memo[n] = c
	if n.dir {
		c.entries = cloneEntries(n.entries, memo)
		c.syncedEntries = cloneEntries(n.syncedEntries, memo)
	}
	return c
}

func cloneEntries(src map[string]*node, memo map[*node]*node) map[string]*node {
	dst := make(map[string]*node, len(src))
	for name, n := range src {
		dst[name] = cloneNode(n, memo)
	}
	return dst
}

// --- path resolution ---

func pathErr(op, p string, err error) error { return &fs.PathError{Op: op, Path: p, Err: err} }

// resolve returns the node at p.
func (m *Mem) resolve(op, p string) (*node, error) {
	if err := fsx.CheckPath(p); err != nil {
		return nil, err
	}
	n := m.root
	if p == "." {
		return n, nil
	}
	for _, name := range strings.Split(p, "/") {
		if !n.dir {
			return nil, pathErr(op, p, syscall.ENOTDIR)
		}
		c, ok := n.entries[name]
		if !ok {
			return nil, pathErr(op, p, fs.ErrNotExist)
		}
		n = c
	}
	return n, nil
}

// resolveDir returns the directory at p.
func (m *Mem) resolveDir(op, p string) (*node, error) {
	n, err := m.resolve(op, p)
	if err != nil {
		return nil, err
	}
	if !n.dir {
		return nil, pathErr(op, p, syscall.ENOTDIR)
	}
	return n, nil
}

// resolveParent returns the directory that holds p and p's name in it. p is
// not the root.
func (m *Mem) resolveParent(op, p string) (*node, string, error) {
	if err := fsx.CheckPath(p); err != nil {
		return nil, "", err
	}
	if p == "." {
		return nil, "", pathErr(op, p, fs.ErrInvalid)
	}
	dir, err := m.resolveDir(op, path.Dir(p))
	if err != nil {
		return nil, "", err
	}
	return dir, path.Base(p), nil
}

// --- Reader ---

func (m *Mem) ReadFile(p string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, err := m.resolve("read", p)
	if err != nil {
		return nil, err
	}
	if n.dir {
		return nil, pathErr("read", p, syscall.EISDIR)
	}
	return bytes.Clone(n.data), nil
}

func (m *Mem) ReadDir(dir string) ([]fsx.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.resolveDir("readdir", dir)
	if err != nil {
		return nil, err
	}
	names := sortedKeys(d.entries)
	entries := make([]fsx.Entry, len(names))
	for i, name := range names {
		entries[i] = fsx.Entry{Name: name, IsDir: d.entries[name].dir}
	}
	return entries, nil
}

func (m *Mem) Stat(p string) (fsx.Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, err := m.resolve("stat", p)
	if err != nil {
		return fsx.Info{}, err
	}
	return fsx.Info{Name: path.Base(p), Size: int64(len(n.data)), IsDir: n.dir, OwnerOnly: !n.wide}, nil
}

func (m *Mem) Available(dir string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.resolveDir("statfs", dir); err != nil {
		return 0, err
	}
	return m.available, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- files ---

// memFile is an open file. Its writes append: every file is written either
// right after it is created or by appending, never in place.
type memFile struct {
	m      *Mem
	n      *node
	name   string
	closed bool
}

func (f *memFile) Name() string { return f.name }

func (f *memFile) Write(p []byte) (int, error) {
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	if f.closed {
		return 0, pathErr("write", f.name, fs.ErrClosed)
	}
	f.n.data = append(f.n.data, p...)
	return len(p), nil
}

// Sync makes the content written so far durable. It does nothing for the
// file's directory entry: that needs SyncDir on the directory.
func (f *memFile) Sync() error {
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	if f.closed {
		return pathErr("sync", f.name, fs.ErrClosed)
	}
	f.n.synced = len(f.n.data)
	return nil
}

func (f *memFile) Close() error {
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	if f.closed {
		return pathErr("close", f.name, fs.ErrClosed)
	}
	f.closed = true
	return nil
}

func (m *Mem) CreateTemp(_ fsx.Tag, dir string, mode fsx.Mode) (fsx.File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.resolveDir("create", dir)
	if err != nil {
		return nil, err
	}
	var name string
	for {
		m.tempSeq++
		name = fmt.Sprintf("%s%016x", fsx.TempPrefix, m.tempSeq)
		if _, taken := d.entries[name]; !taken {
			break
		}
	}
	n := &node{mode: mode}
	d.entries[name] = n
	return &memFile{m: m, n: n, name: path.Join(dir, name)}, nil
}

func (m *Mem) OpenAppend(_ fsx.Tag, p string) (fsx.File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, name, err := m.resolveParent("open", p)
	if err != nil {
		return nil, err
	}
	n, ok := d.entries[name]
	switch {
	case !ok:
		n = &node{mode: fsx.ModePrivate}
		d.entries[name] = n
	case n.dir:
		return nil, pathErr("open", p, syscall.EISDIR)
	}
	return &memFile{m: m, n: n, name: p}, nil
}

// --- directory entries ---

func (m *Mem) Rename(_ fsx.Tag, from, to string) error {
	if err := fsx.CheckRename(from, to); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d, fromName, err := m.resolveParent("rename", from)
	if err != nil {
		return err
	}
	toName := path.Base(to)
	n, ok := d.entries[fromName]
	switch {
	case !ok:
		return pathErr("rename", from, fs.ErrNotExist)
	case n.dir:
		return pathErr("rename", from, syscall.EISDIR)
	}
	if target, ok := d.entries[toName]; ok && target.dir {
		return pathErr("rename", to, syscall.EISDIR)
	}
	delete(d.entries, fromName)
	d.entries[toName] = n
	d.moves = append(d.moves, move{from: fromName, to: toName})
	return nil
}

// SyncDir makes every entry operation of the directory durable. It does
// not touch the content of the files in it or the entries of its subdirectories.
func (m *Mem) SyncDir(_ fsx.Tag, dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.resolveDir("syncdir", dir)
	if err != nil {
		return err
	}
	d.syncedEntries = maps(d.entries)
	d.moves = nil
	return nil
}

func maps(src map[string]*node) map[string]*node {
	dst := make(map[string]*node, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (m *Mem) Remove(_ fsx.Tag, p string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, name, err := m.resolveParent("remove", p)
	if err != nil {
		return err
	}
	n, ok := d.entries[name]
	switch {
	case !ok:
		return pathErr("remove", p, fs.ErrNotExist)
	case n.dir:
		return pathErr("remove", p, syscall.EISDIR)
	}
	delete(d.entries, name)
	return nil
}

// RemoveAll unbinds every name of the tree, children first, each name subject
// to its own fate in a power loss. A path that
// does not exist is not an error, as with os.RemoveAll.
func (m *Mem) RemoveAll(_ fsx.Tag, dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, name, err := m.resolveParent("removeall", dir)
	if err != nil {
		return err
	}
	n, ok := d.entries[name]
	if !ok {
		return nil
	}
	unbindTree(n)
	delete(d.entries, name)
	return nil
}

func unbindTree(n *node) {
	if !n.dir {
		return
	}
	for name, c := range n.entries {
		unbindTree(c)
		delete(n.entries, name)
	}
}

func (m *Mem) Mkdir(_ fsx.Tag, dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, name, err := m.resolveParent("mkdir", dir)
	if err != nil {
		return err
	}
	if _, ok := d.entries[name]; ok {
		return pathErr("mkdir", dir, fs.ErrExist)
	}
	d.entries[name] = newDir()
	return nil
}

func (m *Mem) RenameDir(_ fsx.Tag, from, to string) error {
	if err := fsx.CheckRename(from, to); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d, fromName, err := m.resolveParent("rename", from)
	if err != nil {
		return err
	}
	toName := path.Base(to)
	n, ok := d.entries[fromName]
	switch {
	case !ok:
		return pathErr("rename", from, fs.ErrNotExist)
	case !n.dir:
		return pathErr("rename", from, syscall.ENOTDIR)
	}
	if _, ok := d.entries[toName]; ok {
		return pathErr("rename", to, fs.ErrExist)
	}
	delete(d.entries, fromName)
	d.entries[toName] = n
	d.moves = append(d.moves, move{from: fromName, to: toName})
	return nil
}

// Lock creates the lock file if it is absent and takes an exclusive lock. The
// lock is gone from every clone, as it is after the death of a process.
func (m *Mem) Lock(p string) (func() error, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, name, err := m.resolveParent("lock", p)
	if err != nil {
		return nil, err
	}
	n, ok := d.entries[name]
	switch {
	case !ok:
		d.entries[name] = &node{mode: fsx.ModePrivate}
	case n.dir:
		return nil, pathErr("lock", p, syscall.EISDIR)
	}
	if m.locks[p] {
		return nil, fsx.ErrLocked
	}
	m.locks[p] = true
	var once sync.Once
	return func() error {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			delete(m.locks, p)
		})
		return nil
	}, nil
}
