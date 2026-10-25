package store

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
)

// The methods of this file are the transactions of the same name. Each encodes
// everything first, so that what cannot be written is refused before a
// transaction opens; then opens the transaction, writes in the order of its
// declaration, commits and returns. An error is the txn error of the failed
// write, with its class, never wrapped into another. The caller acknowledges and
// updates its memory only after nil.
//
// A record that replaces another is passed whole. A target record carries the
// revision it was read with, and is written with the next one.

// file is one file to write: its path and its bytes.
type file struct {
	path string
	data []byte
}

func recordFile(dir string, rec Record) (file, error) {
	data, err := EncodeRecord(rec)
	if err != nil {
		return file{}, err
	}
	return file{path.Join(dir, rec.Name()), data}, nil
}

// committed commits tx and returns what the transaction wrote, or nothing when
// the commit failed.
func committed[T any](tx *txn.Tx, written T) (T, error) {
	if err := tx.Commit(); err != nil {
		var none T
		return none, err
	}
	return written, nil
}

// bumped is a target record as it is written: one revision on. A lost record
// has none.
func bumped(in InRecord) InRecord {
	if !in.Lost {
		in.Rev++
	}
	return in
}

func outFiles(outs []OutRecord) ([]file, error) {
	var fs []file
	for _, o := range outs {
		f, err := recordFile(OutDir, o)
		if err != nil {
			return nil, err
		}
		fs = append(fs, f)
	}
	return fs, nil
}

// inFiles encodes target records as they are written, and returns them as
// written too, in the order given: the one place that says what a write of a
// target record produces, so that no caller works it out again.
func inFiles(ins []InRecord) (written []InRecord, fs []file, err error) {
	for _, in := range ins {
		in = bumped(in)
		f, err := recordFile(InDir, in)
		if err != nil {
			return nil, nil, err
		}
		written = append(written, in)
		fs = append(fs, f)
	}
	return written, fs, nil
}

func ctlFiles(ctls []CtlRecord) ([]file, error) {
	var fs []file
	for _, c := range ctls {
		f, err := recordFile(CtlDir, c)
		if err != nil {
			return nil, err
		}
		fs = append(fs, f)
	}
	return fs, nil
}

// bodyFile is the file of a body, which must be what the reference of the
// record that holds it says.
func bodyFile(content []byte, ref *BodyRef) (*file, error) {
	if content == nil {
		return nil, nil
	}
	if len(content) > envelope.MaxBody {
		return nil, fmt.Errorf("store: a body of %d bytes is over the limit of %d", len(content), envelope.MaxBody)
	}
	want := BodyRefOf(content)
	if ref == nil || *ref != want {
		return nil, errors.New("store: the record does not refer to the body written with it")
	}
	return &file{path.Join(BodiesDir, want.Name()), content}, nil
}

// groups writes files with ReplaceDefer under role and syncs dir once after
// them. With no files it writes nothing.
func writeGroup(tx *txn.Tx, role, syncRole, dir string, files []file) error {
	if len(files) == 0 {
		return nil
	}
	for _, f := range files {
		if err := tx.ReplaceDefer(role, f.path, f.data, fsx.ModePrivate); err != nil {
			return err
		}
	}
	return tx.SyncDir(syncRole, dir)
}

func (s *Store) replace(tx *txn.Tx, role string, f file) error {
	return tx.Replace(role, f.path, f.data, fsx.ModePrivate)
}

// existence says which of the files a call is about to remove are there. The
// call asks the disk before it opens its transaction. A transaction that has passed its
// commit point turns every failure into a fatal one, and a read that fails
// there would be an ordinary error on a disk that already changed; so no call
// reads the disk once it writes.
type existence map[FileRef]bool

// exist asks the disk about every file of the groups.
func (s *Store) exist(groups ...[]FileRef) (existence, error) {
	ex := existence{}
	for _, group := range groups {
		for _, f := range group {
			there, err := fsx.Exists(s.r, path.Join(f.Dir, f.Name))
			if err != nil {
				return nil, err
			}
			ex[f] = there
		}
	}
	return ex, nil
}

// take reports whether the file is there and forgets it, so that a name listed
// twice is removed once.
func (e existence) take(f FileRef) bool {
	there := e[f]
	delete(e, f)
	return there
}

// bodyFiles are the files of the bodies, each once.
func bodyFiles(refs []BodyRef) []FileRef {
	var out []FileRef
	for _, r := range dedupe(refs) {
		out = append(out, FileRef{Dir: BodiesDir, Name: r.Name()})
	}
	return out
}

