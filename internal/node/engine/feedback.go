package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

// Feedback: what the node tells the endpoint that sent a message when a delivery
// of it goes wrong. The delivery rules decide that an item of feedback is owed
// (delivery.Item) and the record that made the decision stores the item with it;
// this file takes the items into the queue of the node, waits out the merge window
// and the rate cap (delivery.FeedbackQueue), and tells the endpoint with a notice:
// a message of the system endpoint to the endpoint, which the endpoint receives like
// any other. The notice is the proof that an item was told: it covers the items by
// the name of the record that holds each. Once the notice is written, clearCovered
// takes the covered items off those records, and a write of it that failed is tried
// again; the replay of feedback does so at the start for a crash in between.

// feedbackState is the feedback the node owes and has not told yet.
type feedbackState struct {
	// q is the queue: the items waiting for their notice and the notices sent
	// lately, for the rate cap. Everything in it is rebuilt from the disk at the
	// start (loadFeedback).
	q delivery.FeedbackQueue
	// outLost are the out-lost records that still hold an item, by file name: the
	// records of files that were lost unread, which no source record stands for.
	outLost map[string]store.OutLostRecord
	// uncleared are the items a written notice covers that a failed write left on
	// their records, until clearCovered takes them off (retryClears). Nothing queues
	// them again: they are told. They are not kept across a restart, whose replay
	// takes them off the records by the covers of the notices on disk.
	uncleared []delivery.Item
}

// sameItem says whether two items are the one decision: the queue holds one item
// for a delivery, a kind and a source record.
func sameItem(a, b delivery.Item) bool {
	return a.Ref == b.Ref && a.Feedback == b.Feedback && a.Source == b.Source
}

// loadFeedback builds the queue from the disk as it is after the recovery rules
// ran: every item a source record or an out-lost record still holds, and the
// notices sent within the rate window, which the notices of the system endpoint on
// disk say, so that a restart does not give an endpoint a new window.
func (e *Engine) loadFeedback(sc *store.Scan, now time.Time) error {
	e.fb = feedbackState{outLost: map[string]store.OutLostRecord{}}
	add := func(items []delivery.Item) error {
		for _, it := range items {
			if err := e.fb.q.Add(it); err != nil {
				return fmt.Errorf("engine: a stored item of feedback is not one the queue takes: %w", err)
			}
		}
		return nil
	}
	for _, o := range sc.Out {
		if err := add(o.Feedback); err != nil {
			return err
		}
	}
	for _, l := range sc.OutLost {
		if len(l.Feedback) > 0 {
			e.fb.outLost[l.Name()] = l
		}
		if err := add(l.Feedback); err != nil {
			return err
		}
	}
	for _, in := range sc.In {
		if in.Key.Sender.IsSystem() && in.Key.SenderNode == e.d.Node {
			e.fb.q.Sent = append(e.fb.q.Sent, delivery.SentNotice{Sender: ident.Address{Node: e.d.Node, Endpoint: in.Key.Recipient}, At: in.AcceptedAt})
		}
	}
	e.armFeedback(now)
	return nil
}

// putOut is the one place a source record that the loop wrote enters the view: it
// takes the record and hands the queue the items the record holds that the one it
// replaces did not, so that a decision made by any write is told, and an item the
// record held already is not queued a second time.
func (e *Engine) putOut(now time.Time, o store.OutRecord) {
	known := e.view.out[o.Name()].Feedback
	e.view.setOut(o)
	e.queueFeedback(now, known, o.Feedback)
}

// queueFeedback queues the items of held that known does not have.
func (e *Engine) queueFeedback(now time.Time, known, held []delivery.Item) {
	queued := false
	for _, it := range held {
		if slices.ContainsFunc(known, func(k delivery.Item) bool { return sameItem(k, it) }) {
			continue
		}
		if err := e.fb.q.Add(it); err != nil {
			e.log.Error("an item of feedback the engine decided is not one the queue takes; the sender is not told", "error", err.Error())
			continue
		}
		queued = true
	}
	if queued {
		e.armFeedback(now)
	}
}

