package store

import (
	"errors"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
)

// Errors of reading a body.
var (
	// ErrBodyMissing: the file of a body is not there.
	ErrBodyMissing = errors.New("store: body file missing")
	// ErrBodyMismatch: the content of a body file does not hash to its name.
	// The caller runs the body quarantine.
	ErrBodyMismatch = errors.New("store: body file does not match its name")
)

// Store writes and reads state/. It keeps no state of its own besides the
// writer: the engine's view of the records is built from a Scan.
type Store struct {
	w *txn.Writer
	r fsx.Reader
}

// New returns a Store over the writer of the program. The writer's table must
// hold Transactions().
func New(w *txn.Writer) *Store { return &Store{w: w, r: w.Reader()} }

// Available is the number of bytes available to this user on the volume of
// state/.
func (s *Store) Available() (uint64, error) { panic(errNotImplemented) }

// ReadBody returns the content of a body and checks it against its name. A
// damaged body is ErrBodyMismatch, a missing one ErrBodyMissing.
func (s *Store) ReadBody(ref BodyRef) ([]byte, error) { panic(errNotImplemented) }

// Scan reads and validates every file of state/ and returns what it found.
// It reads the first line and the body of every record file and nothing of
// bodies/.
func (s *Store) Scan() (*Scan, error) { panic(errNotImplemented) }