// release removes the body files named, those that are there, and syncs
// bodies/. A body that is already gone was removed by an earlier, interrupted
// release.
func (s *Store) release(tx *txn.Tx, refs []BodyRef, ex existence) error {
	if len(refs) == 0 {
		return nil
	}
	for _, f := range bodyFiles(refs) {
		if !ex.take(f) {
			continue
		}
		if err := tx.Remove("body", path.Join(f.Dir, f.Name)); err != nil {
			return err
		}
	}
	return tx.SyncDir("body-dir", BodiesDir)
}

func dedupe(refs []BodyRef) []BodyRef {
	seen := map[BodyRef]bool{}
	var out []BodyRef
	for _, r := range refs {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

// Intake records a message accepted from a local sender: the body, if any, and
// the source record.
func (s *Store) Intake(body []byte, out OutRecord) error {
	of, err := recordFile(OutDir, out)
	if err != nil {
		return err
	}
	if out.Lost != nil {
		return errors.New("store: a lost record is never accepted")
	}
	bf, err := bodyFile(body, out.Body)
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declIntake)
	defer tx.Close()
	if bf != nil {
		if err := s.replace(tx, "body", *bf); err != nil {
			return err
		}
	}
	if err := s.replace(tx, "out", of); err != nil {
		return err
	}
	return tx.Commit()
}

// Deliver records the messages accepted for the endpoints of this node. The
// body is nil when the message was rejected, or when it is already there
// because the source on this node wrote it. It returns the records as written.
func (s *Store) Deliver(body []byte, ins []InRecord) ([]InRecord, error) {
	if len(ins) == 0 {
		return nil, errors.New("store: Deliver needs a record")
	}
	written, files, err := inFiles(ins)
	if err != nil {
		return nil, err
	}
	var bf *file
	if body != nil {
		if bf, err = bodyFile(body, ins[0].Body); err != nil {
			return nil, err
		}
	}
	tx := s.w.Begin(&declDeliver)
	defer tx.Close()
	if bf != nil {
		if err := s.replace(tx, "body", *bf); err != nil {
			return nil, err
		}
	}
	if err := writeGroup(tx, "in", "in-dir", InDir, files); err != nil {
		return nil, err
	}
	return committed(tx, written)
}

// DeliverReply records a reply that carries evidence: the updated source
// record of the message it answers, if the evidence changed it, before the
// target records of the reply, then removes the bodies whose last reference
// this transaction drops; the caller computes them from its reference counts,
// counting the records this transaction writes (a reply may say the same bytes
// as the message it answers, and then refers to that very body).
// It returns the target records as written (the revision one on).
func (s *Store) DeliverReply(body []byte, out *OutRecord, ins []InRecord, release []BodyRef) ([]InRecord, error) {
	if len(ins) == 0 {
		return nil, errors.New("store: DeliverReply needs a record")
	}
	written, files, err := inFiles(ins)
	if err != nil {
		return nil, err
	}
	var of *file
	if out != nil {
		f, err := recordFile(OutDir, *out)
		if err != nil {
			return nil, err
		}
		of = &f
	}
	var bf *file
	if body != nil {
		if bf, err = bodyFile(body, ins[0].Body); err != nil {
			return nil, err
		}
	}
	ex, err := s.exist(bodyFiles(release))
	if err != nil {
		return nil, err
	}
	tx := s.w.Begin(&declDeliverReply)
	defer tx.Close()
	if bf != nil {
		if err := s.replace(tx, "reply-body", *bf); err != nil {
			return nil, err
		}
	}
	if of != nil {
		if err := s.replace(tx, "out", *of); err != nil {
			return nil, err
		}
	}
	if err := writeGroup(tx, "in", "in-dir", InDir, files); err != nil {
		return nil, err
	}
	if err := s.release(tx, release, ex); err != nil {
		return nil, err
	}
	return committed(tx, written)
}

// SourceUpdate replaces one source record and removes the bodies whose last
// reference it dropped.
func (s *Store) SourceUpdate(out OutRecord, release []BodyRef) error {
	of, err := recordFile(OutDir, out)
	if err != nil {
		return err
	}
	ex, err := s.exist(bodyFiles(release))
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declSourceUpdate)
	defer tx.Close()
	if err := s.replace(tx, "out", of); err != nil {
		return err
	}
	if err := s.release(tx, release, ex); err != nil {
		return err
	}
	return tx.Commit()
}

// ControlIntake records a control signal accepted on the target: the initial
// message's body and the session endpoint it created, as preparation, then the
// control record.
func (s *Store) ControlIntake(body []byte, endpoint *EndpointRecord, ctl CtlRecord) error {
	cf, err := recordFile(CtlDir, ctl)
	if err != nil {
		return err
	}
	var ef *file
	if endpoint != nil {
		if endpoint.Status != EndpointCreating || endpoint.CreatedBy != trimRec(ctl.Name()) {
			return errors.New("store: a session endpoint made with a control record is being created and names the record as its creator")
		}
		if ctl.Intent == nil || ctl.Intent.Endpoint != endpoint.ID {
			return errors.New("store: a session endpoint made with a control record is the one the record's intent creates")
		}
		f, err := recordFile(EndpointsDir, *endpoint)
		if err != nil {
			return err
		}
		ef = &f
	}
	bf, err := bodyFile(body, ctl.Body)
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declControlIntake)
	defer tx.Close()
	if bf != nil {
		if err := s.replace(tx, "body", *bf); err != nil {
			return err
		}
	}
	if ef != nil {
		if err := s.replace(tx, "endpoint", *ef); err != nil {
			return err
		}
	}
	if err := s.replace(tx, "ctl", cf); err != nil {
		return err
	}
	return tx.Commit()
}

