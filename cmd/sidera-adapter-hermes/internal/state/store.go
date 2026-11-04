// Package state is the adapter's record of what it has done: which card sent what
// through which endpoint to which peer, which inbound messages it has handled,
// which remote sessions it has open. The adapter writes a transition here before
// it does what the transition describes, so that after a crash it knows what it
// may already have done.
//
// The file is replaced by writing a temporary file next to it and renaming it,
// without a flush to disk. Surviving a power cut is deliberately not promised: the
// channel guarantees that a failure is reported to the sender, and the rare loss
// of the last transitions shows up there as a delivery of unknown outcome.
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const fileName = "state.json"

// Store keeps Data in a directory and serializes access to it.
type Store struct {
	dir string

	mu sync.Mutex
	// raw is the encoding of the data as last written. A view or an update starts
	// from a decoding of it, so that no caller holds a pointer into the data the
	// store keeps, and a failed update leaves nothing behind.
	raw []byte
}

// Open opens the store in dir, creating the directory (owner only) and an empty
// state when there is none.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir}
	// A crash between creating a temporary file and renaming it leaves the file
	// behind; nothing else uses the name.
	stale, _ := filepath.Glob(filepath.Join(dir, fileName+".*.tmp"))
	for _, f := range stale {
		_ = os.Remove(f)
	}
	raw, err := os.ReadFile(filepath.Join(dir, fileName))
	switch {
	case errors.Is(err, os.ErrNotExist):
		raw, err = json.Marshal(newData())
		if err != nil {
			return nil, err
		}
		if err := s.write(raw); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	}
	if _, err := decode(raw); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, fileName), err)
	}
	s.raw = raw
	return s, nil
}

func decode(raw []byte) (Data, error) {
	d := newData()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return Data{}, err
	}
	if d.Version != currentVersion {
		return Data{}, fmt.Errorf("state version %d, this adapter reads %d", d.Version, currentVersion)
	}
	return d, nil
}

// View returns a copy of the data. Changing it changes nothing in the store.
func (s *Store) View() Data {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := decode(s.raw)
	if err != nil {
		panic("state: the store holds data it wrote itself and cannot read: " + err.Error())
	}
	return d
}

// Update applies fn to a copy of the data and writes the result. The data is
// changed only when fn returns nil and the write succeeds, and Update returns
// after the write: the caller may then do what the change describes.
func (s *Store) Update(fn func(d *Data) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := decode(s.raw)
	if err != nil {
		panic("state: the store holds data it wrote itself and cannot read: " + err.Error())
	}
	if err := fn(&d); err != nil {
		return err
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if err := s.write(raw); err != nil {
		return err
	}
	s.raw = raw
	return nil
}

// write replaces the file with raw: a temporary file in the same directory, then
// a rename. The temporary file is created owner-only.
func (s *Store) write(raw []byte) (err error) {
	tmp, err := os.CreateTemp(s.dir, fileName+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(s.dir, fileName))
}
