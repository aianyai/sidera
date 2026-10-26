package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
)

// Quarantine settles a damaged file in one transaction. It writes every effect
// the damage calls for first, and removes the file, or overwrites it in place,
// last. Every effect can be written again without harm, so a file that is still
// there after a crash is simply settled again by the next start: nothing records
// how far a settlement got, and nothing resumes one. What the file was is not
// kept; the alert says that it was.
//
// By the directory of the file:
//
//   - in/: the alert; then the file is removed.
//   - ctl/: the session endpoints its creation left (QuarantineSessions) are
//     closed, open ones stay open; the alert; then the file is removed. A
//     delivery derived from the record is a record of its own and stays.
//   - out/ with a first line that can be trusted (DamagedFile.Key is set): the
//     sequence floors of every pair of its key are raised above everything known;
//     the alert; then the file is overwritten with its replacement, the lost
//     shape of the sender's record, which the caller builds with the delivery
//     package's constructor.
//   - out/ with a first line that is unreadable: the alert; then the file is
//     removed. Nothing in it says what it was, so there is no replacement and no
//     floor to raise.
//   - seq/ with a first line that can be trusted: the alert; then the file is
//     overwritten with the floors its sender's source records say, which is what
//     a missing floor file amounts to. With an unreadable first line the alert,
//     then the file is removed.
//   - any other directory: the alert; then the file is removed.
type Quarantine struct {
	File DamagedFile
	// Alert is the alert of the settlement; the store gives it its id (see
	// QuarantineAlert).
	Alert QuarantineAlert
	// Replacement is the lost record to overwrite a source record with, and is
	// given for an out/ file whose first line can be trusted and for no other.
	Replacement Record
	// Scan is the scan the settlement is part of: the numbers of the other source
	// records of a pair set the sequence floor, and the session endpoints of a
	// control record are the ones QuarantineSessions finds in it. It is required
	// for a file of ctl/, out/ whose first line can be trusted, and seq/ whose
	// first line can be trusted; without it no endpoint could be found and one left
	// open would hold its slot for good.
	Scan *Scan
}

// QuarantineAlert is the alert of a settlement as its caller states it. The id is
// the store's: it is derived from the place of the damage and from the bytes that
// show it, so that a settlement repeated after a crash rewrites the alert it
// wrote and does not add a second, and a later, different damage of a file of the
// same name, a resent message taken in again, is an alert of its own that never
// replaces one the owner has already acknowledged.
type QuarantineAlert struct {
	// Kind is a name of the form [a-z][a-z0-9_]{0,63}.
	Kind    string
	At      time.Time
	Detail  string
	Subject string
}

// damageAlert is the alert record of a settlement. The id is the first 16 bytes
// of the hash of the directory and name of the file or record settled and the
// evidence of the damage: the bytes of a damaged file, or the name of the body a
// record refers to that does not hash to it.
func damageAlert(a QuarantineAlert, dir, name string, evidence []byte) AlertRecord {
	h := sha256.New()
	h.Write([]byte(dir + "\x00" + name + "\x00"))
	h.Write(evidence)
	var id AlertID
	copy(id[:], h.Sum(nil))
	return AlertRecord{ID: id, Kind: a.Kind, At: a.At, Detail: a.Detail, Subject: a.Subject}
}

// BodyQuarantine settles a record that refers to a body whose content does not
// hash to its name (ErrBodyMismatch). Only the records that refer to the body are
// affected; the body file itself is an orphan once none does.
//
//   - A record that still needs the body (OutRecord.NeedsBody, or the Record of an
//     InRecord) is lost with it: a target record is removed after the alert, and a
//     source record is overwritten with its Replacement, after the floors of its
//     pairs are raised, as for a damaged source record.
//   - Any other record keeps its state and is overwritten without the reference.
//
// Replacement is given for a source record that needs the body and for no other
// record.
type BodyQuarantine struct {
	// Record is the record as it was read: an OutRecord, an InRecord or a
	// CtlRecord that refers to the body.
	Record      Record
	Alert       QuarantineAlert
	Replacement Record
	// Scan is the scan the settlement is part of, required for a source record
	// that needs the body (see Quarantine).
	Scan *Scan
}

// settlement is what one transaction of quarantine writes, all of it encoded
// before anything is written: the effects in order, the alert, and last the one
// write that ends the settlement. A write that cannot be encoded, or a floor file
// that cannot be read (DamagedFileError), is refused before the file is touched.
type settlement struct {
	floors   *file
	sessions []file
	alert    file
	// The last write: the file overwritten in place, or the file removed.
	overwrite *file
	remove    FileRef
}