func trimRec(name string) string { return name[:len(name)-len(recordSuffix)] }

// ControlResult records the result of a control signal, then the session
// endpoint it moves, then removes the bodies it released.
func (s *Store) ControlResult(ctl CtlRecord, endpoint *EndpointRecord, release []BodyRef) error {
	cf, err := recordFile(CtlDir, ctl)
	if err != nil {
		return err
	}
	var ef *file
	if endpoint != nil {
		f, err := recordFile(EndpointsDir, *endpoint)
		if err != nil {
			return err
		}
		ef = &f
	}
	ex, err := s.exist(bodyFiles(release))
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declControlResult)
	defer tx.Close()
	if err := s.replace(tx, "ctl", cf); err != nil {
		return err
	}
	if ef != nil {
		if err := s.replace(tx, "endpoint", *ef); err != nil {
			return err
		}
	}
	if err := s.release(tx, release, ex); err != nil {
		return err
	}
	return tx.Commit()
}

// ControlReply replaces a source record that handled a control reply.
func (s *Store) ControlReply(out OutRecord) error {
	of, err := recordFile(OutDir, out)
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declControlReply)
	defer tx.Close()
	if err := s.replace(tx, "out", of); err != nil {
		return err
	}
	return tx.Commit()
}

// PromptBegin records that a prompt is about to be handed to an adapter.
func (s *Store) PromptBegin(a AttemptsRecord) error {
	af, err := recordFile(AttemptsDir, a)
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declPromptBegin)
	defer tx.Close()
	if err := s.replace(tx, "attempts", af); err != nil {
		return err
	}
	return tx.Commit()
}

// checkFollowers verifies that the records written with a head record are
// followers it lists, as they were read: the same revision and acceptance time.
// A head that lists a revision the follower does not have would never be
// replayed, and one that lists a record it does not write would lose it.
func checkFollowers(head AttemptsRecord, events []PendingEvent, ins []InRecord) error {
	if !slices.Contains(events, head.Pending.Event) {
		return fmt.Errorf("store: the head record's pending event is %d, not one of %v", head.Pending.Event, events)
	}
	for _, in := range ins {
		i := slices.IndexFunc(head.Pending.Items, func(it PendingItem) bool { return it.Key == in.Key })
		if i < 0 {
			return fmt.Errorf("store: %s is written with the head record but not listed in it", in.Key)
		}
		if it := head.Pending.Items[i]; it.Rev != in.Rev || !it.AcceptedAt.Equal(in.AcceptedAt) {
			return fmt.Errorf("store: %s is listed with revision %d accepted at %v, and written as revision %d accepted at %v", in.Key, it.Rev, it.AcceptedAt, in.Rev, in.AcceptedAt)
		}
	}
	return nil
}

func (s *Store) headAndFollowers(decl *txn.Decl, head AttemptsRecord, events []PendingEvent, ins []InRecord) ([]InRecord, error) {
	if err := checkFollowers(head, events, ins); err != nil {
		return nil, err
	}
	af, err := recordFile(AttemptsDir, head)
	if err != nil {
		return nil, err
	}
	written, files, err := inFiles(ins)
	if err != nil {
		return nil, err
	}
	tx := s.w.Begin(decl)
	defer tx.Close()
	if err := s.replace(tx, "attempts", af); err != nil {
		return nil, err
	}
	if err := writeGroup(tx, "in", "in-dir", InDir, files); err != nil {
		return nil, err
	}
	return committed(tx, written)
}

// PromptEnd records the result of a prompt, then the notified target records
// its Pending lists.
// It returns the target records as written (the revision one on).
func (s *Store) PromptEnd(a AttemptsRecord, ins []InRecord) ([]InRecord, error) {
	return s.headAndFollowers(&declPromptEnd, a, []PendingEvent{PendingNotified}, ins)
}

// Blocked records a blocked report, then the target records its Pending lists.
// It returns the target records as written (the revision one on).
func (s *Store) Blocked(a AttemptsRecord, ins []InRecord) ([]InRecord, error) {
	return s.headAndFollowers(&declBlocked, a, []PendingEvent{PendingBlocked, PendingUnblocked}, ins)
}

