// Package fsxtest provides the test doubles of fsx: a crashable in-memory file
// system and a fault-injecting wrapper.
package fsxtest

import (
	"math/rand/v2"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// Stubs: the implementation lands after the tests that specify it.

type Mem struct{}

func NewMem() *Mem                                     { panic("not implemented") }
func (m *Mem) Clone() *Mem                             { panic("not implemented") }
func (m *Mem) CrashClone(rng *rand.Rand) *Mem          { panic("not implemented") }
func (m *Mem) CrashCloneWorst() *Mem                   { panic("not implemented") }
func (m *Mem) SetAvailable(n uint64)                   { panic("not implemented") }
func (m *Mem) SetOwnerOnly(path string, owner bool)    { panic("not implemented") }
func (m *Mem) ReadFile(path string) ([]byte, error)    { panic("not implemented") }
func (m *Mem) ReadDir(dir string) ([]fsx.Entry, error) { panic("not implemented") }
func (m *Mem) Stat(path string) (fsx.Info, error)      { panic("not implemented") }
func (m *Mem) Available(dir string) (uint64, error)    { panic("not implemented") }
func (m *Mem) CreateTemp(tag fsx.Tag, dir string, mode fsx.Mode) (fsx.File, error) {
	panic("not implemented")
}
func (m *Mem) OpenAppend(tag fsx.Tag, path string) (fsx.File, error) { panic("not implemented") }
func (m *Mem) Rename(tag fsx.Tag, from, to string) error             { panic("not implemented") }
func (m *Mem) SyncDir(tag fsx.Tag, dir string) error                 { panic("not implemented") }
func (m *Mem) Remove(tag fsx.Tag, path string) error                 { panic("not implemented") }
func (m *Mem) RemoveAll(tag fsx.Tag, dir string) error               { panic("not implemented") }
func (m *Mem) Mkdir(tag fsx.Tag, dir string) error                   { panic("not implemented") }
func (m *Mem) RenameDir(tag fsx.Tag, from, to string) error          { panic("not implemented") }
func (m *Mem) Lock(path string) (func() error, error)                { panic("not implemented") }

var _ fsx.FS = (*Mem)(nil)
