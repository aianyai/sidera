//go:build !darwin

package proc

import "fmt"

// Observing, starting and signalling processes is implemented for macOS only
// so far. Every operation fails with ErrUnsupported and says which platform:
// the adapter must not guess about processes it cannot see.

type unsupported struct{}

func errUnsupported(op string) error { return fmt.Errorf("%w: %s", ErrUnsupported, op) }

// NewTable returns a table that fails every call.
func NewTable() Table { return unsupported{} }

// NewLauncher returns a launcher that fails every call.
func NewLauncher(Table) Launcher { return unsupported{} }

// NewSignaller returns a signaller that fails every call.
func NewSignaller() Signaller { return unsupported{} }

func (unsupported) Lookup(int) (Info, error)    { return Info{}, errUnsupported("process lookup") }
func (unsupported) Snapshot() (Snapshot, error) { return nil, errUnsupported("process listing") }
func (unsupported) Start(Launch) (Started, error) {
	return Started{}, errUnsupported("starting a detached process")
}
func (unsupported) Interrupt(int) error { return errUnsupported("interrupting a process") }