// Lease records the leases of one page of target records.
// It returns the target records as written (the revision one on).
func (s *Store) Lease(ins []InRecord) ([]InRecord, error) {
	if len(ins) == 0 {
		return nil, errors.New("store: Lease needs a record")
	}
	written, files, err := inFiles(ins)
	if err != nil {
		return nil, err
	}
	tx := s.w.Begin(&declLease)
	defer tx.Close()
	if err := writeGroup(tx, "in", "in-dir", InDir, files); err != nil {
		return nil, err
	}
	return committed(tx, written)
}

// Ack records acknowledgements and removes the bodies they released.
// It returns the target records as written (the revision one on).
func (s *Store) Ack(ins []InRecord, release []BodyRef) ([]InRecord, error) {
	if len(ins) == 0 {
		return nil, errors.New("store: Ack needs a record")
	}
	written, files, err := inFiles(ins)
	if err != nil {
		return nil, err
	}
	ex, err := s.exist(bodyFiles(release))
	if err != nil {
		return nil, err
	}
	tx := s.w.Begin(&declAck)
	defer tx.Close()
	if err := writeGroup(tx, "in", "in-dir", InDir, files); err != nil {
		return nil, err
	}
	if err := s.release(tx, release, ex); err != nil {
		return nil, err
	}
	return committed(tx, written)
}

// TickBatch is the records one Tick moved.
type TickBatch struct {
	Out     []OutRecord
	In      []InRecord
	Ctl     []CtlRecord
	Release []BodyRef
}

// Tick records records moved by the passing of time.
// It returns the target records as written (the revision one on).
func (s *Store) Tick(b TickBatch) ([]InRecord, error) {
	of, err := outFiles(b.Out)
	if err != nil {
		return nil, err
	}
	written, inf, err := inFiles(b.In)
	if err != nil {
		return nil, err
	}
	cf, err := ctlFiles(b.Ctl)
	if err != nil {
		return nil, err
	}
	if len(of)+len(inf)+len(cf) == 0 {
		return nil, errors.New("store: Tick needs a record")
	}
	ex, err := s.exist(bodyFiles(b.Release))
	if err != nil {
		return nil, err
	}
	tx := s.w.Begin(&declTick)
	defer tx.Close()
	if err := writeGroup(tx, "out", "out-dir", OutDir, of); err != nil {
		return nil, err
	}
	if err := writeGroup(tx, "in", "in-dir", InDir, inf); err != nil {
		return nil, err
	}
	if err := writeGroup(tx, "ctl", "ctl-dir", CtlDir, cf); err != nil {
		return nil, err
	}
	if err := s.release(tx, b.Release, ex); err != nil {
		return nil, err
	}
	return committed(tx, written)
}

// EndpointUpdate replaces an endpoint record.
func (s *Store) EndpointUpdate(ep EndpointRecord) error {
	ef, err := recordFile(EndpointsDir, ep)
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declEndpointUpdate)
	defer tx.Close()
	if err := s.replace(tx, "endpoint", ef); err != nil {
		return err
	}
	return tx.Commit()
}

