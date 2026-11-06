package adaptersetup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// RecordVersion is the version of the record file. A record of a later version is
// refused rather than read as if it were this one.
const RecordVersion = 1

// RecordItem is what setup remembers of one item it did.
type RecordItem struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Target string `json:"target"`
	// Digest is the Digest of the content setup wrote, or of what it registered,
	// so that a later check can tell its own work from someone else's change.
	Digest string `json:"digest"`
	// Version is the adapter version that wrote the item.
	Version string `json:"version"`
}

// Record is what an adapter's setup has done for one instance. It is the adapter's
// alone: the core CLI and the daemon never read it, and a refresh finds in it what it
// may touch: only recorded items that still exist. The members are the ones below; a
// member this type does not know is dropped when the record is rewritten, so an adapter
// that needs more keeps it in a file of its own.
type Record struct {
	Version int `json:"version"`
	// AdapterVersion is the version of the adapter that wrote the record last.
	AdapterVersion string `json:"adapter_version"`
	// SideraProgram is the sidera program the items name, from the last request that
	// wrote them; a refresh that finds another one has paths to rewrite.
	SideraProgram string       `json:"sidera_program"`
	Items         []RecordItem `json:"items"`
	// Declined are the ids of items the person was asked about and said no to. Handle
	// keeps it (see Request.Declined): an adapter reads the record, changes Items and
	// writes it back, and never edits Declined itself. An id is never both here and in
	// Items.
	Declined []string `json:"declined,omitempty"`
}

// RecordPath is where the record of an instance is: <stateDir>/setup/record.json.
func RecordPath(stateDir string) string {
	return filepath.Join(stateDir, "setup", "record.json")
}

// ReadRecord reads the record of the instance. An instance with no record, a
// state directory that does not exist included, gives (nil, nil).
func ReadRecord(stateDir string) (*Record, error) {
	data, err := os.ReadFile(RecordPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("the setup record %s is not valid: %w", RecordPath(stateDir), err)
	}
	if r.Version != RecordVersion {
		return nil, fmt.Errorf("the setup record %s is of version %d, and this adapter reads version %d", RecordPath(stateDir), r.Version, RecordVersion)
	}
	return &r, nil
}

// WriteRecord writes the record, for the owner alone and atomically, and refuses
// an instance whose state directory does not exist.
func WriteRecord(stateDir string, r Record) error {
	if err := RequireStateDir(stateDir); err != nil {
		return err
	}
	r.Version = RecordVersion
	if r.Items == nil {
		r.Items = []RecordItem{}
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return WritePrivateFile(RecordPath(stateDir), append(data, '\n'))
}

// RemoveRecord deletes the record, as the last undo does; a record that is not
// there is not an error.
func RemoveRecord(stateDir string) error {
	if err := os.Remove(RecordPath(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Installed says whether the record holds an item setup did. A record that holds only
// declines (the person said no to everything they were asked) records nothing that
// was done, and an adapter answers for it as for no record: check and refresh say
// `recorded: false`, which is also what lets `adapter remove` go ahead. Safe on nil.
func (r *Record) Installed() bool { return r != nil && len(r.Items) > 0 }

// IsDeclined says whether the person declined the item. Safe on nil.
func (r *Record) IsDeclined(id string) bool { return r != nil && slices.Contains(r.Declined, id) }

// Find returns the recorded item with the id.
func (r *Record) Find(id string) (RecordItem, bool) {
	for _, it := range r.Items {
		if it.ID == id {
			return it, true
		}
	}
	return RecordItem{}, false
}

// Set adds the item, or replaces the recorded one with its id.
func (r *Record) Set(item RecordItem) {
	for i := range r.Items {
		if r.Items[i].ID == item.ID {
			r.Items[i] = item
			return
		}
	}
	r.Items = append(r.Items, item)
}

// Drop removes the item with the id.
func (r *Record) Drop(id string) {
	kept := r.Items[:0]
	for _, it := range r.Items {
		if it.ID != id {
			kept = append(kept, it)
		}
	}
	r.Items = kept
}
