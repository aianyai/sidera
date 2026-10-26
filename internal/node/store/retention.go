package store

import (
	"encoding/hex"
	"path"
	"slices"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/reply"
)

// AdminResultRetention is how long the result of an administrative operation is
// kept: long enough for the command line to resend an operation whose outcome it
// did not learn and for the owner to ask for a result after a weekend.
const AdminResultRetention = 7 * 24 * time.Hour

// AlertAckRetention is how long an acknowledged alert is kept.
const AlertAckRetention = 7 * 24 * time.Hour

func latest(ts ...time.Time) time.Time {
	var m time.Time
	for _, t := range ts {
		if t.After(m) {
			m = t
		}
	}
	return m
}

// targetRetention is the retention of a target delivery: the latest of what the
// deduplication record, the answer to a source's pull, the status report and the
// facts reply permission reads need, each as the delivery and reply packages
// define it. The pull window is today always shorter than the facts window, so
// it never decides the result; it is a term of its own so that the delivery
// rule holds whatever the facts window becomes.
func targetRetention(reportBy, createdAt time.Time, dedup delivery.DedupRecord) time.Time {
	return latest(
		delivery.DedupRetainUntil(reportBy),
		delivery.RecordRetainUntil(reportBy),
		dedup.RetainUntil,
		delivery.StatusReportRetainUntil(createdAt, reportBy),
		reply.RetainFactsUntil(reportBy),
	)
}

// RetainUntil is the instant until which the records of a delivery are kept as
// live files, and whether they are finished: every delivery in a final state, or
// the sender's replacement for a record lost to storage corruption. A
// replacement is not final in the delivery rules: Tick still closes it at its
// ReportBy. Treating it as finished is safe because that change does not move
// what the retention is computed from (ReportBy is fixed for a replacement) and
// the sweep waits until the returned instant, which is not earlier than the
// retention that follows from that ReportBy. It is the latest of the retentions
// the delivery and reply packages define for the facts the record holds. A
// record that is not finished is never swept.
func RetainUntil(rec Record) (until time.Time, finished bool) {
	switch r := rec.(type) {
	case InRecord:
		return targetRetention(r.Record.ReportBy, r.AcceptedAt, r.Dedup), r.Record.State.IsTerminal()
	case CtlRecord:
		return targetRetention(r.Record.ReportBy, r.AcceptedAt, r.Dedup), r.Record.State.IsTerminal()
	case OutRecord:
		var ds []delivery.Record
		finished = true
		if r.Lost != nil {
			ds = r.Lost.Deliveries
		} else {
			for _, t := range r.Outbound.Recipients {
				ds = append(ds, t.Delivery)
			}
			if r.Outbound.Derived != nil {
				ds = append(ds, r.Outbound.Derived.Delivery)
			}
			for _, d := range ds {
				finished = finished && d.State.IsTerminal()
			}
		}
		for _, d := range ds {
			until = latest(until, delivery.IdempotencyKeyRetainUntil(d.ReportBy), reply.RetainFactsUntil(d.ReportBy))
		}
		if r.Idempotency != nil {
			until = latest(until, r.Idempotency.RetainUntil)
		}
		return until, finished
	case OutLostRecord:
		// Nothing waits for a reply to it, so it is kept for as long as its notice
		// is not given and is swept as soon as that is done.
		return r.DiscoveredAt, len(r.Feedback) == 0
	case AdminRecord:
		return r.RetainUntil, true
	case AlertRecord:
		if r.Acked == nil {
			return time.Time{}, false
		}
		return r.Acked.At.Add(AlertAckRetention), true
	}
	return time.Time{}, false
}

// SweepPlan is the work of one pass of the archive sweep.
type SweepPlan struct {
	// Summaries are the archive summaries of the records the plan removes, for the
	// caller to append (ArchiveSummaries) before it sweeps. A chunk does not carry
	// them: no record waits for its summary.
	Summaries []ArchiveSummary
	Ctl       []FileRef
	// Records are the files of every directory but out/ and endpoints/.
	Records []FileRef
	// Outs are the source records to remove, whole: the sweep writes the sequence
	// floors of their numbers before it removes them.
	Outs []OutRecord
	// OutLosts are the out-lost records whose notice has been given, or whose
	// sender is closed or gone, so that it never will be.
	OutLosts []OutLostRecord
	// Endpoints are the records of the closed endpoints to remove, which go after
	// the files that hang on them.
	Endpoints []FileRef
}

// Chunk is the whole plan as one chunk of the sweep. A caller that sweeps in
// smaller chunks cuts the groups of the chunk it takes from here; a source record
// is cut as a record, so its floor goes with it.
func (p SweepPlan) Chunk() SweepChunk {
	return SweepChunk{
		Ctl: p.Ctl, Records: p.Records,
		Outs: p.Outs, OutLosts: p.OutLosts, Endpoints: p.Endpoints,
	}
}