// armFeedback points the entry of the schedule at the instant the queue next has a
// notice to send, and takes it off when the queue has none.
func (e *Engine) armFeedback(now time.Time) {
	key := schedKey{schedFeedback, ""}
	next, err := e.fb.q.NextFlush(e.p.Feedback)
	switch {
	case err != nil:
		e.log.Error("the feedback parameters are not usable; no notice is sent", "error", err.Error())
	case next.IsZero():
		e.sched.remove(key)
	default:
		e.sched.set(key, next)
	}
}

// runFeedback sends the notices that are due. A write that failed for a reason a
// retry may cure leaves the items queued and is tried again after RetryBackoff; a
// fatal storage error is the loop's, which classify records, and is returned.
func (e *Engine) runFeedback(now time.Time) error {
	err := e.flushFeedback(now)
	if err == nil {
		return nil
	}
	e.classify(err)
	if e.fatal != nil {
		return err
	}
	e.log.Warn("notices could not be written; the feedback stays queued and is tried again", "error", err.Error())
	e.sched.set(schedKey{schedFeedback, ""}, now.Add(e.p.RetryBackoff))
	return nil
}

// flushFeedback takes the due notices out of the queue and writes them one by one.
// The queue is changed on a copy that replaces it when the pass ends: a notice that
// could not be written, and the ones after it, go back with their items and without
// the mark of having been sent, so that a failed write tells nobody and loses
// nothing.
func (e *Engine) flushFeedback(now time.Time) error {
	q := delivery.FeedbackQueue{Pending: slices.Clone(e.fb.q.Pending), Sent: slices.Clone(e.fb.q.Sent)}
	notices, err := q.Flush(now, e.p.Feedback)
	if err != nil {
		return err
	}
	var failure error
	for i, n := range notices {
		if failure = e.sendNotice(now, n); failure != nil {
			unsend(&q, notices[i:], now)
			break
		}
	}
	e.fb.q = q
	if next, err := q.NextFlush(e.p.Feedback); err == nil && !next.IsZero() && !next.After(now) && failure == nil {
		// A notice that is due, with none sent, would be asked for again at once and
		// for ever: the queue and its rules disagree. Say so and look again later.
		e.log.Error("a notice is due and the queue sent none; it is looked at again later")
		e.sched.set(schedKey{schedFeedback, ""}, now.Add(e.p.RetryBackoff))
		return nil
	}
	e.armFeedback(now)
	return failure
}

// unsend puts back the notices Flush took out at now: their items are queued again
// and the mark that each was sent is taken off.
func unsend(q *delivery.FeedbackQueue, notices []delivery.Notice, now time.Time) {
	for _, n := range notices {
		for _, it := range n.Items {
			// An item Flush took out is not pending any more, so this cannot refuse a
			// duplicate; an item the queue refuses was queued by it.
			_ = q.Add(it)
		}
		if i := slices.IndexFunc(q.Sent, func(s delivery.SentNotice) bool { return s.Sender == n.Sender && s.At.Equal(now) }); i >= 0 {
			q.Sent = slices.Delete(q.Sent, i, i+1)
		}
	}
}

