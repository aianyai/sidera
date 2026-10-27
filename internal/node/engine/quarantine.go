package engine

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
)

// Damage. A file of state/ that no longer checks out, and a body that is not what
// its record says, are losses the node must not take silently. The store settles
// each in one transaction (Store.Quarantine, Store.QuarantineBody): the effects
// first, the alert for the owner, and the damaged file last. What is the engine's is
// everything that follows from the delivery rules: the record that replaces a source
// record, which says its deliveries are unknown (delivery.NewLostRecord) and owes the
// sender a notice of it, and the view and the queue of feedback that have to agree
// with what the settlement did. Nothing is told about the target's record that was
// lost: a target keeps no loss, and the source learns it when the target answers a
// pull that it holds no record.
//
// A damaged file of ctl/ is settled like the others: the pull answers from ctl/, so
// the record that goes is answered as the record the node no longer holds and the
// source is told it was lost; the session endpoints still being created by it are
// closed in the same settlement, for the intent that would have settled them went
// with the record.

// quarantineAlertKinds are the kinds of the alerts of a settlement.
const (
	alertCorruptRecord = "corrupt_record"
	alertCorruptBody   = "corrupt_body"
)

// maxSettlePasses is how many damaged files a single write may meet and have settled
// before the engine gives up: a floor file is one for each sending endpoint, and a
// write meets the one of its sender.
const maxSettlePasses = 4

// damagedAlert is the alert of a settlement, in the words of the damage and never in
// those of what the file held. The id is the store's.
func damagedAlert(now time.Time, d store.DamagedFile) store.QuarantineAlert {
	subject := d.Dir + "/" + d.Name
	return store.QuarantineAlert{Kind: alertCorruptRecord, At: now, Subject: subject, Detail: subject + " is damaged: " + d.Damage.Reason.String()}
}

// damagedCtlAlert is the alert of a damaged control record. When the record was
// still creating a session, a native session may exist that the node can no longer
// settle the creation of: it is the alert of a session nobody controls, as the
// adapter's own control_unknown is, so that the owner looks for it.
func damagedCtlAlert(now time.Time, sc *store.Scan, d store.DamagedFile) store.QuarantineAlert {
	a := damagedAlert(now, d)
	for _, ep := range store.QuarantineSessions(sc, d.Name) {
		if cur := endpointIn(sc, ep.ID); cur != nil && cur.Status == store.EndpointCreating {
			a.Kind = alertControlUnknown
			a.Detail += "; a session being created for it may exist, and its endpoint is closed"
			break
		}
	}
	return a
}

func endpointIn(sc *store.Scan, id ident.EndpointID) *store.EndpointRecord {
	for i := range sc.Endpoints {
		if sc.Endpoints[i].ID == id {
			return &sc.Endpoints[i]
		}
	}
	return nil
}

// lostOut is the record that replaces a source record that was lost: one lost
// delivery for each recipient of its key, and the feedback each owes the sender.
func (e *Engine) lostOut(key store.OutKey, now time.Time) (store.OutRecord, error) {
	o := store.OutRecord{Key: key, Lost: &store.LostOut{DiscoveredAt: now}}
	for _, to := range key.Recipients {
		rec, out, err := delivery.NewLostRecord(now)
		if err != nil {
			return store.OutRecord{}, err
		}
		o.Lost.Deliveries = append(o.Lost.Deliveries, rec)
		for _, fb := range out.Feedback {
			o.Feedback = append(o.Feedback, delivery.Item{
				Ref:      delivery.DeliveryRef{Sender: ident.Address{Node: e.d.Node, Endpoint: key.Sender}, Message: key.Message, Recipient: to},
				Feedback: fb, At: now,
			})
		}
	}
	return o, nil
}