// PlanSweep selects what is ready to leave the live files at now: records that
// are finished and past their retention, with a session's derived delivery after
// its control record; admin results past theirs; acknowledged alerts; closed
// endpoints nothing live points to. The bodies of the records that go are not
// listed: the sweep removes each record's own.
//
// A closed session endpoint is kept while any source record, or out-lost record,
// has it as sender, swept or not: the sweep of a source record writes the floor
// of its sender, and a floor file written for an endpoint whose record is gone is
// one nothing would ever remove. The sweep that removes the last of its source
// records leaves it, and the next one removes the endpoint with its floor file.
// An out-lost record of a closed endpoint is swept whatever its item says, so
// that it does not keep the endpoint for good. A closed session endpoint whose
// native session is not recorded released is kept whatever else is true of it
// (EndpointRecord.ReleasePending): the record is where the work of releasing the
// session is found, and the sweep must not make the node forget it.
//
// Feedback is told before its record goes. A source record that still holds an item
// is kept while its sender is open, as an out-lost record is, and goes at once when
// the sender is closed or has no record: it can no longer be told. A sender whose
// endpoint file is damaged with a key that can be trusted is neither: its status is
// not known, so it is told first. A notice is kept
// while a record that stays holds an item it covers: the replay of feedback reads
// the cover to take the item off the record, and a notice swept first would have
// the next start tell the item again.
func PlanSweep(sc *Scan, now time.Time) SweepPlan {
	var plan SweepPlan
	ready := func(rec Record) bool {
		until, finished := RetainUntil(rec)
		return finished && !now.Before(until)
	}
	// Control records: the ones that go, and the ones that exist at all.
	ctlExists := map[string]bool{} // by (node, message, recipient endpoint of the control signal)
	ctlGoes := map[string]bool{}
	ctlKey := func(node ident.NodeID, m delivery.MessageID, e ident.EndpointID) string {
		return node.String() + " " + string(m) + " " + e.String()
	}
	for _, c := range sc.Ctl {
		k := ctlKey(c.Key.SenderNode, c.Key.Message, c.Key.Recipient)
		ctlExists[k] = true
		if ready(c) {
			ctlGoes[k] = true
		}
	}
	for _, d := range sc.Damaged {
		if d.Dir == CtlDir {
			if k, ok := d.Key.(TargetKey); ok {
				ctlExists[ctlKey(k.SenderNode, k.Message, k.Recipient)] = true
			}
		}
	}
	add := func(refs *[]FileRef, dir, name string) {
		*refs = append(*refs, FileRef{Dir: dir, Name: name})
	}
	summary := func(rec Record) {
		plan.Summaries = append(plan.Summaries, summarize(rec, now))
	}

	// What leaves among the source and out-lost records is decided first, because a
	// notice is kept for the feedback items that the records that stay still hold.
	// A sender that is closed, or has no record, can no longer be told: closing is
	// irreversible, so what the record holds for it can never be given and goes with
	// it. Any other sender is told first.
	// An endpoint whose file is damaged with a key that can be trusted is not known
	// to be closed, so it counts as open: its sender may still be owed the feedback.
	endpointOpen := map[ident.EndpointID]bool{}
	for _, e := range sc.Endpoints {
		endpointOpen[e.ID] = e.Status != EndpointClosed
	}
	for _, d := range sc.Damaged {
		if id, ok := d.Key.(ident.EndpointID); ok && d.Dir == EndpointsDir {
			endpointOpen[id] = true
		}
	}
	outGoes := func(o OutRecord) bool {
		return ready(o) && (len(o.Feedback) == 0 || !endpointOpen[o.Key.Sender])
	}
	outLostGoes := func(o OutLostRecord) bool { return ready(o) || !endpointOpen[o.Key.Sender] }
	stillHeld := map[CoverRef]bool{} // the items of the records that stay, as a notice covers them
	holdItems := func(name string, items []delivery.Item) {
		for _, it := range items {
			stillHeld[CoverRef{Out: name, Recipient: it.Ref.Recipient, Kind: it.Feedback.Kind}] = true
		}
	}
	for _, o := range sc.Out {
		if !outGoes(o) {
			holdItems(o.Name(), o.Feedback)
		}
	}
	for _, o := range sc.OutLost {
		if !outLostGoes(o) {
			holdItems(o.Name(), o.Feedback)
		}
	}

	for _, c := range sc.Ctl {
		if ready(c) {
			add(&plan.Ctl, CtlDir, c.Name())
			summary(c)
		}
	}
	for _, in := range sc.In {
		if !ready(in) {
			continue
		}
		// A notice is what tells the replay of feedback that an item was told: swept
		// while a record still holds the item, it would have the next start tell it
		// again.
		if slices.ContainsFunc(in.Covers, func(c CoverRef) bool { return stillHeld[c] }) {
			continue
		}
		if in.Derived != nil {
			k := ctlKey(in.Key.SenderNode, in.Key.Message, in.Derived.From)
			if ctlExists[k] && !ctlGoes[k] {
				continue // its control record is live: the delivery is not swept before it
			}
		}
		add(&plan.Records, InDir, in.Name())
		summary(in)
	}
	for _, o := range sc.Out {
		if outGoes(o) {
			summary(o)
			plan.Outs = append(plan.Outs, o)
		}
	}
	for _, o := range sc.OutLost {
		if outLostGoes(o) {
			plan.OutLosts = append(plan.OutLosts, o)
		}
	}
	for _, a := range sc.Admin {
		if ready(a) {
			add(&plan.Records, AdminDir, a.Name())
		}
	}
	for _, a := range sc.Alerts {
		if ready(a) {
			add(&plan.Records, AlertsDir, a.Name())
		}
	}
	// Closed endpoints nothing live points to, with what belongs to them.
	live := map[ident.EndpointID]bool{}
	for _, in := range sc.In {
		if _, finished := RetainUntil(in); !finished {
			live[in.Key.Recipient] = true
		}
	}
	for _, c := range sc.Ctl {
		if _, finished := RetainUntil(c); !finished {
			live[c.Key.Recipient] = true
		}
	}
	sends := map[ident.EndpointID]bool{} // senders of a source record, whole or damaged, or of an out-lost record
	for _, o := range sc.Out {
		sends[o.Key.Sender] = true
	}
	for _, o := range sc.OutLost {
		sends[o.Key.Sender] = true
	}
	for _, d := range sc.Damaged {
		if d.Dir != OutDir {
			continue
		}
		switch k := d.Key.(type) {
		case OutKey:
			sends[k.Sender] = true
		case OutLostKey:
			sends[k.Sender] = true
		}
	}
	for _, e := range sc.Endpoints {
		if e.Status != EndpointClosed || live[e.ID] || (e.Kind == adapterreg.KindSession && sends[e.ID]) || e.ReleasePending() {
			continue
		}
		add(&plan.Endpoints, EndpointsDir, e.Name())
		add2 := func(dir string, rec Record) { add(&plan.Records, dir, rec.Name()) }
		for _, a := range sc.Attempts {
			if a.Endpoint == e.ID {
				add2(AttemptsDir, a)
			}
		}
		for _, c := range sc.Checkpoints {
			if c.Key.Scope == CheckpointScopeSession && c.Key.ID == e.ID.String() {
				add2(CheckpointsDir, c)
			}
		}
		if e.Kind == adapterreg.KindSession {
			for _, f := range sc.Seq {
				if f.Sender == e.ID {
					add2(SeqDir, f)
				}
			}
		}
	}
	sortRefs := func(r []FileRef) {
		sort.Slice(r, func(i, j int) bool { return path.Join(r[i].Dir, r[i].Name) < path.Join(r[j].Dir, r[j].Name) })
	}
	sortRefs(plan.Ctl)
	sortRefs(plan.Records)
	sortRefs(plan.Endpoints)
	return plan
}

