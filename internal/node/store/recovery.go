package store

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/recfile"
	"github.com/aianyai/sidera/internal/node/txn"
)

// Recovery rules. Each rule has a predicate, a pure function of a Scan that
// names the work, and a method that does it. The predicate is the one thing the
// rule and the check "a run without a crash or damage has no recovery work" both
// call, so the check cannot drift from the rule. The predicates of the rules
// that need the delivery rules to decide what to write (draining records of a
// closed endpoint, applying the event of a prompt or a block) return the
// candidates and the engine applies the transition.

// MissingStateDirs lists the directories of state/ that do not exist, parent
// first: the work of the layout rule.
func MissingStateDirs(r fsx.Reader) ([]string, error) {
	var out []string
	for _, d := range StateDirs() {
		if _, err := r.Stat(d); errors.Is(err, fs.ErrNotExist) {
			out = append(out, d)
		} else if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// OrphanBodies is the work of orphan-gc: the bodies no record refers to. A
// record refers to the body it holds a reference to; a quarantined file refers
// to nothing.
func OrphanBodies(sc *Scan) []string {
	refs := map[string]bool{}
	for _, o := range sc.Out {
		if o.Body != nil {
			refs[o.Body.Name()] = true
		}
	}
	for _, in := range sc.In {
		if in.Body != nil {
			refs[in.Body.Name()] = true
		}
	}
	for _, c := range sc.Ctl {
		if c.Body != nil {
			refs[c.Body.Name()] = true
		}
	}
	var out []string
	for _, b := range sc.Bodies {
		if !refs[b] {
			out = append(out, b)
		}
	}
	return out
}

// OrphanEndpoints is the work of orphan-gc-endpoint: the session endpoints
// still being created whose control record is neither there nor quarantined. The
// endpoint is preparation that never became part of a committed control record.
func OrphanEndpoints(sc *Scan) []EndpointRecord {
	exists := map[string]bool{}
	for _, c := range sc.Ctl {
		exists[c.Name()] = true
	}
	for _, c := range sc.Corrupt {
		if c.Dir == CtlDir {
			exists[c.Original] = true
		}
	}
	for _, d := range sc.Damaged {
		if d.Dir == CtlDir {
			exists[d.Name] = true
		}
	}
	var out []EndpointRecord
	for _, e := range sc.Endpoints {
		if e.Status == EndpointCreating && (e.CreatedBy == "" || !exists[e.CreatedBy+recordSuffix]) {
			out = append(out, e)
		}
	}
	return out
}

func alertIDs(sc *Scan) map[AlertID]bool {
	ids := map[AlertID]bool{}
	for _, a := range sc.Alerts {
		ids[a.ID] = true
	}
	return ids
}

// UnfinishedQuarantines is the work of quarantine-resume: the quarantined files
// of the directories of records whose alert does not exist. The alert is the
// last write of a quarantine, so a quarantine is finished exactly when it is
// there.
func UnfinishedQuarantines(sc *Scan) []CorruptFile { return unfinished(sc, false) }

// UnfinishedBodyQuarantines is the work of the continuation of quarantine-body:
// the quarantined bodies whose alert does not exist.
func UnfinishedBodyQuarantines(sc *Scan) []CorruptFile { return unfinished(sc, true) }

func unfinished(sc *Scan, bodies bool) []CorruptFile {
	ids := alertIDs(sc)
	var out []CorruptFile
	for _, c := range sc.Corrupt {
		if (c.Dir == BodiesDir) == bodies && !ids[c.Alert] {
			out = append(out, c)
		}
	}
	return out
}

// EffectReplays is the work of ctl-effect-replay, as the endpoint records to
// write: a session endpoint still being created whose control record holds the
// end of the creation becomes open or closed accordingly, and an open session
// endpoint a control record closed becomes closed. Nothing is ever moved from
// closed to open.
func EffectReplays(sc *Scan) []EndpointRecord {
	ctls := map[string]*CtlRecord{}
	closedBy := map[ident.EndpointID]bool{}
	for i := range sc.Ctl {
		c := &sc.Ctl[i]
		ctls[c.Name()] = c
		if !c.ClosedEndpoint.IsZero() {
			closedBy[c.ClosedEndpoint] = true
		}
	}
	var out []EndpointRecord
	for _, e := range sc.Endpoints {
		switch {
		case e.Status == EndpointCreating && e.CreatedBy != "":
			c := ctls[e.CreatedBy+recordSuffix]
			if c == nil || c.Intent == nil || c.Intent.Endpoint != e.ID || c.Intent.State == IntentCreating {
				continue
			}
			n := e
			if c.Intent.State == IntentCreated {
				n.Status = EndpointOpen
				sess := SessionInfo{}
				if e.Session != nil {
					sess = *e.Session
				}
				sess.Preset, sess.NativeRef, sess.Process = c.Preset, c.Intent.NativeRef, c.Intent.Process
				n.Session = &sess
			} else {
				n.Status, n.Token = EndpointClosed, nil
			}
			out = append(out, n)
		case e.Status == EndpointOpen && closedBy[e.ID]:
			n := e
			n.Status, n.Token = EndpointClosed, nil
			out = append(out, n)
		}
	}
	return out
}

// DrainCandidates is the structural part of the work of endpoint-drain-replay:
// the target and control records still accepted, not lost, whose endpoint is
// closed or has no record. The engine decides, with the delivery rules, which
// of them a forced close changes.
func DrainCandidates(sc *Scan) (in []InRecord, ctl []CtlRecord) {
	alive := map[ident.EndpointID]bool{}
	for _, e := range sc.Endpoints {
		if e.Status != EndpointClosed {
			alive[e.ID] = true
		}
	}
	for _, r := range sc.In {
		if !r.Lost && r.Record.State == delivery.StateAccepted && !alive[r.Key.Recipient] {
			in = append(in, r)
		}
	}
	for _, r := range sc.Ctl {
		if !r.Lost && r.Record.State == delivery.StateAccepted && !alive[r.Key.Recipient] {
			ctl = append(ctl, r)
		}
	}
	return in, ctl
}

// InstanceDrainWork is the work of instance-drain-replay: the endpoints, open or
// being created, of instances for which enabled is false, as closed records,
// token removed.
func InstanceDrainWork(sc *Scan, enabled func(instance string) bool) []EndpointRecord {
	var out []EndpointRecord
	for _, e := range sc.Endpoints {
		if e.Status != EndpointClosed && !enabled(e.Instance) {
			n := e
			n.Status, n.Token = EndpointClosed, nil
			out = append(out, n)
		}
	}
	return out
}

// PendingWork is one head record with the followers it still owes: the target
// records that exist, were not written since the head, and are the same record.
type PendingWork struct {
	Head      AttemptsRecord
	Followers []InRecord
}

func pendingWork(sc *Scan, events ...PendingEvent) []PendingWork {
	byKey := map[TargetKey]*InRecord{}
	for i := range sc.In {
		byKey[sc.In[i].Key] = &sc.In[i]
	}
	var out []PendingWork
	for _, a := range sc.Attempts {
		if !slices.Contains(events, a.Pending.Event) {
			continue
		}
		w := PendingWork{Head: a}
		for _, it := range a.Pending.Items {
			if r := byKey[it.Key]; r != nil && !r.Lost && r.Rev == it.Rev && r.AcceptedAt.Equal(it.AcceptedAt) {
				w.Followers = append(w.Followers, *r)
			}
		}
		if len(w.Followers) > 0 {
			out = append(out, w)
		}
	}
	return out
}

// PromptReplays is the work of prompt-replay.
func PromptReplays(sc *Scan) []PendingWork { return pendingWork(sc, PendingNotified) }

// BlockedReplays is the work of blocked-replay: the pending lists of blocks and
// of recoveries.
func BlockedReplays(sc *Scan) []PendingWork {
	return pendingWork(sc, PendingBlocked, PendingUnblocked)
}

// FeedbackReplays is the work of feedback-replay, as the source records to
// write: those whose pending feedback holds items a notice already covers,
// without those items.
func FeedbackReplays(sc *Scan) []OutRecord {
	idx := map[string]int{}
	for i, o := range sc.Out {
		idx[o.Name()] = i
	}
	next := map[int][]delivery.Item{}
	for _, n := range sc.In {
		if n.Lost {
			continue
		}
		for _, c := range n.Covers {
			i, ok := idx[c.Out]
			if !ok {
				continue
			}
			items, seen := next[i]
			if !seen {
				items = slices.Clone(sc.Out[i].Feedback)
			}
			next[i] = slices.DeleteFunc(items, func(it delivery.Item) bool {
				return it.Ref.Recipient == c.Recipient && it.Feedback.Kind == c.Kind
			})
		}
	}
	var out []OutRecord
	for i, items := range next {
		if len(items) != len(sc.Out[i].Feedback) {
			o := sc.Out[i]
			o.Feedback = items
			out = append(out, o)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name() < out[b].Name() })
	return out
}

// Quarantine is the quarantine of one damaged file.
type Quarantine struct {
	File DamagedFile
	// Alert is the alert of the quarantine, its ID chosen before and made part
	// of the quarantined name.
	Alert AlertRecord
	// Replacement is the record to put under the original name, for a damaged
	// delivery record whose key can be trusted; nil otherwise.
	Replacement Record
	// Session is the closed record of the session endpoint still being created
	// whose control record this file was; nil otherwise.
	Session *EndpointRecord
	// Scan is the scan the quarantine is part of: the other source records of a
	// pair set the sequence floor.
	Scan *Scan
}

// QuarantineResume finishes a quarantine whose alert is missing. It writes what
// the first pass wrote, in the same order and under the same names: the floors
// raised once more, the replacement, the closed session endpoint, and the alert
// last. Nothing is skipped because it seems to be there: it may not be durable.
// The replacement has the name the damaged file had, so a resumption rewrites
// that record and makes no second one; the caller builds it by the same rule as
// the first pass, and builds none for a file whose first line is not trusted
// (CorruptFile.Key is nil).
type QuarantineResume struct {
	File        CorruptFile
	Alert       AlertRecord
	Replacement Record
	Session     *EndpointRecord
	Scan        *Scan
}

// pendingLost lists the keys of the quarantined source records of the disk,
// whose alert does not exist yet, but for the file named except.
func (s *Store) pendingLost(except string) ([]OutKey, error) {
	entries, err := s.readDir(OutDir)
	if err != nil {
		return nil, err
	}
	var out []OutKey
	for _, e := range entries {
		original, id, ok := corruptParts(OutDir, e.Name)
		if !ok || original == except {
			continue
		}
		there, err := s.present(path.Join(AlertsDir, recfile.FileName(string(KindAlert), AlertKeyBytes(id))))
		if err != nil {
			return nil, err
		}
		if there {
			continue
		}
		c, err := s.scanCorrupt(OutDir, e.Name, original, id)
		if err != nil {
			return nil, err
		}
		if k, ok := c.Key.(OutKey); ok {
			out = append(out, k)
		}
	}
	return out, nil
}

// raiseFloors is the floor file written when a source record whose numbers are
// lost is quarantined. For each target of its key the floor becomes m+k: m is
// the highest of the floor on the disk now and the numbers of the source records
// still there, k the number of records of the pair whose numbers are lost and
// not counted in the floor yet (this one, and the quarantines not finished).
// Numbers above m belong to lost records and are at most k.
func (s *Store) raiseFloors(key OutKey, scanned *Scan, self string) (*file, error) {
	pending, err := s.pendingLost(self)
	if err != nil {
		return nil, err
	}
	current, err := s.readFloors(key.Sender)
	if err != nil {
		return nil, err
	}
	raises := map[ident.Address]uint64{}
	for _, to := range key.Recipients {
		m := current[to]
		if scanned != nil {
			for _, o := range scanned.Out {
				if n, ok := o.number(to); ok && o.Key.Sender == key.Sender && n > m {
					m = n
				}
			}
		}
		k := uint64(1)
		for _, p := range pending {
			if p.Sender == key.Sender && slices.Contains(p.Recipients, to) {
				k++
			}
		}
		raises[to] = m + k
	}
	f, err := floorRecordFile(key.Sender, current, raises, nil)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// replacementFile encodes the replacement of a damaged delivery record: a
// record of its directory that has the name the damaged file had.
func replacementFile(dir, name string, rec Record) (*file, error) {
	if rec == nil {
		return nil, nil
	}
	if d := dirOfRecord(rec); d != dir || rec.Name() != name {
		return nil, fmt.Errorf("store: the replacement must be a record of %s named %s", dir, name)
	}
	f, err := recordFile(dir, rec)
	if err != nil {
		return nil, err
	}
	switch r := rec.(type) {
	case OutRecord:
		if r.Lost == nil {
			return nil, errors.New("store: a replacement is a lost record")
		}
	case InRecord:
		if !r.Lost {
			return nil, errors.New("store: a replacement is a lost record")
		}
	case CtlRecord:
		if !r.Lost {
			return nil, errors.New("store: a replacement is a lost record")
		}
	default:
		return nil, errors.New("store: only a delivery record has a replacement")
	}
	return &f, nil
}

func dirOfRecord(rec Record) string {
	switch rec.(type) {
	case OutRecord:
		return OutDir
	case InRecord:
		return InDir
	case CtlRecord:
		return CtlDir
	}
	return ""
}

// tail is what follows the rename in a quarantine and in its resumption, all of
// it encoded: the floors, the replacement, the closed session, the alert.
type tail struct {
	floors, replacement, session *file
	alert                        file
}

// prepareTail works out and encodes the tail before anything is written, so that
// what cannot be written, or read (a damaged floor file, DamagedFileError), is
// refused before the file is renamed.
func (s *Store) prepareTail(dir, name string, key any, scanned *Scan, replacement Record, session *EndpointRecord, alert AlertRecord) (*tail, error) {
	t := &tail{}
	if k, isOut := key.(OutKey); isOut && dir == OutDir {
		f, err := s.raiseFloors(k, scanned, name)
		if err != nil {
			return nil, err
		}
		t.floors = f
	}
	rf, err := replacementFile(dir, name, replacement)
	if err != nil {
		return nil, err
	}
	t.replacement = rf
	if session != nil {
		if session.Status != EndpointClosed {
			return nil, errors.New("store: the session endpoint of a quarantine is closed")
		}
		f, err := recordFile(EndpointsDir, *session)
		if err != nil {
			return nil, err
		}
		t.session = &f
	}
	if t.alert, err = recordFile(AlertsDir, alert); err != nil {
		return nil, err
	}
	return t, nil
}

// write writes the tail, the alert last, and commits. Nothing is skipped because
// it seems to be there already: what a process crash left may not be durable,
// and the alert, which says the quarantine is finished, must not become durable
// ahead of the rest.
func (t *tail) write(s *Store, tx *txn.Tx) error {
	if t.floors != nil {
		if err := s.replace(tx, "seq-floor", *t.floors); err != nil {
			return err
		}
	}
	if t.replacement != nil {
		if err := s.replace(tx, "replacement", *t.replacement); err != nil {
			return err
		}
	}
	if t.session != nil {
		if err := s.replace(tx, "session", *t.session); err != nil {
			return err
		}
	}
	if err := s.replace(tx, "alert", t.alert); err != nil {
		return err
	}
	return tx.Commit()
}

// Quarantine renames the damaged file, raises the sequence floors of a source
// record whose numbers are lost, puts the replacement, closes the session
// endpoint, and writes the alert last.
func (s *Store) Quarantine(q Quarantine) error {
	d := q.File
	if !slices.Contains(recordDirs, d.Dir) || !isRecordName(d.Name) {
		return fmt.Errorf("store: %s/%s is not a record file", d.Dir, d.Name)
	}
	if q.Alert.ID == (AlertID{}) {
		return errors.New("store: a quarantine has an alert with an id")
	}
	var key any
	if d.Damage.Class == BodyDamaged {
		key = d.Key
	}
	if _, err := s.presentOrErr(path.Join(d.Dir, d.Name)); err != nil {
		return err
	}
	// Everything is encoded before the file is renamed.
	t, err := s.prepareTail(d.Dir, d.Name, key, q.Scan, q.Replacement, q.Session, q.Alert)
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declQuarantine)
	defer tx.Close()
	if err := tx.RenameFile("record", path.Join(d.Dir, d.Name), path.Join(d.Dir, corruptName(d.Name, q.Alert.ID))); err != nil {
		return err
	}
	return t.write(s, tx)
}

func (s *Store) presentOrErr(p string) (bool, error) {
	there, err := s.present(p)
	if err == nil && !there {
		err = fmt.Errorf("store: %s is not there", p)
	}
	return there, err
}

// QuarantineResume writes what the interrupted quarantine of the file did not.
func (s *Store) QuarantineResume(q QuarantineResume) error {
	c := q.File
	if c.Dir == BodiesDir || c.Alert != q.Alert.ID {
		return errors.New("store: a quarantine is resumed with the alert its file names")
	}
	t, err := s.prepareTail(c.Dir, c.Original, c.Key, q.Scan, q.Replacement, q.Session, q.Alert)
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declQuarantineResume)
	defer tx.Close()
	return t.write(s, tx)
}

// BodyQuarantine is one chunk of the quarantine of a body whose content does
// not match its name.
type BodyQuarantine struct {
	Name    string  // the body file
	AlertID AlertID // part of the quarantined name
	// Rename is set on the first chunk: the body is renamed before anything
	// else, which makes the head record of the transaction.
	Rename bool
	// Out, In and Ctl are the records that refer to the body, each replaced by
	// a lost record or stripped of the reference.
	Out []OutRecord
	In  []InRecord
	Ctl []CtlRecord
	// Alert is set on the last chunk.
	Alert *AlertRecord
}

// QuarantineBody writes a chunk of the quarantine of a body.
func (s *Store) QuarantineBody(q BodyQuarantine) error {
	if !isHashName(q.Name) || q.AlertID == (AlertID{}) {
		return errors.New("store: a body quarantine names a body and an alert")
	}
	of, err := outFiles(q.Out)
	if err != nil {
		return err
	}
	inf, err := inFiles(q.In)
	if err != nil {
		return err
	}
	cf, err := ctlFiles(q.Ctl)
	if err != nil {
		return err
	}
	var af *file
	if q.Alert != nil {
		if q.Alert.ID != q.AlertID {
			return errors.New("store: the alert of a body quarantine is the one its file names")
		}
		f, err := recordFile(AlertsDir, *q.Alert)
		if err != nil {
			return err
		}
		af = &f
	}
	if !q.Rename && len(of)+len(inf)+len(cf) == 0 && af == nil {
		return nil
	}
	tx := s.w.Begin(&declQuarantineBody)
	defer tx.Close()
	if q.Rename {
		from := path.Join(BodiesDir, q.Name)
		if err := tx.RenameFile("body", from, path.Join(BodiesDir, corruptName(q.Name, q.AlertID))); err != nil {
			return err
		}
	}
	if err := writeGroup(tx, "out", "out-dir", OutDir, of); err != nil {
		return err
	}
	if err := writeGroup(tx, "in", "in-dir", InDir, inf); err != nil {
		return err
	}
	if err := writeGroup(tx, "ctl", "ctl-dir", CtlDir, cf); err != nil {
		return err
	}
	if af != nil {
		if err := s.replace(tx, "alert", *af); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// OrphanGC removes the bodies, then syncs the directory.
func (s *Store) OrphanGC(bodies []string) error {
	if len(bodies) == 0 {
		return nil
	}
	for _, b := range bodies {
		if !isHashName(b) {
			return fmt.Errorf("store: %q is not the name of a body", b)
		}
	}
	tx := s.w.Begin(&declOrphanGC)
	defer tx.Close()
	for _, b := range bodies {
		if _, err := s.removeRef(tx, "body", FileRef{Dir: BodiesDir, Name: b}); err != nil {
			return err
		}
	}
	if err := tx.SyncDir("dir", BodiesDir); err != nil {
		return err
	}
	return tx.Commit()
}

// OrphanGCEndpoint removes the endpoint records, then syncs the directory.
func (s *Store) OrphanGCEndpoint(eps []EndpointRecord) error {
	if len(eps) == 0 {
		return nil
	}
	tx := s.w.Begin(&declOrphanGCEndpoint)
	defer tx.Close()
	for _, e := range eps {
		if _, err := s.removeRef(tx, "endpoint", FileRef{Dir: EndpointsDir, Name: e.Name()}); err != nil {
			return err
		}
	}
	if err := tx.SyncDir("dir", EndpointsDir); err != nil {
		return err
	}
	return tx.Commit()
}

// CtlEffectReplay writes the endpoint records EffectReplays returned.
func (s *Store) CtlEffectReplay(eps []EndpointRecord) error {
	if len(eps) == 0 {
		return nil
	}
	var files []file
	for _, e := range eps {
		f, err := recordFile(EndpointsDir, e)
		if err != nil {
			return err
		}
		files = append(files, f)
	}
	tx := s.w.Begin(&declCtlEffectReplay)
	defer tx.Close()
	if err := writeGroup(tx, "endpoint", "endpoint-dir", EndpointsDir, files); err != nil {
		return err
	}
	return tx.Commit()
}

// EndpointDrainReplay writes a chunk of the closed deliveries of closed
// endpoints.
func (s *Store) EndpointDrainReplay(b DrainBatch) error {
	return s.drain(&declEndpointDrainReplay, nil, b, false)
}

// InstanceDrainReplay closes the endpoints of disabled instances and writes a
// chunk of their deliveries.
func (s *Store) InstanceDrainReplay(closed []EndpointRecord, b DrainBatch) error {
	return s.drain(&declInstanceDrainReplay, closed, b, true)
}

func (s *Store) followersReplay(decl *txn.Decl, ins []InRecord) error {
	if len(ins) == 0 {
		return nil
	}
	files, err := inFiles(ins)
	if err != nil {
		return err
	}
	tx := s.w.Begin(decl)
	defer tx.Close()
	if err := writeGroup(tx, "in", "in-dir", InDir, files); err != nil {
		return err
	}
	return tx.Commit()
}

// PromptReplay writes the followers of a prompt, with the event applied.
func (s *Store) PromptReplay(ins []InRecord) error { return s.followersReplay(&declPromptReplay, ins) }

// BlockedReplay writes the followers of a block or a recovery.
func (s *Store) BlockedReplay(ins []InRecord) error {
	return s.followersReplay(&declBlockedReplay, ins)
}

// FeedbackReplay writes the source records without the feedback a notice covers.
func (s *Store) FeedbackReplay(outs []OutRecord) error {
	if len(outs) == 0 {
		return nil
	}
	files, err := outFiles(outs)
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declFeedbackReplay)
	defer tx.Close()
	if err := writeGroup(tx, "out", "out-dir", OutDir, files); err != nil {
		return err
	}
	return tx.Commit()
}