// settleFile settles one damaged file with the scan the settlement is part of. A
// source record whose first line can be trusted is overwritten with the lost record
// that replaces it; every other file is settled by the store alone.
func (e *Engine) settleFile(now time.Time, sc *store.Scan, d store.DamagedFile) error {
	q := store.Quarantine{File: d, Alert: damagedAlert(now, d), Scan: sc, Node: e.d.Node}
	if d.Dir == store.CtlDir {
		q.Alert = damagedCtlAlert(now, sc, d)
	}
	if key, ok := d.Key.(store.OutKey); ok && d.Dir == store.OutDir && d.Damage.Class == store.BodyDamaged {
		o, err := e.lostOut(key, now)
		if err != nil {
			return err
		}
		q.Replacement = o
	}
	if err := e.d.Store.Quarantine(q); err != nil {
		return err
	}
	e.log.Warn("a damaged file was settled", "file", d.Dir+"/"+d.Name, "damage", d.Damage.Reason.String())
	return nil
}

// quarantineAll settles the damaged files the plan lists, one at a time, each with a scan taken after the one before was settled: a settlement
// raises floors, overwrites records and writes records that the next one reads. A
// file that an earlier settlement removed is not settled again.
func (e *Engine) quarantineAll(now time.Time, files []store.DamagedFile) error {
	for _, d := range files {
		sc, err := e.d.Store.Scan()
		if err != nil {
			return err
		}
		cur, ok := damagedIn(sc, d.Dir, d.Name)
		if !ok {
			continue
		}
		if err := e.settleFile(now, sc, cur); err != nil {
			return err
		}
	}
	return nil
}

func damagedIn(sc *store.Scan, dir, name string) (store.DamagedFile, bool) {
	i := slices.IndexFunc(sc.Damaged, func(d store.DamagedFile) bool { return d.Dir == dir && d.Name == name })
	if i < 0 {
		return store.DamagedFile{}, false
	}
	return sc.Damaged[i], true
}

// settleLive settles a damaged file a write met while the node runs, as the scan
// entry would have, and makes the book of sequence numbers and the alerts agree
// with what it did. The store reads a record file that it did not write only for the
// floors of a pair (seq/), so a floor file is the one damaged file a write meets; a
// file of another directory would have effects that the view and the queue of
// feedback must follow, which nothing needs yet, so it is refused and the write
// fails with it.
func (e *Engine) settleLive(now time.Time, d store.DamagedFile) error {
	if d.Dir != store.SeqDir {
		err := fmt.Errorf("engine: %s/%s is damaged, and a damaged file of %s is not settled while the node runs", d.Dir, d.Name, d.Dir)
		e.log.Error("a write met a damaged file that only the start settles", "file", d.Dir+"/"+d.Name, "error", err.Error())
		return err
	}
	before, err := e.d.Store.Scan()
	if err != nil {
		return err
	}
	cur, ok := damagedIn(before, d.Dir, d.Name)
	if !ok {
		return nil // settled since the write met it
	}
	if err := e.settleFile(now, before, cur); err != nil {
		return err
	}
	return e.syncAfterSettlement()
}

// syncAfterSettlement brings what a settlement changed that the view does not follow
// write by write into agreement with the disk: the book of sequence numbers is built
// again from it (a settlement raises floors and replaces records), and the alerts
// are read.
func (e *Engine) syncAfterSettlement() error {
	after, err := e.d.Store.Scan()
	if err != nil {
		return err
	}
	e.view.seq = store.NewSeqBook(after)
	e.view.noteAlerts(after.Alerts)
	return nil
}

// retryingDamaged runs a write of the store that reads other files: when it meets a
// damaged one, which the store refuses to read as if it were empty, the file is
// settled and the write is tried again.
func (e *Engine) retryingDamaged(now time.Time, write func() error) error {
	for pass := 0; ; pass++ {
		err := write()
		var de *store.DamagedFileError
		if !errors.As(err, &de) {
			return err
		}
		if pass >= maxSettlePasses {
			return fmt.Errorf("engine: a write still meets damaged files after %d were settled: %w", pass, err)
		}
		if serr := e.settleLive(now, de.File); serr != nil {
			return errors.Join(err, fmt.Errorf("engine: settling %s/%s: %w", de.File.Dir, de.File.Name, serr))
		}
	}
}