// sendNotice tells the endpoint of the notice: the notice is written, and then the
// items it covers are taken off the records that hold them. A crash between the
// two is repaired at the start by the replay of feedback, which reads the covers.
// The write of the notice is the only one whose failure leaves the endpoint untold;
// a failure to clear the records leaves it told and the items on their records
// until the clearing is tried again, which is not left to the next start: the
// notice may be swept by then, and with it the covers that tell the replay these
// items were told.
func (e *Engine) sendNotice(now time.Time, n delivery.Notice) error {
	st := e.view.endpoints[n.Sender.Endpoint]
	if st == nil || st.rec.Status != store.EndpointOpen {
		// Closing ends the endpoint's life for feedback: an endpoint opened again with
		// the same id does not hear of what happened before it was closed, as the
		// records queued for a closing endpoint are not carried over to it either. The
		// items would stay on their records for ever if they were not taken off.
		e.log.Warn("feedback for an endpoint that is not open is dropped: closing ended its life for feedback", "endpoint", n.Sender.Endpoint.String(), "items", len(n.Items))
		return e.clearCovered(now, n.Items)
	}
	key := noticeKey(e.d.Node, n)
	if _, held := e.view.in[key]; !held {
		in, body, err := e.formNotice(now, st, n, key)
		if err != nil {
			return err
		}
		if err := e.d.Store.FeedbackForm(body, in); err != nil {
			return err
		}
		e.view.setIn(in)
	}
	return e.clearCovered(now, n.Items)
}

// formNotice is the target record of a notice of the system endpoint to an
// endpoint, with the body it says. The record is exempt traffic: the delivery rules
// take it in whatever the endpoint's queue holds (delivery.Intake does not read the
// backlog for it) and it takes no place of that queue.
func (e *Engine) formNotice(now time.Time, st *endpointState, n delivery.Notice, key store.TargetKey) (store.InRecord, []byte, error) {
	body := noticeText(n.Items)
	ref := store.BodyRefOf(body)
	accept := now.Add(delivery.DefaultAcceptBefore)
	verdict, err := delivery.Intake(now, accept, authz.TrafficSystemFeedback, delivery.Authorization{}, delivery.Backlog{})
	if err != nil || verdict.Outcome != delivery.IntakeAccept {
		return store.InRecord{}, nil, fmt.Errorf("engine: the system endpoint's notice %s is not taken in: %v (%v)", key.Message, verdict.Outcome, err)
	}
	maxQueue := e.d.Config.EndpointSettings(st.rec.Instance, st.rec.ID).MaxQueue
	rec, err := delivery.NewTargetNoticeRecord(now, accept, delivery.ReportBy(accept), maxQueue)
	if err != nil {
		return store.InRecord{}, nil, fmt.Errorf("engine: the record of notice %s: %w", key.Message, err)
	}
	in := store.InRecord{
		Key: key, Record: rec,
		Dedup:      delivery.NewDedupRecord(key.Dedup(), delivery.BodyDigest(sha256.Sum256(body)), rec.ReportBy),
		AcceptedAt: now,
		Handout:    store.HandoutFields{ContentType: "text/plain", SentAt: now},
		Body:       &ref,
	}
	for _, it := range n.Items {
		name, ok := e.recordOfItem(it)
		if !ok {
			e.log.Warn("an item of feedback names a record this node does not hold; the notice does not cover it", "message", string(it.Ref.Message))
			continue
		}
		in.Covers = append(in.Covers, store.CoverOf(name, it))
	}
	return in, body, nil
}

// reissueCovered owes a sender again what a notice told it, for a notice that is lost
// with its body: the items it covered were taken off their source records when it
// was written, and no other record would say they were never read. Each goes back on
// the record it came from, written before the notice is settled, so that a crash in
// between finds the notice damaged again and writes the same items again, and the
// queue then tells it with a new notice. A cover whose source record is gone (swept,
// or lost and replaced by another) is left out with a word in the log, and so is the
// cover of an out-lost record, whose item is built from the name of a file that
// cannot be read again once the record is swept; the owner holds the alerts of both
// losses.
func (e *Engine) reissueCovered(now time.Time, notice store.InRecord) error {
	outs := map[string]store.OutRecord{}
	for _, c := range notice.Covers {
		o, ok := outs[c.Out]
		if !ok {
			if o, ok = e.view.out[c.Out]; !ok {
				e.log.Warn("a notice that was lost covered an item of a record this node does not hold; it is not owed again", "kind", c.Kind.String())
				continue
			}
		}
		it := e.coveredItem(now, o, c)
		// An item a failed clearing left on the record is the one the cover names.
		if slices.ContainsFunc(o.Feedback, func(x delivery.Item) bool { return c.Covers(c.Out, x) }) {
			continue
		}
		o.Feedback = append(slices.Clone(o.Feedback), it)
		outs[c.Out] = o
	}
	for _, o := range sortedOuts(outs) {
		if err := e.d.Store.SourceUpdate(o); err != nil {
			return err
		}
		e.putOut(now, o)
	}
	return nil
}

