package store

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"path"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
)

// Errors of reading a body.
var (
	// ErrBodyMissing: the file of a body is not there.
	ErrBodyMissing = errors.New("store: body file missing")
	// ErrBodyMismatch: the content of a body file does not hash to its name.
	// The caller settles each record that refers to it (QuarantineBody).
	ErrBodyMismatch = errors.New("store: body file does not match its name")
)

// ErrReleasedBodyInUse: a write was asked to remove a body that a record the same
// write stores refers to. Nothing was written.
var ErrReleasedBodyInUse = errors.New("store: a released body is referred to by a record written with it")

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
// state/. When state/ is missing it is the volume of the data directory, which
// is the volume state/ will be made on.
func (s *Store) Available() (uint64, error) {
	n, err := s.r.Available(StateDir)
	if errors.Is(err, fs.ErrNotExist) {
		return s.r.Available(".")
	}
	return n, err
}

// ReadBody returns the content of a body and checks it against its name. A
// damaged body is ErrBodyMismatch, a missing one ErrBodyMissing.
func (s *Store) ReadBody(ref BodyRef) ([]byte, error) {
	data, err := s.r.ReadFile(path.Join(BodiesDir, ref.Name()))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrBodyMissing, ref.Name())
	}
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(data); !bytes.Equal(sum[:], ref.Digest[:]) || len(data) != ref.Len {
		return nil, fmt.Errorf("%w: %s", ErrBodyMismatch, ref.Name())
	}
	return data, nil
}
