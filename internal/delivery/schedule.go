package delivery

import (
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// deadline is an instant from which a rule of Tick applies to a record as it
// is now. applies is false when the record's state is not one the rule looks at.
type deadline struct {
	at      time.Time
	applies bool
}

// reachedBy reports whether the rule applies and its instant is not after now.
func (d deadline) reachedBy(now time.Time) bool {
	return d.applies && !now.Before(d.at)
}

// The deadlines Tick consults, each read from the record's current state. Tick,
// the hand-out check and NextTick all go through them, so a deadline is
// defined once: what it is, when it applies and which instant it is.
// tickDeadlines lists them all.

// controlSettleDeadline: an accepted control signal that has no reply at
// SettleBy becomes delivery_unknown.
func (r Record) controlSettleDeadline() deadline {
	return deadline{SettleBy(r.ReportBy), r.Side == SideTarget && r.Control && r.State == StateAccepted}
}

// leaseSettleDeadline: a lease still open at SettleBy becomes delivery_unknown.
func (r Record) leaseSettleDeadline() deadline {
	return deadline{SettleBy(r.ReportBy), r.hasQueue() && r.State == StateAccepted && r.Lease.Active}
}

// leaseExpiryDeadline: an open lease runs out at ExpiresAt, and the message goes
// back to the queue or, after the last allowed lease, becomes delivery_unknown.
func (r Record) leaseExpiryDeadline() deadline {
	return deadline{r.Lease.ExpiresAt, r.hasQueue() && r.State == StateAccepted && r.Lease.Active}
}

// backlogDeadline: a message in the queue without a lease is closed at
// BacklogDeadline. A lease holds the rule off until it ends.
func (r Record) backlogDeadline() deadline {
	return deadline{r.BacklogDeadline, r.hasQueue() && r.State == StateAccepted && !r.Lease.Active}
}

// closeDeadline: a record that is not final closes after ReportBy. Time has
// nanosecond resolution, so the first instant after ReportBy is the next
// nanosecond, and "not before it" is "after ReportBy".
func (r Record) closeDeadline() deadline {
	return deadline{r.ReportBy.Add(time.Nanosecond), !r.State.IsTerminal()}
}

// tickDeadlines lists every timed rule of Tick, in no particular order. A new
// timed rule of Tick is a deadline method above and an entry here; NextTick then
// answers for it, and the property-test generator and the required paths of the
// NextTick tests are extended in the same change.
func (r Record) tickDeadlines() []deadline {
	return []deadline{
		r.controlSettleDeadline(),
		r.leaseSettleDeadline(),
		r.leaseExpiryDeadline(),
		r.backlogDeadline(),
		r.closeDeadline(),
	}
}

// NextTick returns the earliest instant at which Tick could change the record:
// the first of the deadlines Tick consults that applies to the record in its
// present state. The zero time means no time-driven change is pending, which is
// the case exactly when the record is final.
//
// The instant can lie in the past of the caller's clock when a deadline was
// missed; Tick then has work to do at once. After Tick, and after any other
// change to the record, ask again: the answer follows the state.
func (r Record) NextTick() time.Time {
	var next time.Time
	for _, d := range r.tickDeadlines() {
		if d.applies && (next.IsZero() || d.at.Before(next)) {
			next = d.at
		}
	}
	return next
}

// NextFlush returns the earliest instant at which Flush would send a notice:
// the zero time when nothing is pending. For each sender endpoint with items
// waiting that is the later of the end of the merge window of its oldest item
// and the instant at which it has fewer than RateCap notices left in the rate
// window; the earliest over the senders is the answer. Like Flush it refuses
// parameters that are not all set.
//
// A notice that Flush sends changes the answer, and so does Add; ask again
// after either.
func (q *FeedbackQueue) NextFlush(p FeedbackParams) (time.Time, error) {
	if err := p.Validate(); err != nil {
		return time.Time{}, err
	}
	opened := map[ident.Address]time.Time{}
	for _, it := range q.Pending {
		if first, ok := opened[it.Ref.Sender]; !ok || it.At.Before(first) {
			opened[it.Ref.Sender] = it.At
		}
	}
	var next time.Time
	for sender, first := range opened {
		due := mergeDueAt(first, p)
		if free := q.rateFreeAt(sender, p); free.After(due) {
			due = free
		}
		if next.IsZero() || due.Before(next) {
			next = due
		}
	}
	return next, nil
}

// rateFreeAt returns the first instant at which the sender endpoint has fewer
// than RateCap notices counting toward the cap: when the RateCap-th most recent
// of its sent notices leaves the rate window. The zero time when it is free
// whatever the instant.
func (q *FeedbackQueue) rateFreeAt(sender ident.Address, p FeedbackParams) time.Time {
	var sentAt []time.Time
	for _, s := range q.Sent {
		if s.Sender == sender {
			sentAt = append(sentAt, s.At)
		}
	}
	if len(sentAt) < p.RateCap {
		return time.Time{}
	}
	sort.Slice(sentAt, func(i, j int) bool { return sentAt[i].After(sentAt[j]) })
	return rateReleaseAt(sentAt[p.RateCap-1], p)
}