// settle writes the settlement in one transaction and commits it. Nothing is
// skipped because it seems to be there already: what a process crash left may not
// be durable, and the file that triggers the settlement must not go, or be
// overwritten, ahead of what it was settled for.
func (s *Store) settle(st *settlement) error {
	trigger := ""
	if st.remove != (FileRef{}) {
		trigger = path.Join(st.remove.Dir, st.remove.Name)
	} else if st.overwrite != nil {
		trigger = st.overwrite.path
	}
	if _, err := s.presentOrErr(trigger); err != nil {
		return err
	}
	tx := s.w.Begin(&declQuarantine)
	defer tx.Close()
	if st.floors != nil {
		if err := s.replace(tx, "seq-floor", *st.floors); err != nil {
			return err
		}
	}
	for _, f := range st.sessions {
		if err := s.replace(tx, "session", f); err != nil {
			return err
		}
	}
	if err := s.replace(tx, "alert", st.alert); err != nil {
		return err
	}
	if st.overwrite != nil {
		if err := s.replace(tx, "overwrite", *st.overwrite); err != nil {
			return err
		}
	} else {
		if err := tx.Remove("record", trigger); err != nil {
			return err
		}
		if err := tx.SyncDir("dir", st.remove.Dir); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) presentOrErr(p string) (bool, error) {
	there, err := fsx.Exists(s.r, p)
	if err == nil && !there {
		err = fmt.Errorf("store: %s is not there", p)
	}
	return there, err
}

// Quarantine settles one damaged file; see the type.
func (s *Store) Quarantine(q Quarantine) error {
	d := q.File
	if !slices.Contains(recordDirs, d.Dir) || !isRecordName(d.Dir, d.Name) {
		return fmt.Errorf("store: %s/%s is not a record file", d.Dir, d.Name)
	}
	damaged, err := s.r.ReadFile(path.Join(d.Dir, d.Name))
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("store: %s/%s is not there", d.Dir, d.Name)
	}
	if err != nil {
		return err
	}
	st := &settlement{remove: FileRef{Dir: d.Dir, Name: d.Name}}
	trusted := d.Damage.Class == BodyDamaged
	if q.Replacement != nil && !(d.Dir == OutDir && trusted) {
		return errors.New("store: only a source record whose first line can be trusted has a replacement")
	}
	switch {
	case d.Dir == OutDir && trusted:
		key, _ := d.Key.(OutKey)
		if err := s.replaceOut(st, key, d.Name, q.Replacement, q.Scan); err != nil {
			return err
		}
	case d.Dir == SeqDir && trusted:
		if q.Scan == nil {
			return errors.New("store: a floor file is rewritten from the scan that holds the source records")
		}
		sender, _ := d.Key.(ident.EndpointID)
		f, err := recordFile(SeqDir, floorsFromRecords(sender, q.Scan))
		if err != nil {
			return err
		}
		if path.Base(f.path) != d.Name {
			return fmt.Errorf("store: the floors of %s would overwrite %s/%s, which is not the file of that sender", sender, d.Dir, d.Name)
		}
		st.overwrite, st.remove = &f, FileRef{}
	case d.Dir == CtlDir:
		if q.Scan == nil {
			return errors.New("store: the quarantine of a control record is given the scan that lists its session endpoints")
		}
		if st.sessions, err = sessionFiles(q.Scan, d.Name); err != nil {
			return err
		}
	}
	if st.alert, err = recordFile(AlertsDir, damageAlert(q.Alert, d.Dir, d.Name, damaged)); err != nil {
		return err
	}
	return s.settle(st)
}

