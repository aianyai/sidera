package fsxtest

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// Stubs: the implementation lands after the tests that specify it.

// Match selects the steps a rule applies to.
type Match struct {
	Tx, Role string   // empty matches any
	Step     fsx.Step // zero matches any
	Hit      int      // zero matches every occurrence; n matches the n-th of that key
	Path     string   // empty matches any; for Reader calls this is the only selector
}

type Rule struct{}

func (r *Rule) Hits() int { panic("not implemented") }

type Faulty struct{ fsx.FS }

func NewFaulty(t testing.TB, base fsx.FS) *Faulty                      { panic("not implemented") }
func (f *Faulty) Fail(m Match, err error) *Rule                        { panic("not implemented") }
func (f *Faulty) FailShortWrite(m Match, written int, err error) *Rule { panic("not implemented") }
