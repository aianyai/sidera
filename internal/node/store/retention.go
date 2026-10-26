package store

import (
	"encoding/hex"
	"path"
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
	Summaries []ArchiveSummary
	Ctl       []FileRef
	// Records are the files of every directory but out/ and endpoints/.
	Records []FileRef
	// Outs are the source records to remove, whole: the sweep writes the sequence
	// floors of their numbers before it removes them.
	Outs []OutRecord
	// OutLosts are the out-lost records whose notice has been given.
	OutLosts []OutLostRecord
	// Endpoints are the records of the closed endpoints to remove, which go after
	// the files that hang on them.
	Endpoints []FileRef
	Bodies    []BodyRef
}

// Chunk is the whole plan as one chunk of the sweep at now. A caller that sweeps
// in smaller chunks cuts the groups of the chunk it takes from here; a source
// record is cut as a record, so its floor goes with it.
func (p SweepPlan) Chunk(now time.Time) SweepChunk {
	return SweepChunk{
		Now: now, Summaries: p.Summaries, Ctl: p.Ctl, Records: p.Records,
		Outs: p.Outs, OutLosts: p.OutLosts, Endpoints: p.Endpoints, Bodies: p.Bodies,
	}
}

// PlanSweep selects what is ready to leave the live files at now: records that
// are finished and past their retention, with a session's derived delivery after
// its control record; admin results past theirs; acknowledged alerts; closed
// endpoints nothing live points to; and the bodies nothing refers to afterwards.
//
// A closed session endpoint is kept while any source record, or out-lost record,
// has it as sender, swept or not: the sweep of a source record writes the floor of its sender, and
// a floor file written for an endpoint whose record is gone is one nothing would
// ever remove. The sweep that removes the last of its source records leaves it,
// and the next one removes the endpoint with its floor file.
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
	swept := map[string]bool{} // by path
	add := func(refs *[]FileRef, dir, name string) {
		*refs = append(*refs, FileRef{Dir: dir, Name: name})
		swept[path.Join(dir, name)] = true
	}
	var bodiesOfSwept []BodyRef
	summary := func(rec Record) {
		plan.Summaries = append(plan.Summaries, summarize(rec, now))
	}

	for _, c := range sc.Ctl {
		if ready(c) {
			add(&plan.Ctl, CtlDir, c.Name())
			summary(c)
			if c.Body != nil {
				bodiesOfSwept = append(bodiesOfSwept, *c.Body)
			}
		}
	}
	for _, in := range sc.In {
		if !ready(in) {
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
		if in.Body != nil {
			bodiesOfSwept = append(bodiesOfSwept, *in.Body)
		}
	}
	for _, o := range sc.Out {
		if !ready(o) {
			continue
		}
		swept[path.Join(OutDir, o.Name())] = true
		summary(o)
		plan.Outs = append(plan.Outs, o)
		if o.Body != nil {
			bodiesOfSwept = append(bodiesOfSwept, *o.Body)
		}
	}
	for _, o := range sc.OutLost {
		if ready(o) {
			swept[path.Join(OutDir, o.Name())] = true
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
		if e.Status != EndpointClosed || live[e.ID] || (e.Kind == adapterreg.KindSession && sends[e.ID]) {
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
	// Bodies a swept record referred to and no record that stays refers to.
	stays := map[string]bool{}
	hold := func(name string, r *BodyRef) {
		if r != nil && !swept[name] {
			stays[r.Name()] = true
		}
	}
	for _, o := range sc.Out {
		hold(path.Join(OutDir, o.Name()), o.Body)
	}
	for _, in := range sc.In {
		hold(path.Join(InDir, in.Name()), in.Body)
	}
	for _, c := range sc.Ctl {
		hold(path.Join(CtlDir, c.Name()), c.Body)
	}
	seen := map[string]bool{}
	for _, b := range bodiesOfSwept {
		if !stays[b.Name()] && !seen[b.Name()] {
			seen[b.Name()] = true
			plan.Bodies = append(plan.Bodies, b)
		}
	}
	sortRefs := func(r []FileRef) {
		sort.Slice(r, func(i, j int) bool { return path.Join(r[i].Dir, r[i].Name) < path.Join(r[j].Dir, r[j].Name) })
	}
	sortRefs(plan.Ctl)
	sortRefs(plan.Records)
	sortRefs(plan.Endpoints)
	sort.Slice(plan.Bodies, func(i, j int) bool { return plan.Bodies[i].Name() < plan.Bodies[j].Name() })
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
		if r.Body != nil {
			s.Size = r.Body.Len
		}
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