// summarize is the archive summary of a record that goes. The times that the
// record does not hold (when it closed) are left out.
func summarize(rec Record, now time.Time) ArchiveSummary {
	s := ArchiveSummary{At: now}
	setDelivery := func(d delivery.Record) {
		s.State = d.State.String()
		if d.State == delivery.StateRejected {
			s.Reason = d.Reject.String()
		}
	}
	switch r := rec.(type) {
	case InRecord:
		s.Kind, s.Key = KindIn, r.Key.String()
		setDelivery(r.Record)
		s.AcceptedAt = r.AcceptedAt
		s.Digest = hex.EncodeToString(r.Dedup.Digest[:])
		if r.Body != nil {
			s.Size = r.Body.Len
		}
	case CtlRecord:
		s.Kind, s.Key = KindCtl, r.Key.String()
		setDelivery(r.Record)
		s.AcceptedAt = r.AcceptedAt
		s.Digest = hex.EncodeToString(r.Dedup.Digest[:])
	case OutRecord:
		kb, _ := r.Key.Bytes()
		s.Kind, s.Key = r.Key.Kind(), string(kb)
		if r.Lost != nil {
			setDelivery(r.Lost.Deliveries[0])
			s.Reason, s.Quarantined = "storage_loss", true
			break
		}
		setDelivery(r.Outbound.Recipients[0].Delivery)
		s.AcceptedAt = r.Envelope.SentAt
		s.DeliveredAt = r.Outbound.Recipients[0].Arrival.DeliveredAt
		s.Digest = hex.EncodeToString(r.BodyDigest[:])
		if r.Body != nil {
			s.Size = r.Body.Len
		}
	}
	return s
}
