package store

import (
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// CorruptFile is a quarantined file: `<original name>.<alert id>.corrupt`.
type CorruptFile struct {
	Dir      string
	Name     string // the full name
	Original string // the name the file had
	Alert    AlertID
	// Kind and Key are those of the first line, set only when it can be trusted:
	// it is readable, its kind is one the directory holds, and its kind and key
	// hash to Original (the rule of DecodeRecord). Key is the parsed key. A file
	// whose first line does not pass has neither, and is handled like one whose
	// first line is unreadable.
	Kind Kind
	Key  any
}

// DamagedFile is a file that failed validation and is not quarantined yet.
type DamagedFile struct {
	Dir    string
	Name   string
	Damage Damage
	// Kind and Key are set when the damage is BodyDamaged: the kind and the
	// parsed key of the first line.
	Kind Kind
	Key  any
}

// Scan is everything state/ holds, decoded. The store keeps no index: the
// engine builds its view from this.
type Scan struct {
	Out         []OutRecord
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
	// Corrupt lists every quarantined file of every directory, bodies/
	// included.
	Corrupt []CorruptFile
	// Damaged lists the files that failed validation, in directory order.
	Damaged []DamagedFile
	// Unknown lists paths that are neither a record, a body, a quarantined
	// file nor debris of an interrupted write, sorted.
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

func isRecordName(name string) bool {
	h, ok := strings.CutSuffix(name, recordSuffix)
	return ok && isHashName(h)
}

// corruptParts splits `<original>.<alert id>.corrupt` of a directory. The
// original is a record name or, in bodies/, a digest.
func corruptParts(dir, name string) (original string, id AlertID, ok bool) {
	rest, found := strings.CutSuffix(name, ".corrupt")
	if !found {
		return "", AlertID{}, false
	}
	i := strings.LastIndexByte(rest, '.')
	if i < 0 {
		return "", AlertID{}, false
	}
	original = rest[:i]
	id, err := ParseAlertID(rest[i+1:])
	if err != nil {
		return "", AlertID{}, false
	}
	if dir == BodiesDir {
		return original, id, isHashName(original)
	}
	return original, id, isRecordName(original)
}

func corruptName(original string, id AlertID) string {
	return original + "." + id.String() + ".corrupt"
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
			case isRecordName(e.Name):
				if err := s.scanRecord(sc, dir, e.Name); err != nil {
					return nil, err
				}
			default:
				if original, id, ok := corruptParts(dir, e.Name); ok {
					c, err := s.scanCorrupt(dir, e.Name, original, id)
					if err != nil {
						return nil, err
					}
					sc.Corrupt = append(sc.Corrupt, c)
				} else {
					sc.Unknown = append(sc.Unknown, p)
				}
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
		switch original, id, ok := corruptParts(BodiesDir, e.Name); {
		case !e.IsDir && isHashName(e.Name):
			sc.Bodies = append(sc.Bodies, e.Name)
		case !e.IsDir && ok:
			sc.Corrupt = append(sc.Corrupt, CorruptFile{Dir: BodiesDir, Name: e.Name, Original: original, Alert: id})
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
		d := DamagedFile{Dir: dir, Name: name, Damage: *dmg}
		if dmg.Class == BodyDamaged {
			d.Kind = Kind(dmg.Kind)
			d.Key, _ = ParseKey(d.Kind, dmg.Key)
		}
		sc.Damaged = append(sc.Damaged, d)
		return nil
	}
	switch r := rec.(type) {
	case OutRecord:
		sc.Out = append(sc.Out, r)
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

// scanCorrupt reads the first line of a quarantined file of a directory of
// records. It is trusted by the rule of a record file (readHeader), against the
// name the file had: only then are the kind and the key of the file known,
// which the sequence floor of a source record needs. A first line that
// parses but does not belong to that name says nothing about the file.
func (s *Store) scanCorrupt(dir, name, original string, id AlertID) (CorruptFile, error) {
	c := CorruptFile{Dir: dir, Name: name, Original: original, Alert: id}
	data, err := s.r.ReadFile(path.Join(dir, name))
	if err != nil {
		return c, err
	}
	if h, _, key, dmg := readHeader(dir, original, data); dmg == nil {
		c.Kind, c.Key = Kind(h.Kind), key
	}
	return c, nil
}