// coveredItem is the item that a cover stands for, as the delivery rules made it as
// far as the record still says: the kind is the cover's, the reason of a rejection and
// the state of the delivery are the record's now.
func (e *Engine) coveredItem(now time.Time, o store.OutRecord, c store.CoverRef) delivery.Item {
	fb := delivery.Feedback{Kind: c.Kind}
	if o.Lost == nil {
		for _, t := range o.Outbound.Deliveries() {
			if t.Address == c.Recipient {
				fb.State = t.Delivery.State
				if c.Kind == delivery.KindRejected {
					fb.Reject = t.Delivery.Reject
				}
			}
		}
	}
	return delivery.Item{
		Ref:      delivery.DeliveryRef{Sender: ident.Address{Node: e.d.Node, Endpoint: o.Key.Sender}, Message: o.Key.Message, Recipient: c.Recipient},
		Feedback: fb, At: now,
	}
}

// recordOfItem is the file name of the record that holds an item: the out-lost
// record that names itself, or the source record of the message the item is about.
func (e *Engine) recordOfItem(it delivery.Item) (string, bool) {
	if it.Source != "" {
		_, ok := e.fb.outLost[it.Source]
		return it.Source, ok
	}
	name, ok := e.view.byMessage[msgKey{it.Ref.Sender.Endpoint, it.Ref.Message}]
	return name, ok
}

// clearCovered takes the items off the records that hold them, which is what the
// notice that covers them lets the next write do. A write that failed for a reason a
// retry may cure leaves its items on their records and is tried again after
// RetryBackoff (retryClears); only a fatal storage error is returned.
func (e *Engine) clearCovered(now time.Time, items []delivery.Item) error {
	outs := map[string]store.OutRecord{}
	lost := map[string]store.OutLostRecord{}
	var ofOuts, ofLost []delivery.Item
	for _, it := range items {
		name, ok := e.recordOfItem(it)
		if !ok {
			continue
		}
		if it.Source != "" {
			l, seen := lost[name]
			if !seen {
				l = e.fb.outLost[name]
			}
			l.Feedback = withoutItem(name, l.Feedback, it)
			lost[name] = l
			ofLost = append(ofLost, it)
			continue
		}
		o, seen := outs[name]
		if !seen {
			o = e.view.out[name]
		}
		o.Feedback = withoutItem(name, o.Feedback, it)
		outs[name] = o
		ofOuts = append(ofOuts, it)
	}
	var failed []delivery.Item
	if len(outs) > 0 {
		if err := e.d.Store.FeedbackClear(sortedOuts(outs)); err != nil {
			if txn.IsFatal(err) {
				return err
			}
			e.log.Warn("the records could not be cleared of the feedback a notice covers; it is tried again", "error", err.Error())
			failed = append(failed, ofOuts...)
		} else {
			for _, o := range sortedOuts(outs) {
				e.putOut(now, o)
			}
		}
	}
	if len(lost) > 0 {
		if err := e.d.Store.OutLostClear(sortedLosts(lost)); err != nil {
			if txn.IsFatal(err) {
				return err
			}
			e.log.Warn("the records could not be cleared of the feedback a notice covers; it is tried again", "error", err.Error())
			failed = append(failed, ofLost...)
		} else {
			for name, l := range lost {
				if len(l.Feedback) == 0 {
					delete(e.fb.outLost, name)
				} else {
					e.fb.outLost[name] = l
				}
			}
		}
	}
	if len(failed) > 0 {
		e.fb.uncleared = append(e.fb.uncleared, failed...)
		e.sched.set(schedKey{schedFeedbackClear, ""}, now.Add(e.p.RetryBackoff))
	}
	return nil
}