// readBody reads the body of the record owner, which holds ref, for whatever needs
// its bytes. A body that is not what its record says, and one whose file is gone,
// are damage: the record is settled before the error is returned, so that whoever
// asked is told what it found and need not know what was done. When the settlement
// itself fails the error carries both; a fatal failure of it is returned alone, because
// an error that says the body is damaged is read as "settled" and the record may not be.
// A read that failed for another reason says nothing of the body: it is a refusal for
// the storage being unavailable, which a retry may cure, and the record is left as it is.
func (e *Engine) readBody(now time.Time, owner store.Record, ref store.BodyRef) ([]byte, error) {
	b, err := e.d.Store.ReadBody(owner.Name(), ref)
	if err != nil && !errors.Is(err, store.ErrBodyMissing) && !errors.Is(err, store.ErrBodyMismatch) {
		return nil, &Refusal{Reason: ReasonStorageUnavailable, Err: err}
	}
	if gone := errors.Is(err, store.ErrBodyMissing); gone || errors.Is(err, store.ErrBodyMismatch) {
		if qerr := e.quarantineBody(now, owner, gone); qerr != nil {
			if txnFatal(e.classify(qerr)) { // a fatal error stops the loop after this command
				// Whether the record was settled is not known, and the caller must not take
				// it for settled, which the error of the body would tell it.
				return nil, qerr
			}
			return nil, errors.Join(err, qerr)
		}
	}
	return b, err
}

// quarantineBody settles the record whose body is not what it says, or is gone (gone),
// with an alert of its own. The body file is left: once the record is settled, it is an orphan and
// the next start removes it. A record that fails to be settled keeps its reference,
// so the next read of the body finds it again.
func (e *Engine) quarantineBody(now time.Time, owner store.Record, gone bool) error {
	file := path.Join(store.BodiesDir, store.BodyFileName(owner.Name()))
	what := " is not what its record says"
	if gone {
		what = " is gone"
	}
	alert := store.QuarantineAlert{
		Kind: alertCorruptBody, At: now, Subject: file,
		Detail: file + what + "; the record is settled",
	}
	if err := e.retryingDamaged(now, func() error { return e.quarantineOwner(now, owner, alert) }); err != nil {
		return err
	}
	return e.syncAfterSettlement()
}

// quarantineOwner settles one record whose body is not what it says, and follows it
// in the view: a target record that still needs the body is removed, a source
// record that still needs it is replaced by the lost record, and any other record
// keeps its state and drops the reference. The delivery of the initial message of a
// creation that is not settled is a target record that needs its body: the creation
// goes with it (failCreationWithoutMessage).
func (e *Engine) quarantineOwner(now time.Time, owner store.Record, alert store.QuarantineAlert) error {
	switch r := owner.(type) {
	case store.InRecord:
		if len(r.Covers) > 0 {
			// A notice that is lost owes its sender what it told, again.
			if err := e.reissueCovered(now, r); err != nil {
				return err
			}
		}
		written, err := e.d.Store.QuarantineBody(store.BodyQuarantine{Record: r, Alert: alert})
		if err != nil {
			return err
		}
		if written == nil {
			e.view.dropIn(r.Key)
			if r.Derived != nil {
				return e.failCreationWithoutMessage(now, store.TargetKey{SenderNode: r.Key.SenderNode, Sender: r.Key.Sender, Message: r.Key.Message, Recipient: r.Derived.From})
			}
			return nil
		}
		e.view.setIn(*written)
	case store.OutRecord:
		q := store.BodyQuarantine{Record: r, Alert: alert}
		var lost *store.OutRecord
		if r.NeedsBody() {
			o, err := e.lostOut(r.Key, now)
			if err != nil {
				return err
			}
			sc, err := e.d.Store.Scan()
			if err != nil {
				return err
			}
			q.Replacement, q.Scan, lost = o, sc, &o
		}
		if _, err := e.d.Store.QuarantineBody(q); err != nil {
			return err
		}
		if lost != nil {
			e.putOut(now, *lost)
			return nil
		}
		r.Body = nil
		e.putOut(now, r)
	default:
		return fmt.Errorf("engine: %T is not a record that owns a body", owner)
	}
	return nil
}