// EndpointClose records the closing of an endpoint, the head record of a
// closure, and the result of the operation that asked for it, if any.
func (s *Store) EndpointClose(ep EndpointRecord, result *AdminRecord) error {
	if ep.Status != EndpointClosed {
		return errors.New("store: EndpointClose writes a closed endpoint")
	}
	ef, err := recordFile(EndpointsDir, ep)
	if err != nil {
		return err
	}
	var rf *file
	if result != nil {
		f, err := recordFile(AdminDir, *result)
		if err != nil {
			return err
		}
		rf = &f
	}
	tx := s.w.Begin(&declEndpointClose)
	defer tx.Close()
	if err := s.replace(tx, "endpoint", ef); err != nil {
		return err
	}
	if rf != nil {
		if err := s.replace(tx, "result", *rf); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DrainBatch is a chunk of the records closed because their endpoint is.
type DrainBatch struct {
	In      []InRecord
	Ctl     []CtlRecord
	Release []BodyRef
}

// drain writes the endpoint records that close an instance's endpoints, when
// there are any, and a chunk of closed deliveries.
func (s *Store) drain(decl *txn.Decl, closed []EndpointRecord, b DrainBatch, withHead bool) ([]InRecord, error) {
	var ep []file
	for _, e := range closed {
		if e.Status != EndpointClosed {
			return nil, errors.New("store: an endpoint closed by a drain is closed")
		}
		f, err := recordFile(EndpointsDir, e)
		if err != nil {
			return nil, err
		}
		ep = append(ep, f)
	}
	written, inf, err := inFiles(b.In)
	if err != nil {
		return nil, err
	}
	cf, err := ctlFiles(b.Ctl)
	if err != nil {
		return nil, err
	}
	if withHead && len(ep) == 0 {
		return nil, errors.New("store: an instance drain closes an endpoint")
	}
	if !withHead && len(inf)+len(cf) == 0 {
		return nil, errors.New("store: a drain closes a record")
	}
	ex, err := s.exist(bodyFiles(b.Release))
	if err != nil {
		return nil, err
	}
	tx := s.w.Begin(decl)
	defer tx.Close()
	if withHead {
		if err := writeGroup(tx, "endpoint", "endpoint-dir", EndpointsDir, ep); err != nil {
			return nil, err
		}
	}
	if err := writeGroup(tx, "in", "in-dir", InDir, inf); err != nil {
		return nil, err
	}
	if err := writeGroup(tx, "ctl", "ctl-dir", CtlDir, cf); err != nil {
		return nil, err
	}
	if err := s.release(tx, b.Release, ex); err != nil {
		return nil, err
	}
	return committed(tx, written)
}

// EndpointDrain records a chunk of the deliveries closed by an endpoint's
// closing.
// It returns the target records as written (the revision one on).
func (s *Store) EndpointDrain(b DrainBatch) ([]InRecord, error) {
	return s.drain(&declEndpointDrain, nil, b, false)
}

// InstanceDrain records the closing of the endpoints of a disabled instance
// and a chunk of the deliveries closed with them.
// It returns the target records as written (the revision one on).
func (s *Store) InstanceDrain(closed []EndpointRecord, b DrainBatch) ([]InRecord, error) {
	return s.drain(&declInstanceDrain, closed, b, true)
}

// FeedbackForm records a notice to a sender's endpoint and its body.
// It returns the notice as written (the revision one on).
func (s *Store) FeedbackForm(body []byte, notice InRecord) (InRecord, error) {
	if !notice.Key.Sender.IsSystem() {
		return InRecord{}, errors.New("store: a notice is sent by the system endpoint")
	}
	written := bumped(notice)
	nf, err := recordFile(InDir, written)
	if err != nil {
		return InRecord{}, err
	}
	bf, err := bodyFile(body, notice.Body)
	if err != nil {
		return InRecord{}, err
	}
	if bf == nil {
		return InRecord{}, errors.New("store: a notice has a body")
	}
	tx := s.w.Begin(&declFeedbackForm)
	defer tx.Close()
	if err := s.replace(tx, "body", *bf); err != nil {
		return InRecord{}, err
	}
	if err := s.replace(tx, "notice", nf); err != nil {
		return InRecord{}, err
	}
	return committed(tx, written)
}

// FeedbackClear replaces the source records whose feedback items were covered
// by a notice.
func (s *Store) FeedbackClear(outs []OutRecord) error {
	if len(outs) == 0 {
		return errors.New("store: FeedbackClear needs a record")
	}
	files, err := outFiles(outs)
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declFeedbackClear)
	defer tx.Close()
	if err := writeGroup(tx, "out", "out-dir", OutDir, files); err != nil {
		return err
	}
	return tx.Commit()
}

// CheckpointPut records an adapter's checkpoint.
func (s *Store) CheckpointPut(c CheckpointRecord) error {
	f, err := recordFile(CheckpointsDir, c)
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declCheckpointPut)
	defer tx.Close()
	if err := s.replace(tx, "checkpoint", f); err != nil {
		return err
	}
	return tx.Commit()
}

// AlertRaise records an owner alert.
func (s *Store) AlertRaise(a AlertRecord) error {
	f, err := recordFile(AlertsDir, a)
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declAlertRaise)
	defer tx.Close()
	if err := s.replace(tx, "alert", f); err != nil {
		return err
	}
	return tx.Commit()
}

// DaemonStop records the result of the stop operation.
func (s *Store) DaemonStop(result AdminRecord) error {
	f, err := recordFile(AdminDir, result)
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declDaemonStop)
	defer tx.Close()
	if err := s.replace(tx, "result", f); err != nil {
		return err
	}
	return tx.Commit()
}

// AlertAckInput is an acknowledgement of an alert by the owner.
type AlertAckInput struct {
	Alert  AlertRecord // the alert as it was read, Acked set
	Result AdminRecord
	// Quarantined is the file the alert names when it is one of a directory
	// other than the delivery ones: it is removed before the alert is marked.
	// Nil when there is none or when the clean-up belongs to the sweep.
	Quarantined *CorruptFile
}

