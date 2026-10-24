// Package txn is the single entry point of every persistent write of keys/,
// config/ and state/.
package txn

import "github.com/aianyai/sidera/internal/node/fsx"

type Kind uint8

const (
	Normal Kind = iota + 1
	Recovery
)

type Op uint8

const (
	OpReplace Op = iota + 1
	OpReplaceDefer
	OpAppend
	OpSyncFile
	OpRenameFile
	OpRemove
	OpSyncDir
	OpMakeDir
	OpRenameDir
	OpRemoveAll
	OpDiscard
)

type Write struct {
	Role     string
	Op       Op
	Many     bool
	Optional bool
	Prepare  bool
}

type Decl struct {
	Name   string
	Kind   Kind
	Writes []Write
}

type Key struct {
	fsx.Key
	Kind Kind
}

type Table struct{}

func NewTable(decls ...*Decl) (*Table, error) { panic("unimplemented") }
func (t *Table) Keys() []Key                  { panic("unimplemented") }
func Internal() []*Decl                       { panic("unimplemented") }

type Observer interface {
	Reached(p fsx.Point, kind Kind)
}

type Count struct {
	Key  Key
	Hits int
}

type Writer struct{}

func New(fs fsx.FS, table *Table, obs Observer) *Writer { panic("unimplemented") }
func (w *Writer) Reader() fsx.Reader                    { panic("unimplemented") }
func (w *Writer) Begin(d *Decl) *Tx                     { panic("unimplemented") }
func (w *Writer) Recover() error                        { panic("unimplemented") }
func (w *Writer) Coverage() []Count                     { panic("unimplemented") }

func CleanupWork(r fsx.Reader) ([]fsx.Leftover, error) { panic("unimplemented") }

type Tx struct{}

func (t *Tx) Replace(role, path string, data []byte, mode fsx.Mode) error { panic("unimplemented") }
func (t *Tx) ReplaceDefer(role, path string, data []byte, mode fsx.Mode) error {
	panic("unimplemented")
}
func (t *Tx) Append(role, path string, data []byte) error { panic("unimplemented") }
func (t *Tx) SyncFile(role, path string) error            { panic("unimplemented") }
func (t *Tx) RenameFile(role, from, to string) error      { panic("unimplemented") }
func (t *Tx) Remove(role, path string) error              { panic("unimplemented") }
func (t *Tx) SyncDir(role, dir string) error              { panic("unimplemented") }
func (t *Tx) MakeDir(role, dir string) error              { panic("unimplemented") }
func (t *Tx) RenameDir(role, from, to string) error       { panic("unimplemented") }
func (t *Tx) RemoveAll(role, dir string) error            { panic("unimplemented") }
func (t *Tx) Discard(role, dir string) error              { panic("unimplemented") }
func (t *Tx) Commit() error                               { panic("unimplemented") }
func (t *Tx) Close()                                      { panic("unimplemented") }

type Error struct {
	Tx    string
	Point fsx.Point
	Path  string
	Class fsx.Class
	Err   error
}

func (e *Error) Error() string { panic("unimplemented") }
func (e *Error) Unwrap() error { panic("unimplemented") }

func IsFatal(err error) bool { panic("unimplemented") }