// retryClears takes off their records the items that a notice covers and an earlier
// write could not clear. A fatal storage error is the loop's, which classify
// records.
func (e *Engine) retryClears(now time.Time) {
	items := e.fb.uncleared
	e.fb.uncleared = nil
	if err := e.clearCovered(now, items); err != nil {
		e.classify(err)
	}
}

// withoutItem is the items of the record of out/ with the given file name without
// the one the cover of it names: a cover clears an item by the store's rule, the one
// the replay of feedback reads.
func withoutItem(record string, items []delivery.Item, it delivery.Item) []delivery.Item {
	cover := store.CoverOf(record, it)
	return slices.DeleteFunc(slices.Clone(items), func(x delivery.Item) bool { return cover.Covers(record, x) })
}

func sortedOuts(m map[string]store.OutRecord) []store.OutRecord {
	out := make([]store.OutRecord, 0, len(m))
	for _, o := range m {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

func sortedLosts(m map[string]store.OutLostRecord) []store.OutLostRecord {
	out := make([]store.OutLostRecord, 0, len(m))
	for _, o := range m {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// noticeKey is the key of the record of a notice: the system endpoint of this node
// as the sender, the endpoint told as the recipient, and a message identifier made
// from the notice alone. The same items give the same identifier, so a notice that
// is formed again after a crash is the same record, not a second one.
func noticeKey(node ident.NodeID, n delivery.Notice) store.TargetKey {
	lines := make([]string, 0, len(n.Items))
	for _, it := range n.Items {
		lines = append(lines, fmt.Sprintf("%s %s %s %s", it.Ref.Message, it.Ref.Recipient, it.Feedback.Kind, it.Source))
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(n.Sender.String() + "\n" + strings.Join(lines, "\n")))
	return store.TargetKey{
		SenderNode: node, Sender: ident.SystemEndpoint(),
		Message:   delivery.MessageID("n-" + hex.EncodeToString(sum[:16])),
		Recipient: n.Sender.Endpoint,
	}
}

// noticeText is what the notice says to the agent: for each kind of feedback the
// words that say what the agent may conclude (delivery.Kind.Summary, the one place
// they are written), then one line for each delivery it is about.
func noticeText(items []delivery.Item) []byte {
	var kinds []delivery.Kind
	for _, it := range items {
		if !slices.Contains(kinds, it.Feedback.Kind) {
			kinds = append(kinds, it.Feedback.Kind)
		}
	}
	var b strings.Builder
	b.WriteString("Notice from the channel of this node, not a message from another endpoint.\n")
	for _, k := range kinds {
		fmt.Fprintf(&b, "\n%s: %s\n", k, k.Summary())
		for _, it := range items {
			if it.Feedback.Kind != k {
				continue
			}
			b.WriteString("- " + itemLine(it) + "\n")
		}
	}
	return []byte(b.String())
}

// itemLine names what an item is about: the message and its recipient, with the
// reason or the cause the delivery rules gave, or, for the loss of a message nobody
// can name, that it cannot be named.
func itemLine(it delivery.Item) string {
	if it.Feedback.Kind == delivery.KindLostUnknownMessage {
		return "a message of this endpoint that cannot be named"
	}
	s := fmt.Sprintf("message %s to %s", it.Ref.Message, it.Ref.Recipient)
	switch {
	case it.Feedback.Reject != delivery.RejectNone:
		s += " (" + it.Feedback.Reject.String() + ")"
	case it.Feedback.Cause != delivery.CauseNone:
		s += " (cause: " + it.Feedback.Cause.String() + ")"
	}
	return s
}