// AlertAck records an acknowledgement.
func (s *Store) AlertAck(a AlertAckInput) error {
	if a.Alert.Acked == nil {
		return errors.New("store: AlertAck writes an acknowledged alert")
	}
	af, err := recordFile(AlertsDir, a.Alert)
	if err != nil {
		return err
	}
	rf, err := recordFile(AdminDir, a.Result)
	if err != nil {
		return err
	}
	var q string
	if a.Quarantined != nil {
		if err := checkRef(FileRef{Dir: a.Quarantined.Dir, Name: a.Quarantined.Name}, true); err != nil {
			return err
		}
		if a.Quarantined.Dir == OutDir || a.Quarantined.Dir == InDir || a.Quarantined.Dir == CtlDir {
			return errors.New("store: an alert does not remove the quarantined file of a delivery record: the sweep does")
		}
		q = path.Join(a.Quarantined.Dir, a.Quarantined.Name)
	}
	var there bool
	if q != "" {
		if there, err = fsx.Exists(s.r, q); err != nil {
			return err
		}
	}
	tx := s.w.Begin(&declAlertAck)
	defer tx.Close()
	if q != "" {
		if there {
			if err := tx.Remove("quarantined", q); err != nil {
				return err
			}
			if err := tx.SyncDir("quarantined-dir", a.Quarantined.Dir); err != nil {
				return err
			}
		}
	}
	if err := s.replace(tx, "alert", af); err != nil {
		return err
	}
	if err := s.replace(tx, "result", rf); err != nil {
		return err
	}
	return tx.Commit()
}

// PreparedEndpoint is an endpoint record encoded for a transaction another
// package opens. Making it is the validation: PrepareEndpoint runs before that
// transaction begins, so a record that cannot be written is an error that
// touches nothing, and PutEndpoint can only fail the way a write of the
// transaction does.
type PreparedEndpoint struct{ f file }

// PrepareEndpoint validates and encodes an endpoint record. It does no I/O.
func PrepareEndpoint(ep EndpointRecord) (PreparedEndpoint, error) {
	f, err := recordFile(EndpointsDir, ep)
	if err != nil {
		return PreparedEndpoint{}, err
	}
	return PreparedEndpoint{f}, nil
}

// PutEndpoint writes a prepared endpoint record in a transaction another
// package opened, under the role the declaration gives it (RoleEndpoint by
// convention). The package that opened the transaction does the commit. It
// panics for a PreparedEndpoint that PrepareEndpoint did not return.
func PutEndpoint(tx *txn.Tx, role string, p PreparedEndpoint) error {
	if p.f.path == "" {
		panic("store: PutEndpoint of a value PrepareEndpoint did not return")
	}
	return tx.Replace(role, p.f.path, p.f.data, fsx.ModePrivate)
}

// PreparedAdminResult is the result of an administrative operation encoded for a
// transaction another package opens, as PreparedEndpoint is.
type PreparedAdminResult struct{ f file }

// PrepareAdminResult validates and encodes the result of an administrative
// operation. It does no I/O.
func PrepareAdminResult(r AdminRecord) (PreparedAdminResult, error) {
	f, err := recordFile(AdminDir, r)
	if err != nil {
		return PreparedAdminResult{}, err
	}
	return PreparedAdminResult{f}, nil
}

// PutAdminResult writes a prepared result in a transaction another package
// opened, under the role the declaration gives it (RoleResult by convention).
// It panics for a PreparedAdminResult that PrepareAdminResult did not return.
func PutAdminResult(tx *txn.Tx, role string, p PreparedAdminResult) error {
	if p.f.path == "" {
		panic("store: PutAdminResult of a value PrepareAdminResult did not return")
	}
	return tx.Replace(role, p.f.path, p.f.data, fsx.ModePrivate)
}

// ArchiveEvent is one diagnostic event of the archive.
type ArchiveEvent struct {
	At    time.Time
	File  string // the file name hash of the record, 64 hex digits
	Event string
	N     *int
}

// ArchiveEvents appends a batch of events to the archive file of the day of at.
// Nothing is synced: events are diagnostics, and a later sync of the same
// file makes them durable.
func (s *Store) ArchiveEvents(at time.Time, events []ArchiveEvent) error {
	if len(events) == 0 {
		return nil
	}
	lines := []byte("\n")
	for _, e := range events {
		l, err := eventLine(e)
		if err != nil {
			return err
		}
		lines = append(lines, l...)
	}
	tx := s.w.Begin(&declArchiveEvents)
	defer tx.Close()
	if err := tx.Append("archive", path.Join(ArchiveDir, ArchiveDay(at)), lines); err != nil {
		return err
	}
	return tx.Commit()
}

// ArchiveExpire removes the archive files named, which are older than the
// retention.
func (s *Store) ArchiveExpire(names []string) error {
	if len(names) == 0 {
		return nil
	}
	for _, n := range names {
		if _, ok := archiveDate(n); !ok {
			return fmt.Errorf("store: %q is not the name of an archive file", n)
		}
	}
	var present []string
	for _, n := range names {
		p := path.Join(ArchiveDir, n)
		there, err := fsx.Exists(s.r, p)
		if err != nil {
			return err
		}
		if there {
			present = append(present, p)
		}
	}
	if len(present) == 0 {
		return nil
	}
	tx := s.w.Begin(&declArchiveExpire)
	defer tx.Close()
	for _, p := range present {
		if err := tx.Remove("file", p); err != nil {
			return err
		}
	}
	if err := tx.SyncDir("dir", ArchiveDir); err != nil {
		return err
	}
	return tx.Commit()
}