// QuarantineBody settles one record whose body is damaged; see BodyQuarantine.
// It returns the target record as written, when it was written.
func (s *Store) QuarantineBody(q BodyQuarantine) ([]InRecord, error) {
	st := &settlement{}
	var written []InRecord
	var err error
	// The record settled and the body it refers to; they identify the damage.
	var dir, name string
	var body *BodyRef
	switch r := q.Record.(type) {
	case InRecord:
		dir, name, body = InDir, r.Name(), r.Body
		if r.Body == nil {
			return nil, fmt.Errorf("store: %s refers to no body", r.Name())
		}
		if q.Replacement != nil {
			return nil, errors.New("store: a target record has no replacement")
		}
		if r.Record.NeedsBody() {
			st.remove = FileRef{Dir: InDir, Name: r.Name()}
			break
		}
		r.Body = nil
		var files []file
		if written, files, err = inFiles([]InRecord{r}); err != nil {
			return nil, err
		}
		st.overwrite = &files[0]
	case CtlRecord:
		dir, name, body = CtlDir, r.Name(), r.Body
		if r.Body == nil {
			return nil, fmt.Errorf("store: %s refers to no body", r.Name())
		}
		if q.Replacement != nil {
			return nil, errors.New("store: a control record has no replacement")
		}
		r.Body = nil
		f, err := recordFile(CtlDir, r)
		if err != nil {
			return nil, err
		}
		st.overwrite = &f
	case OutRecord:
		dir, name, body = OutDir, r.Name(), r.Body
		if r.Body == nil || r.Lost != nil {
			return nil, fmt.Errorf("store: %s refers to no body", r.Name())
		}
		if r.NeedsBody() {
			if err := s.replaceOut(st, r.Key, r.Name(), q.Replacement, q.Scan); err != nil {
				return nil, err
			}
			break
		}
		if q.Replacement != nil {
			return nil, errors.New("store: a source record that no longer needs its body has no replacement")
		}
		r.Body = nil
		f, err := recordFile(OutDir, r)
		if err != nil {
			return nil, err
		}
		st.overwrite = &f
	default:
		return nil, fmt.Errorf("store: %T is not a record that refers to a body", q.Record)
	}
	if st.alert, err = recordFile(AlertsDir, damageAlert(q.Alert, dir, name, []byte(body.Name()))); err != nil {
		return nil, err
	}
	if err := s.settle(st); err != nil {
		return nil, err
	}
	return written, nil
}

// replaceOut fills a settlement of a source record whose numbers are lost: the
// floors of every pair of its key, and the replacement that overwrites it.
func (s *Store) replaceOut(st *settlement, key OutKey, name string, replacement Record, scanned *Scan) error {
	if scanned == nil {
		return errors.New("store: a source record is replaced from the scan that holds the numbers of the others")
	}
	out, ok := replacement.(OutRecord)
	switch {
	case !ok || out.Lost == nil:
		return errors.New("store: the replacement of a source record is a lost record")
	case out.Name() != name:
		return fmt.Errorf("store: the replacement must be the record of %s named %s", OutDir, name)
	}
	f, err := recordFile(OutDir, out)
	if err != nil {
		return err
	}
	floors, err := s.raiseFloors(key, scanned)
	if err != nil {
		return err
	}
	st.floors, st.overwrite, st.remove = floors, &f, FileRef{}
	return nil
}

// sessionFiles encodes the closed session endpoints of a control record.
func sessionFiles(sc *Scan, ctlName string) ([]file, error) {
	var out []file
	for _, e := range QuarantineSessions(sc, ctlName) {
		f, err := recordFile(EndpointsDir, e)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// raiseFloors is the floor file written when a source record whose numbers are
// lost is settled. For each target of its key the floor becomes m+1: m is the
// highest of the floor on the disk now and the numbers of the source records still
// there. The lost record may have used the number after m and no other that is
// not below it, so the next number of the pair is above it. A floor is never
// lowered.
func (s *Store) raiseFloors(key OutKey, scanned *Scan) (*file, error) {
	current, err := s.readFloors(key.Sender)
	if err != nil {
		return nil, err
	}
	raises := map[ident.Address]uint64{}
	for _, to := range key.Recipients {
		m := current[to]
		for _, o := range scanned.Out {
			if n, ok := o.number(to); ok && o.Key.Sender == key.Sender && n > m {
				m = n
			}
		}
		raises[to] = m + 1
	}
	f, err := floorRecordFile(key.Sender, current, raises, nil)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// floorsFromRecords is the floor file of a sender whose own is lost: the highest
// number each of its pairs holds in the source records still there, which is
// what the sequence book makes of a missing floor file.
func floorsFromRecords(sender ident.EndpointID, sc *Scan) SeqRecord {
	rec := SeqRecord{Sender: sender, Floors: map[ident.Address]uint64{}}
	for _, o := range sc.Out {
		if o.Key.Sender != sender {
			continue
		}
		for _, to := range o.Key.Recipients {
			if n, ok := o.number(to); ok {
				rec.Floors[to] = max(rec.Floors[to], n)
			}
		}
	}
	return rec
}
