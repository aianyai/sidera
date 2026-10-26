package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"
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
//   - out/ with a first line that is unreadable, whose name names an endpoint of
//     this node (open or closed): the floors of every pair of that endpoint are
//     raised above everything known, for nothing says which pair the lost message
//     belonged to; the out-lost record that tells the endpoint is written, named
//     after the damaged file so that a repetition writes the same one; the alert;
//     then the file is removed. There is no replacement: nothing says what it was.
//   - out/ with a first line that is unreadable and a name that names no such
//     endpoint: the alert; then the file is removed. Nobody can be told.
//   - an out-lost record that is damaged: the same notice again, written over it,
//     when its endpoint is on this node, for it is the one place a notice could
//     vanish without a trace; otherwise the alert, then the file is removed.
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
	// records of a pair set the sequence floor, the session endpoints of a
	// control record are the ones QuarantineSessions finds in it, and the endpoint
	// a source file was sent by is looked up in it. It is required for a file of
	// ctl/, of out/ (whatever the damage, since the name of one that cannot be read
	// is looked up), and seq/ whose first line can be trusted; without it no
	// endpoint could be found and one left open would hold its slot for good.
	Scan *Scan
	// Node is this node, whose address the feedback item of an out-lost record
	// carries. It is required when the settlement writes one, which the store
	// decides from the file and the scan and a caller cannot know: a caller always
	// gives it for a file of out/. The discovery time of the record is the time of
	// the alert.
	Node ident.NodeID
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
	// lost is the out-lost record that tells the sender of a source file that
	// could not be read.
	lost  *file
	alert file
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
	if st.lost != nil {
		if err := s.replace(tx, "out-lost", *st.lost); err != nil {
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
	_, isSource := d.Key.(OutKey)
	if q.Replacement != nil && !(d.Dir == OutDir && trusted && isSource) {
		return errors.New("store: only a source record whose first line can be trusted has a replacement")
	}
	switch {
	case d.Dir == OutDir && trusted && isSource:
		key, _ := d.Key.(OutKey)
		if err := s.replaceOut(st, key, d.Name, q.Replacement, q.Scan); err != nil {
			return err
		}
	case d.Dir == OutDir && trusted:
		key, _ := d.Key.(OutLostKey)
		if err := s.renewOutLost(st, key, q); err != nil {
			return err
		}
	case d.Dir == OutDir:
		if err := s.tellSender(st, d.Name, q); err != nil {
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
	floors, err := s.raiseFloors(key.Sender, key.Recipients, scanned)
	if err != nil {
		return err
	}
	st.floors, st.overwrite, st.remove = floors, &f, FileRef{}
	return nil
}

// senderOnThisNode reports whether e is an endpoint of this node that a source
// file can be the message of: open or closed, not one whose creation has not
// ended (it has sent nothing) and not the system endpoint (it sends its notices
// as target records, never from out/).
func senderOnThisNode(sc *Scan, e ident.EndpointID) bool {
	return slices.ContainsFunc(sc.Endpoints, func(r EndpointRecord) bool {
		return r.ID == e && (r.Status == EndpointOpen || r.Status == EndpointClosed)
	})
}

// tellSender fills the settlement of a source file whose first line cannot be
// read. Nothing in it says what it was, but its name says whose it was; when that
// is an endpoint of this node the settlement writes the out-lost record that
// tells the endpoint, and raises every floor of the endpoint, since the recipients
// of the lost message are unknown. In any other case nobody can be told and the
// settlement is the alert and the removal of the file.
func (s *Store) tellSender(st *settlement, name string, q Quarantine) error {
	sender, hash, _ := splitOutName(name)
	e, err := ident.ParseEndpointID(sender)
	if err != nil {
		return nil
	}
	if q.Scan == nil {
		return errors.New("store: a source file whose first line is unreadable is settled with the scan that tells whether its sender is an endpoint of this node")
	}
	if !senderOnThisNode(q.Scan, e) {
		return nil
	}
	if q.Node.IsZero() {
		return errors.New("store: the notice of a lost source file needs the node whose address its feedback carries")
	}
	rec := NewOutLostRecord(OutLostKey{Sender: e, Damaged: hash}, q.Node, q.Alert.At)
	f, err := recordFile(OutDir, rec)
	if err != nil {
		return err
	}
	pairs, err := s.knownPairs(e, q.Scan)
	if err != nil {
		return err
	}
	floors, err := s.raiseFloors(e, pairs, q.Scan)
	if err != nil {
		return err
	}
	st.lost, st.floors = &f, floors
	return nil
}

// renewOutLost fills the settlement of an out-lost record whose body is damaged:
// the record is written again over itself with its item pending, when its
// endpoint is on this node. The damaged one said nothing but that the endpoint
// is to be told, and whether it had been told is lost with it, so the endpoint
// may hear it twice and never not at all. No floor is raised: the record names no
// message. When the endpoint is gone nobody can be told, and the file is removed.
func (s *Store) renewOutLost(st *settlement, key OutLostKey, q Quarantine) error {
	if q.Scan == nil {
		return errors.New("store: an out-lost record is settled with the scan that tells whether its endpoint is on this node")
	}
	if !senderOnThisNode(q.Scan, key.Sender) {
		return nil
	}
	if q.Node.IsZero() {
		return errors.New("store: the notice of a lost source file needs the node whose address its feedback carries")
	}
	f, err := recordFile(OutDir, NewOutLostRecord(key, q.Node, q.Alert.At))
	if err != nil {
		return err
	}
	st.overwrite, st.remove = &f, FileRef{}
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
// lost is settled. For each of the targets given the floor becomes m+1: m is the
// highest of the floor on the disk now and the numbers of the source records still
// there. The lost record may have used the number after m and no other that is
// not below it, so the next number of the pair is above it. A floor is never
// lowered. Where there is nothing to raise there is no file to write.
func (s *Store) raiseFloors(sender ident.EndpointID, targets []ident.Address, scanned *Scan) (*file, error) {
	current, err := s.readFloors(sender)
	if err != nil {
		return nil, err
	}
	raises := map[ident.Address]uint64{}
	for _, to := range targets {
		m := current[to]
		for _, o := range scanned.Out {
			if n, ok := o.number(to); ok && o.Key.Sender == sender && n > m {
				m = n
			}
		}
		raises[to] = m + 1
	}
	if len(raises) == 0 {
		return nil, nil
	}
	f, err := floorRecordFile(sender, current, raises, nil)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// knownPairs lists every target the sender is known to have sent to, which is
// all that is left of it when a message of unknown targets is lost.
func (s *Store) knownPairs(sender ident.EndpointID, scanned *Scan) ([]ident.Address, error) {
	current, err := s.readFloors(sender)
	if err != nil {
		return nil, err
	}
	return senderPairs(scanned, sender, current), nil
}

// senderPairs lists the targets a sender is known to have sent to: those its
// floor file holds (floors) and those of its source records that have a
// sequence number.
func senderPairs(sc *Scan, sender ident.EndpointID, floors map[ident.Address]uint64) []ident.Address {
	seen := map[ident.Address]bool{}
	var out []ident.Address
	add := func(to ident.Address) {
		if !seen[to] {
			seen[to] = true
			out = append(out, to)
		}
	}
	for to := range floors {
		add(to)
	}
	for _, o := range sc.Out {
		if o.Key.Sender != sender {
			continue
		}
		for _, to := range o.Key.Recipients {
			if _, ok := o.number(to); ok {
				add(to)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
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