// FileRef names a file of state/.
type FileRef struct {
	Dir  string
	Name string
}

// checkRef refuses a name that would reach outside the directory it names or a
// file that is not one a sweep removes.
func checkRef(f FileRef, corrupt bool) error {
	if !slices.Contains(recordDirs, f.Dir) && f.Dir != BodiesDir {
		return fmt.Errorf("store: %q is not a directory of records", f.Dir)
	}
	if f.Name == "" || path.Base(f.Name) != f.Name || fsx.IsLeftover(f.Name) {
		return fmt.Errorf("store: %q is not a file name", f.Name)
	}
	if corrupt {
		if _, _, ok := corruptParts(f.Dir, f.Name); !ok {
			return fmt.Errorf("store: %q is not a quarantined file", f.Name)
		}
		return nil
	}
	if !isRecordName(f.Name) {
		return fmt.Errorf("store: %q is not a record file", f.Name)
	}
	return nil
}

// SweepChunk is one chunk of the archive sweep.
type SweepChunk struct {
	// Now is the time of the sweep; its UTC date names the archive file.
	Now       time.Time
	Summaries []ArchiveSummary
	// Corrupt, Ctl, Records and Outs are removed in that order, then Bodies, the
	// directories of each of Corrupt and Ctl synced after the group and the others
	// after the last of them; Endpoints go last, after that sync.
	//
	// A record that depends on a control record (the derived delivery of a
	// session creation) goes in Records and its control record in Ctl. Records
	// holds the files of every directory but out/ and endpoints/; the source
	// records are Outs, as records and not as names, because the sweep writes the
	// floor of every sequence number they used before it removes them, and a
	// chunk that named them only by file could not. A closed session endpoint's
	// record goes in Endpoints, after the attempts, checkpoints and floor files that
	// hang on it are durably gone: a crash, or a power loss that kept one removal
	// and lost the other, must not leave those without the record that says whom
	// they belong to, since nothing would ever remove them.
	Corrupt   []FileRef
	Ctl       []FileRef
	Records   []FileRef
	Outs      []OutRecord
	Endpoints []FileRef
	Bodies    []BodyRef
	// KeepTarget, when set, prunes from the floor files the sweep writes the
	// floors of targets that can no longer receive messages.
	KeepTarget func(ident.Address) bool
}

// readFloors is the floors of one sending endpoint as the disk holds them: none
// when there is no floor file, a DamagedFileError when it is damaged. It is the
// one place a floor file is read for writing.
func (s *Store) readFloors(sender ident.EndpointID) (map[ident.Address]uint64, error) {
	name := SeqRecord{Sender: sender}.Name()
	data, err := s.r.ReadFile(path.Join(SeqDir, name))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return map[ident.Address]uint64{}, nil
	case err != nil:
		return nil, err
	}
	rec, dmg := DecodeRecord(SeqDir, name, data)
	if dmg != nil {
		return nil, &DamagedFileError{File: damagedFile(SeqDir, name, dmg)}
	}
	return rec.(SeqRecord).Floors, nil
}

// floorRecordFile is the floor file of a sender: what the disk holds with the
// raises merged in, a floor never lowered, and the targets keep refuses dropped.
func floorRecordFile(sender ident.EndpointID, current, raises map[ident.Address]uint64, keep func(ident.Address) bool) (file, error) {
	rec := SeqRecord{Sender: sender, Floors: map[ident.Address]uint64{}}
	for a, n := range current {
		rec.Floors[a] = n
	}
	for a, n := range raises {
		if n == 0 || a.IsZero() {
			return file{}, errors.New("store: a floor is for an address and at least 1")
		}
		if n > rec.Floors[a] {
			rec.Floors[a] = n
		}
	}
	if keep != nil {
		for a := range rec.Floors {
			if !keep(a) {
				delete(rec.Floors, a)
			}
		}
	}
	return recordFile(SeqDir, rec)
}

// floorFile is the floor file of a sender with the given raises merged into what
// the disk holds.
func (s *Store) floorFile(sender ident.EndpointID, raises map[ident.Address]uint64, keep func(ident.Address) bool) (file, error) {
	current, err := s.readFloors(sender)
	if err != nil {
		return file{}, err
	}
	return floorRecordFile(sender, current, raises, keep)
}

