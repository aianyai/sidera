package store

import (
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// DamagedFile is a file that failed validation and is not settled yet
// (Store.Quarantine).
type DamagedFile struct {
	Dir    string
	Name   string
	Damage Damage
	// Kind and Key are set when the damage is BodyDamaged: the kind and the
	// parsed key of the first line.
	Kind Kind
	Key  any
}

// damagedFile is the report of a file that failed validation with dmg, in the
// form the scan lists it and a write that met it returns it.
func damagedFile(dir, name string, dmg *Damage) DamagedFile {
	d := DamagedFile{Dir: dir, Name: name, Damage: *dmg}
	if dmg.Class == BodyDamaged {
		d.Kind = Kind(dmg.Kind)
		d.Key, _ = ParseKey(d.Kind, dmg.Key)
	}
	return d
}

// Scan is everything state/ holds, decoded. The store keeps no index: the
// engine builds its view from this.
type Scan struct {
	Out         []OutRecord
	OutLost     []OutLostRecord
	In          []InRecord
	Ctl         []CtlRecord
	Endpoints   []EndpointRecord
	Attempts    []AttemptsRecord
	Checkpoints []CheckpointRecord
	Admin       []AdminRecord
	Alerts      []AlertRecord
	Seq         []SeqRecord

	// Bodies are the names of the files of bodies/, none of them read.
	Bodies []string
	// Damaged lists the files that failed validation, in directory order with the
	// floor files first (see recordDirs).
	Damaged []DamagedFile
	// Unknown lists paths that are neither a record, a body nor debris of an
	// interrupted write, sorted.
	Unknown []string
	// Archive lists the names of the files of archive/.
	Archive []string
}

const recordSuffix = ".rec"

// isHashName reports whether s is 64 lowercase hex digits.
func isHashName(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// isRecordName reports whether name has the shape of the name of a record file
// of dir: a hash, and for out/ a sender before it (splitOutName).
func isRecordName(dir, name string) bool {
	if dir == OutDir {
		_, _, ok := splitOutName(name)
		return ok
	}
	h, ok := strings.CutSuffix(name, recordSuffix)
	return ok && isHashName(h)
}

// readDir lists a directory; a missing one is empty.
func (s *Store) readDir(dir string) ([]fsx.Entry, error) {
	entries, err := s.r.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return entries, err
}

// Scan reads and validates every file of state/ and returns what it found.
// It reads the first line and the body of every record file and nothing of
// bodies/.
func (s *Store) Scan() (*Scan, error) {
	sc := &Scan{}
	for _, dir := range recordDirs {
		entries, err := s.readDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if fsx.IsLeftover(e.Name) {
				continue
			}
			p := path.Join(dir, e.Name)
			switch {
			case e.IsDir:
				sc.Unknown = append(sc.Unknown, p)
			case isRecordName(dir, e.Name):
				if err := s.scanRecord(sc, dir, e.Name); err != nil {
					return nil, err
				}
			default:
				sc.Unknown = append(sc.Unknown, p)
			}
		}
	}
	entries, err := s.readDir(BodiesDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if fsx.IsLeftover(e.Name) {
			continue
		}
		switch {
		case !e.IsDir && isHashName(e.Name):
			sc.Bodies = append(sc.Bodies, e.Name)
		default:
			sc.Unknown = append(sc.Unknown, path.Join(BodiesDir, e.Name))
		}
	}
	entries, err = s.readDir(ArchiveDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !fsx.IsLeftover(e.Name) {
			sc.Archive = append(sc.Archive, e.Name)
		}
	}
	sort.Strings(sc.Unknown)
	return sc, nil
}

func (s *Store) scanRecord(sc *Scan, dir, name string) error {
	data, err := s.r.ReadFile(path.Join(dir, name))
	if err != nil {
		return err
	}
	rec, dmg := DecodeRecord(dir, name, data)
	if dmg != nil {
		sc.Damaged = append(sc.Damaged, damagedFile(dir, name, dmg))
		return nil
	}
	switch r := rec.(type) {
	case OutRecord:
		sc.Out = append(sc.Out, r)
	case OutLostRecord:
		sc.OutLost = append(sc.OutLost, r)
	case InRecord:
		sc.In = append(sc.In, r)
	case CtlRecord:
		sc.Ctl = append(sc.Ctl, r)
	case EndpointRecord:
		sc.Endpoints = append(sc.Endpoints, r)
	case AttemptsRecord:
		sc.Attempts = append(sc.Attempts, r)
	case CheckpointRecord:
		sc.Checkpoints = append(sc.Checkpoints, r)
	case AdminRecord:
		sc.Admin = append(sc.Admin, r)
	case AlertRecord:
		sc.Alerts = append(sc.Alerts, r)
	case SeqRecord:
		sc.Seq = append(sc.Seq, r)
	}
	return nil
}