// ArchiveSweep appends the summaries to the archive, makes them durable, writes
// the sequence floors of the source records it removes and removes the records.
// The order is the guarantee: no record is removed before its summary and the
// floors of its sequence numbers are durable. The floors are the store's to
// work out (floorsOf): a caller cannot remove a source record without them.
func (s *Store) ArchiveSweep(c SweepChunk) error {
	for _, f := range c.Corrupt {
		if err := checkRef(f, true); err != nil {
			return err
		}
	}
	for _, f := range c.Ctl {
		if f.Dir != CtlDir {
			return errors.New("store: the control records of a sweep are in ctl/")
		}
		if err := checkRef(f, false); err != nil {
			return err
		}
	}
	for _, f := range c.Records {
		if f.Dir == BodiesDir || f.Dir == OutDir || f.Dir == EndpointsDir {
			return fmt.Errorf("store: %s is not removed as a record of a sweep: bodies, source records and endpoints have their own groups", f.Dir)
		}
		if err := checkRef(f, false); err != nil {
			return err
		}
	}
	outs := make([]FileRef, 0, len(c.Outs))
	for _, o := range c.Outs {
		if _, err := o.Key.Bytes(); err != nil {
			return fmt.Errorf("store: a source record of a sweep: %w", err)
		}
		outs = append(outs, FileRef{Dir: OutDir, Name: o.Name()})
	}
	for _, f := range c.Endpoints {
		if f.Dir != EndpointsDir {
			return errors.New("store: the endpoints of a sweep are in endpoints/")
		}
		if err := checkRef(f, false); err != nil {
			return err
		}
	}
	var lines []byte
	for _, sm := range c.Summaries {
		l, err := summaryLine(sm)
		if err != nil {
			return err
		}
		lines = append(lines, l...)
	}
	var floors []file
	for _, w := range floorsOf(c.Outs) {
		f, err := s.floorFile(w.Sender, w.Floors, c.KeepTarget)
		if err != nil {
			return err
		}
		floors = append(floors, f)
	}
	if len(lines)+len(floors)+len(c.Corrupt)+len(c.Ctl)+len(c.Records)+len(outs)+len(c.Endpoints)+len(c.Bodies) == 0 {
		return nil
	}

	ex, err := s.exist(c.Corrupt, c.Ctl, c.Records, outs, bodyFiles(c.Bodies), c.Endpoints)
	if err != nil {
		return err
	}
	tx := s.w.Begin(&declArchiveSweep)
	defer tx.Close()
	if len(lines) > 0 {
		p := path.Join(ArchiveDir, ArchiveDay(c.Now))
		if err := tx.Append("archive", p, append([]byte("\n"), lines...)); err != nil {
			return err
		}
		if err := tx.SyncFile("archive", p); err != nil {
			return err
		}
	}
	for _, f := range floors {
		if err := s.replace(tx, "seq-floor", f); err != nil {
			return err
		}
	}
	if err := s.removeAll(tx, "corrupt", "corrupt-dir", c.Corrupt, ex); err != nil {
		return err
	}
	if err := s.removeAll(tx, "ctl", "ctl-dir", c.Ctl, ex); err != nil {
		return err
	}
	dirs := map[string]bool{}
	for _, group := range [][]FileRef{c.Records, outs} {
		for _, f := range group {
			if removed, err := s.removeRef(tx, "record", f, ex); err != nil {
				return err
			} else if removed {
				dirs[f.Dir] = true
			}
		}
	}
	for _, f := range bodyFiles(c.Bodies) {
		if removed, err := s.removeRef(tx, "body", f, ex); err != nil {
			return err
		} else if removed {
			dirs[BodiesDir] = true
		}
	}
	for _, d := range sortedDirs(dirs) {
		if err := tx.SyncDir("dir", d); err != nil {
			return err
		}
	}
	// The endpoint records go once what hangs on them is durably gone: a power
	// loss that kept the removal of the endpoint and lost that of its floor file
	// would leave a file no sweep removes.
	if err := s.removeAll(tx, "endpoint", "endpoint-dir", c.Endpoints, ex); err != nil {
		return err
	}
	return tx.Commit()
}

func sortedDirs(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for d := range m {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// removeRef removes a file if ex says it is there, reporting whether it did.
func (s *Store) removeRef(tx *txn.Tx, role string, f FileRef, ex existence) (bool, error) {
	if !ex.take(f) {
		return false, nil
	}
	return true, tx.Remove(role, path.Join(f.Dir, f.Name))
}

// removeAll removes files under one role and syncs each directory they were in
// under another.
func (s *Store) removeAll(tx *txn.Tx, role, syncRole string, refs []FileRef, ex existence) error {
	dirs := map[string]bool{}
	for _, f := range refs {
		if removed, err := s.removeRef(tx, role, f, ex); err != nil {
			return err
		} else if removed {
			dirs[f.Dir] = true
		}
	}
	for _, d := range sortedDirs(dirs) {
		if err := tx.SyncDir(syncRole, d); err != nil {
			return err
		}
	}
	return nil
}
