package delivery

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// scheduleSeeds is how many random records and queues each property test
// drives. minScheduleCount is how often every path listed in a coverage
// requirement must be reached, so a generator that stopped reaching one cannot
// pass silently.
const (
	scheduleSeeds    = 4000
	minScheduleCount = 20
)

// The instants the tables below name are written out as offsets from t0, from
// the accept_before of ten minutes: report_by lies 2 h + 60 min + 2 min later,
// and the target settles five minutes before it.
var (
	schedAcceptBefore = at(10 * time.Minute)
	schedReportBy     = at(10*time.Minute + 3*time.Hour + 2*time.Minute)
	schedSettleBy     = schedReportBy.Add(-5 * time.Minute)
	// afterReportBy is the first instant at which Tick closes a record: Tick
	// closes after report_by, and time has nanosecond resolution.
	afterReportBy = schedReportBy.Add(time.Nanosecond)
)

// tickChanges is the oracle for "calling Tick could change the record": it runs
// the real Tick at an instant and compares the whole record. It does not look
// at NextTick.
func tickChanges(r Record, now time.Time) bool {
	n, outs, err := r.Tick(now)
	if err != nil {
		panic(fmt.Sprintf("Tick(%v) failed on %+v: %v", now, r, err))
	}
	return len(outs) > 0 || !reflect.DeepEqual(r, n)
}

func mustTick(t *testing.T, r Record, now time.Time) Record {
	t.Helper()
	n, _, err := r.Tick(now)
	if err != nil {
		t.Fatalf("Tick(%v): %v", now.Sub(t0), err)
	}
	return n
}

func mustReject(t *testing.T, r Record, now time.Time) Record {
	t.Helper()
	n, _ := mustApply(t, r, now, Event{Type: EventRejected, Reject: RejectUnauthorized})
	return n
}

// TestNextTickNamesEachDeadline pins the instant of every deadline Tick
// consults, written out by hand, and the states in which no deadline is
// pending. Each row also checks the instant against Tick itself: nothing
// changes a nanosecond earlier, something changes at the instant.
func TestNextTickNamesEachDeadline(t *testing.T) {
	sourceQueued := NewSourceRecord(schedAcceptBefore)
	sourceExpired, _ := mustApply(t, sourceQueued, at(13*time.Minute), Event{Type: EventExpiryQueried, QueriedAt: at(13 * time.Minute)})
	sourceUnknown, _ := mustApply(t, sourceQueued, at(13*time.Minute), Event{Type: EventDeliveryUnknown, Unknown: UnknownHandedOver})
	sourceDelivered, _ := mustApply(t, sourceQueued, at(time.Minute), Event{Type: EventDelivered})
	sourceClosed := mustTick(t, sourceQueued, afterReportBy)

	queued := targetRecord(t, at(time.Minute), 30*time.Minute)
	// Accepted after its deadline (a caller bug the record still has to
	// survive), so the endpoint's queue time runs past the settle deadline and
	// the backlog deadline is clamped to it.
	clamped, err := NewTargetRecord(at(2*time.Hour), schedAcceptBefore, schedReportBy, MaxQueueCap)
	if err != nil {
		t.Fatal(err)
	}
	leased := handOut(t, targetRecord(t, at(0), 30*time.Minute), at(2*time.Minute), 1, 1)
	renewed, res := leased.Renew(at(4*time.Minute), true)
	if res != Renewed {
		t.Fatalf("Renew: %v", res)
	}
	// Queue time 5 min: the backlog deadline (5 min) lies before the lease
	// expiry (4 + 5 = 9 min), and a lease holds the backlog rule off.
	shortQueueLeased := handOut(t, targetRecord(t, at(0), 5*time.Minute), at(4*time.Minute), 1, 1)
	// The lease is issued so close to the settle deadline that it would expire
	// after it.
	lateAccepted, err := NewTargetRecord(at(90*time.Minute), schedAcceptBefore, schedReportBy, MaxQueueCap)
	if err != nil {
		t.Fatal(err)
	}
	leasedAtSettle := handOut(t, lateAccepted, schedSettleBy.Add(-time.Minute), 1, 1)
	requeued := mustTick(t, leased, at(7*time.Minute))
	// Four leases: the last one is the one whose expiry makes the delivery
	// unknown.
	lastLease := targetRecord(t, at(0), 30*time.Minute)
	for i := 0; i < MaxLeases; i++ {
		now := at(time.Duration(5*i) * time.Minute)
		lastLease = handOut(t, mustTick(t, lastLease, now), now, 1, 1)
	}
	unknownByLeases := mustTick(t, lastLease, at(20*time.Minute))
	targetRejected := mustTick(t, queued, at(31*time.Minute))
	targetDelivered := deliverBody(t, targetRecord(t, at(0), 30*time.Minute), at(time.Minute))
	targetClosed := mustTick(t, targetRecord(t, at(0), 30*time.Minute), afterReportBy)
	targetRejectedByHand := mustReject(t, targetRecord(t, at(0), 30*time.Minute), at(time.Minute))

	control := NewTargetControlRecord(schedAcceptBefore, schedReportBy)
	controlUnknown := mustTick(t, control, schedSettleBy)
	controlDelivered, _ := mustApply(t, control, at(time.Minute), Event{Type: EventReplyCommitted})

	rows := []struct {
		name string
		rec  Record
		want time.Time // zero: no deadline pending
	}{
		{"a queued source record closes after report_by", sourceQueued, afterReportBy},
		{"an expired_unconfirmed source record closes after report_by", sourceExpired, afterReportBy},
		{"a delivery_unknown source record closes after report_by", sourceUnknown, afterReportBy},
		{"a delivered source record has no deadline", sourceDelivered, time.Time{}},
		{"a closed_unconfirmed source record has no deadline", sourceClosed, time.Time{}},
		{"a queued target message waits for its backlog deadline", queued, at(31 * time.Minute)},
		{"a backlog deadline clamped to the settle deadline is the settle deadline", clamped, schedSettleBy},
		{"a leased message waits for the lease expiry", leased, at(7 * time.Minute)},
		{"a renewed lease waits for the renewed expiry", renewed, at(9 * time.Minute)},
		{"a lease holds the backlog deadline off", shortQueueLeased, at(9 * time.Minute)},
		{"a lease still open at the settle deadline is settled at it", leasedAtSettle, schedSettleBy},
		{"a requeued message waits for its backlog deadline again", requeued, at(30 * time.Minute)},
		{"the last allowed lease waits for its expiry", lastLease, at(20 * time.Minute)},
		{"a delivery_unknown target record closes after report_by", unknownByLeases, afterReportBy},
		{"a target record rejected by its backlog has no deadline", targetRejected, time.Time{}},
		{"a target record rejected by a fact has no deadline", targetRejectedByHand, time.Time{}},
		{"a delivered target record has no deadline", targetDelivered, time.Time{}},
		{"a closed_unconfirmed target record has no deadline", targetClosed, time.Time{}},
		{"an accepted control record is settled at the settle deadline", control, schedSettleBy},
		{"a delivery_unknown control record closes after report_by", controlUnknown, afterReportBy},
		{"a delivered control record has no deadline", controlDelivered, time.Time{}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got := row.rec.NextTick()
			if !got.Equal(row.want) {
				t.Fatalf("NextTick = %v, want %v (offsets from t0: %v, %v)", got, row.want, got.Sub(t0), row.want.Sub(t0))
			}
			if row.want.IsZero() {
				if tickChanges(row.rec, schedReportBy.Add(1000*time.Hour)) {
					t.Fatalf("no deadline reported, yet Tick changes the record far in the future")
				}
				return
			}
			if tickChanges(row.rec, row.want.Add(-time.Nanosecond)) {
				t.Errorf("Tick changes the record a nanosecond before %v", row.want.Sub(t0))
			}
			if !tickChanges(row.rec, row.want) {
				t.Errorf("Tick does not change the record at %v", row.want.Sub(t0))
			}
		})
	}
}

// scheduleOp is one thing the random record generator does at an instant.
type scheduleOp func(r Record, now time.Time) Record

// scheduleRecord builds a record of a random kind and drives it through random
// events and ticks at whole-minute instants, so that the final record is in any
// state and carries any combination of lease, backlog and report_by.
func scheduleRecord(rng *rand.Rand) Record {
	ab := t0.Add(time.Duration(1+rng.Intn(60)) * time.Minute)
	rb := ReportBy(ab)
	settle := SettleBy(rb)
	chunks := 1 + rng.Intn(2)
	now := t0
	var r Record
	var ops []scheduleOp
	// jumped is what a target message record does right after the clock jumps
	// to just before the settle deadline, so that a lease is issued that runs
	// past it.
	var jumped scheduleOp
	tick := func(r Record, now time.Time) Record {
		n, _, err := r.Tick(now)
		if err != nil {
			panic(err)
		}
		return n
	}
	apply := func(ev func(now time.Time) Event) scheduleOp {
		return func(r Record, now time.Time) Record {
			n, _, err := r.Apply(now, ev(now))
			if err != nil {
				return r
			}
			return n
		}
	}
	event := func(ev Event) func(time.Time) Event { return func(time.Time) Event { return ev } }

	switch rng.Intn(5) {
	case 4:
		return lostScheduleRecord(rng)
	case 3:
		return lateTargetRecord(rng, ab)
	case 0:
		r = NewSourceRecord(ab)
		ops = []scheduleOp{
			tick, tick,
			apply(func(now time.Time) Event { return Event{Type: EventExpiryQueried, QueriedAt: now} }),
			apply(func(now time.Time) Event { return Event{Type: EventExpiryQueried, QueriedAt: now} }),
			apply(func(now time.Time) Event { return Event{Type: EventExpiryQueried, QueriedAt: now} }),
			apply(func(now time.Time) Event { return Event{Type: EventExpiryQueried, QueriedAt: now} }),
			apply(event(Event{Type: EventReceipt})),
			apply(event(Event{Type: EventDelivered})),
			apply(event(Event{Type: EventRejected, Reject: RejectUnauthorized})),
			apply(event(Event{Type: EventDeliveryUnknown, Unknown: UnknownHandedOver})),
		}
	case 1:
		maxQueues := []time.Duration{5 * time.Minute, 30 * time.Minute, time.Hour, MaxQueueCap}
		accepted := t0.Add(time.Duration(rng.Intn(int(ab.Sub(t0)/time.Minute)+1)) * time.Minute)
		var err error
		r, err = NewTargetRecord(accepted, ab, rb, maxQueues[rng.Intn(len(maxQueues))])
		if err != nil {
			panic(err)
		}
		now = accepted
		handout := func(auth AuthorizationKind) scheduleOp {
			return func(r Record, now time.Time) Record {
				n, _, err := r.Handout(now, auth, chunks, 1+rng.Intn(chunks), freshLease())
				if err != nil {
					panic(err)
				}
				return n
			}
		}
		jumped = handout(AuthorizationAllow)
		ops = []scheduleOp{
			tick, handout(AuthorizationAllow), handout(AuthorizationAllow), handout(AuthorizationAllow),
			handout(AuthorizationSuspend), handout(AuthorizationDeny),
			func(r Record, now time.Time) Record { n, _ := r.Renew(now, rng.Intn(4) > 0); return n },
			func(r Record, now time.Time) Record {
				n, _, _, err := ackUnder(r, now, Ack{Part: 1 + rng.Intn(chunks), TokenMatches: true})
				if err != nil {
					return r
				}
				return n
			},
			apply(event(Event{Type: EventNotified})),
			apply(event(Event{Type: EventBlocked, Cause: CauseAdapterOffline})),
			apply(event(Event{Type: EventUnblocked})),
		}
	default:
		r = NewTargetControlRecord(ab, rb)
		ops = []scheduleOp{
			tick, tick,
			apply(event(Event{Type: EventReplyCommitted})),
			apply(event(Event{Type: EventReplyCommitted})),
		}
	}
	for n := rng.Intn(9); n > 0; n-- {
		op := ops[rng.Intn(len(ops))]
		switch p := rng.Intn(100); {
		case p < 75:
			now = now.Add(time.Duration(rng.Intn(16)) * time.Minute)
		case p < 90:
			now = now.Add(time.Duration(15+rng.Intn(76)) * time.Minute)
		default:
			// Right before the settle deadline, where leases are issued
			// that would run past it.
			if now.Before(settle) {
				now = settle.Add(-time.Duration(rng.Intn(6)) * time.Minute)
				if jumped != nil {
					op = jumped
				}
			} else {
				now = now.Add(time.Duration(rng.Intn(30)) * time.Minute)
			}
		}
		r = op(r, now)
	}
	if rng.Intn(3) == 0 {
		// A late tick: somewhere from just before the settle deadline to
		// after report_by.
		late := settle.Add(-20*time.Minute + time.Duration(rng.Intn(60))*time.Minute)
		if rng.Intn(2) == 0 {
			late = settle.Add(time.Duration(rng.Intn(5)) * time.Minute)
		}
		if late.After(now) {
			r = tick(r, late)
		}
	}
	return r
}

// lateTargetRecord is a target message record accepted long after its deadline,
// which a correct caller never does but the record must still settle: the
// endpoint's queue time then runs past the settle deadline and its backlog
// deadline is clamped to it. Half of them are handed out in the last minutes
// before the settle deadline, so the lease they get would run past it.
func lateTargetRecord(rng *rand.Rand, ab time.Time) Record {
	rb := ReportBy(ab)
	settle := SettleBy(rb)
	accepted := ab.Add(time.Duration(rng.Intn(int(settle.Sub(ab)/time.Minute)-5)) * time.Minute)
	r, err := NewTargetRecord(accepted, ab, rb, MaxQueueCap)
	if err != nil {
		panic(err)
	}
	if rng.Intn(2) == 0 {
		return r
	}
	now := settle.Add(-time.Duration(1+rng.Intn(5)) * time.Minute)
	if now.Before(accepted) {
		now = accepted
	}
	r, _, err = r.Handout(now, AuthorizationAllow, 1, 1, freshLease())
	if err != nil {
		panic(err)
	}
	if rng.Intn(2) == 0 {
		r, _ = r.Renew(now.Add(time.Minute), true)
	}
	return r
}

// lostScheduleRecord is a record that replaces one lost to storage corruption,
// of a random kind, discovered at a whole minute: it has a ReportBy and none of
// the other deadlines, and half of them have gone on to close, or, for a control
// signal, to be delivered.
func lostScheduleRecord(rng *rand.Rand) Record {
	discovered := t0.Add(time.Duration(rng.Intn(120)) * time.Minute)
	side, control := SideTarget, false
	switch rng.Intn(3) {
	case 0:
		side = SideSource
	case 1:
		control = true
	}
	r, _, err := NewLostRecord(side, control, discovered)
	if err != nil {
		panic(err)
	}
	switch rng.Intn(4) {
	case 0:
		r, _, err = r.Tick(r.ReportBy.Add(time.Nanosecond))
	case 1:
		if control {
			r, _, err = r.Apply(discovered, Event{Type: EventReplyCommitted})
		}
	}
	if err != nil {
		panic(err)
	}
	return r
}

// describeNextTick says which deadline of the record the instant is, judged
// from the record's fields alone, or "" when it is none of them. It is the test
// side's account of why the instant is what it is.
func describeNextTick(r Record, next time.Time) string {
	rb := r.ReportBy
	settle := rb.Add(-5 * time.Minute)
	side := map[Side]string{SideSource: "source", SideTarget: "target"}[r.Side]
	if r.Control {
		side = "control"
	}
	if r.Lost {
		side = "lost " + side
	}
	switch {
	case next.IsZero():
		return "none: " + r.State.String()
	case r.Side == SideTarget && r.Control && r.State == StateAccepted:
		if next.Equal(settle) {
			return "control settle deadline"
		}
	case r.Side == SideTarget && !r.Control && r.State == StateAccepted && r.Lease.Active:
		switch {
		case next.Equal(settle):
			return "lease open at the settle deadline"
		case next.Equal(r.Lease.ExpiresAt):
			return "lease expiry"
		}
	case r.Side == SideTarget && !r.Control && r.State == StateAccepted:
		if next.Equal(r.BacklogDeadline) {
			if next.Equal(settle) {
				return "backlog deadline clamped to settle"
			}
			return "backlog deadline"
		}
	}
	if next.Equal(rb.Add(time.Nanosecond)) {
		return "close after report_by: " + side + " " + r.State.String()
	}
	return ""
}

// requiredNextTickPaths are the deadlines that can be the earliest one, and the
// final states with none.
var requiredNextTickPaths = []string{
	"none: delivered", "none: rejected", "none: closed_unconfirmed",
	"control settle deadline",
	"lease expiry", "lease open at the settle deadline",
	"backlog deadline", "backlog deadline clamped to settle",
	"close after report_by: source queued", "close after report_by: source expired_unconfirmed",
	"close after report_by: target delivery_unknown", "close after report_by: control delivery_unknown",
	"close after report_by: lost source delivery_unknown", "close after report_by: lost target delivery_unknown",
	"close after report_by: lost control delivery_unknown",
}

// TestPropertyNextTickAgreesWithFineStepTick checks NextTick against Tick run
// at every minute of the window around a random record, and a nanosecond
// either side of each: for no instant before NextTick does Tick change the
// record, at NextTick it does, and a record with no NextTick is exactly a final
// one that Tick never changes.
func TestPropertyNextTickAgreesWithFineStepTick(t *testing.T) {
	paths := map[string]int{}
	for seed := int64(1); seed <= scheduleSeeds; seed++ {
		rng := rand.New(rand.NewSource(seed))
		r := scheduleRecord(rng)
		next := r.NextTick()
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d, record %s %+v: %s", seed, r.State, r, fmt.Sprintf(format, args...))
		}
		if next.IsZero() != r.State.IsTerminal() {
			fail("NextTick %v for state %s: a final record has none and every other has one", next, r.State)
		}
		why := describeNextTick(r, next)
		if why == "" {
			fail("NextTick %v (t0+%v) is none of the record's deadlines (report_by t0+%v)", next, next.Sub(t0), r.ReportBy.Sub(t0))
		}
		paths[why]++

		start := t0.Add(-time.Hour)
		end := r.ReportBy.Add(2 * time.Hour)
		if !next.IsZero() {
			end = next.Add(10 * time.Minute)
		}
		for p := start; !p.After(end); p = p.Add(time.Minute) {
			for _, probe := range []time.Time{p.Add(-time.Nanosecond), p, p.Add(time.Nanosecond)} {
				if (next.IsZero() || probe.Before(next)) && tickChanges(r, probe) {
					fail("Tick changes the record at t0+%v, before NextTick %v", probe.Sub(t0), next.Sub(t0))
				}
			}
		}
		if next.IsZero() {
			if tickChanges(r, r.ReportBy.Add(1000*time.Hour)) {
				fail("no NextTick, yet Tick changes the record far in the future")
			}
		} else if !tickChanges(r, next) {
			fail("Tick does not change the record at NextTick t0+%v", next.Sub(t0))
		}
	}
	reportCoverage(t, paths, requiredNextTickPaths)
}

func reportCoverage(t *testing.T, paths map[string]int, required []string) {
	t.Helper()
	if t.Failed() {
		return
	}
	for _, p := range required {
		if paths[p] < minScheduleCount {
			t.Errorf("path %q reached %d times, want at least %d: the generator no longer exercises it", p, paths[p], minScheduleCount)
		}
	}
	names := make([]string, 0, len(paths))
	for p := range paths {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		t.Logf("%6d  %s", paths[p], p)
	}
}

// scheduleItem is a pending feedback item of the given sender endpoint and message.
func scheduleItem(sender, message int, kind Kind, at time.Time) Item {
	return Item{
		Ref:      DeliveryRef{Sender: addr(1, byte(sender)), Message: MessageID(fmt.Sprintf("m%d", message)), Recipient: addr(2, byte(10+message))},
		Feedback: Feedback{Kind: kind, State: StateAccepted},
		At:       at,
	}
}

func cloneQueue(q *FeedbackQueue) *FeedbackQueue {
	return &FeedbackQueue{Pending: append([]Item(nil), q.Pending...), Sent: append([]SentNotice(nil), q.Sent...)}
}

// flushChanges is the oracle for "Flush could do something": it runs the real
// Flush on a copy of the queue and reports whether a notice goes out or an item
// leaves the queue. Dropping sent notices that no longer count toward the cap
// is bookkeeping with no effect on any later result, so it is not a change.
func flushChanges(t testing.TB, q *FeedbackQueue, p FeedbackParams, now time.Time) bool {
	t.Helper()
	c := cloneQueue(q)
	notices, err := c.Flush(now, p)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	return len(notices) > 0 || len(c.Pending) != len(q.Pending)
}

func mustNextFlush(t testing.TB, q *FeedbackQueue, p FeedbackParams) time.Time {
	t.Helper()
	next, err := q.NextFlush(p)
	if err != nil {
		t.Fatalf("NextFlush: %v", err)
	}
	return next
}

// TestNextFlushNamesEachDeadline pins the instants of the merge window and the
// rate cap, written out by hand, each checked against Flush.
func TestNextFlushNamesEachDeadline(t *testing.T) {
	p := FeedbackParams{MergeWindow: 5 * time.Minute, RateCap: 1, RateWindow: 30 * time.Minute}
	cap2 := FeedbackParams{MergeWindow: 5 * time.Minute, RateCap: 2, RateWindow: 30 * time.Minute}
	item := func(sender, message int, after time.Duration) Item {
		return scheduleItem(sender, message, KindRejected, at(after))
	}
	sent := func(sender int, after time.Duration) SentNotice {
		return SentNotice{Sender: addr(1, byte(sender)), At: at(after)}
	}
	rows := []struct {
		name string
		q    FeedbackQueue
		p    FeedbackParams
		want time.Time // zero: nothing is pending
	}{
		{"an empty queue has nothing to flush", FeedbackQueue{Sent: []SentNotice{sent(1, 0)}}, p, time.Time{}},
		{"a lone item waits for its merge window", FeedbackQueue{Pending: []Item{item(1, 1, 2*time.Minute)}}, p, at(7 * time.Minute)},
		{"the window counts from the first item of the group", FeedbackQueue{Pending: []Item{item(1, 1, 2*time.Minute), item(1, 1, 4*time.Minute)}}, p, at(7 * time.Minute)},
		{"the earliest of several groups decides", FeedbackQueue{Pending: []Item{item(1, 1, 10*time.Minute), item(1, 2, 3*time.Minute)}}, p, at(8 * time.Minute)},
		{"a notice sent recently holds a due group until it leaves the rate window", FeedbackQueue{Pending: []Item{item(1, 1, 0)}, Sent: []SentNotice{sent(1, 10*time.Minute)}}, p, at(40 * time.Minute)},
		{"a notice sent long ago does not hold a group", FeedbackQueue{Pending: []Item{item(1, 1, 60*time.Minute)}, Sent: []SentNotice{sent(1, 0)}}, p, at(65 * time.Minute)},
		{"a rate window that ends before the merge window does not delay it", FeedbackQueue{Pending: []Item{item(1, 1, 20*time.Minute)}, Sent: []SentNotice{sent(1, -10*time.Minute)}}, p, at(25 * time.Minute)},
		{"with a cap of two the older of two recent notices frees a slot", FeedbackQueue{Pending: []Item{item(1, 1, 0)}, Sent: []SentNotice{sent(1, 12*time.Minute), sent(1, 10*time.Minute)}}, cap2, at(40 * time.Minute)},
		{"with more recent notices than the cap the cap-th most recent one decides", FeedbackQueue{Pending: []Item{item(1, 1, 0)}, Sent: []SentNotice{sent(1, 5*time.Minute), sent(1, 10*time.Minute), sent(1, 12*time.Minute)}}, cap2, at(40 * time.Minute)},
		{"with a cap of two one recent notice holds nothing", FeedbackQueue{Pending: []Item{item(1, 1, 0)}, Sent: []SentNotice{sent(1, 10*time.Minute)}}, cap2, at(5 * time.Minute)},
		{"another sender's notices do not count toward the cap", FeedbackQueue{Pending: []Item{item(1, 1, 0)}, Sent: []SentNotice{sent(2, 4*time.Minute)}}, p, at(5 * time.Minute)},
		{"a held sender does not hold another sender's due group", FeedbackQueue{Pending: []Item{item(1, 1, 0), item(2, 2, 3*time.Minute)}, Sent: []SentNotice{sent(1, 4*time.Minute)}}, p, at(8 * time.Minute)},
		{"a held sender's group is released by the rate window when that comes first", FeedbackQueue{Pending: []Item{item(1, 1, 0), item(2, 2, 40*time.Minute)}, Sent: []SentNotice{sent(1, 4*time.Minute)}}, p, at(34 * time.Minute)},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got := mustNextFlush(t, &row.q, row.p)
			if !got.Equal(row.want) {
				t.Fatalf("NextFlush = t0+%v, want t0+%v", got.Sub(t0), row.want.Sub(t0))
			}
			if row.want.IsZero() {
				if flushChanges(t, &row.q, row.p, at(1000*time.Hour)) {
					t.Fatalf("nothing pending, yet Flush acts far in the future")
				}
				return
			}
			if flushChanges(t, &row.q, row.p, row.want.Add(-time.Nanosecond)) {
				t.Errorf("Flush acts a nanosecond before t0+%v", row.want.Sub(t0))
			}
			if !flushChanges(t, &row.q, row.p, row.want) {
				t.Errorf("Flush does nothing at t0+%v", row.want.Sub(t0))
			}
		})
	}
}

func TestNextFlushRefusesUnsetParameters(t *testing.T) {
	q := &FeedbackQueue{Pending: []Item{scheduleItem(1, 1, KindRejected, t0)}}
	for _, p := range []FeedbackParams{
		{},
		{MergeWindow: time.Minute, RateCap: 1},
		{MergeWindow: time.Minute, RateWindow: time.Minute},
		{RateCap: 1, RateWindow: time.Minute},
	} {
		if _, err := q.NextFlush(p); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("NextFlush(%+v) error = %v, want ErrInvalidInput, as Flush", p, err)
		}
	}
}

// scheduleQueue builds a queue by a random run of Add and Flush at whole-minute
// instants, so that Pending and Sent are whatever those two leave, and
// sometimes adds sent notices directly.
func scheduleQueue(rng *rand.Rand) (*FeedbackQueue, FeedbackParams, time.Time) {
	windows := []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}
	rateWindows := []time.Duration{5 * time.Minute, 10 * time.Minute, 30 * time.Minute, time.Hour}
	p := FeedbackParams{
		MergeWindow: windows[rng.Intn(len(windows))],
		RateCap:     1 + rng.Intn(3),
		RateWindow:  rateWindows[rng.Intn(len(rateWindows))],
	}
	kinds := []Kind{KindUnreachable, KindExpiredUnconfirmed, KindDeliveryBlocked, KindBlockedRecovered, KindRejected, KindDeliveryUnknown}
	q := &FeedbackQueue{}
	now := t0
	for n := rng.Intn(16); n > 0; n-- {
		if rng.Intn(10) < 8 {
			now = now.Add(time.Duration(rng.Intn(6)) * time.Minute)
		} else {
			now = now.Add(time.Duration(10+rng.Intn(21)) * time.Minute)
		}
		if rng.Intn(10) < 7 {
			if err := q.Add(scheduleItem(1+rng.Intn(3), 1+rng.Intn(4), kinds[rng.Intn(len(kinds))], now)); err != nil {
				panic(err)
			}
		} else if _, err := q.Flush(now, p); err != nil {
			panic(err)
		}
	}
	if rng.Intn(4) == 0 {
		// A queue loaded from storage may hold more notices than the cap in
		// one window, for example after the cap was lowered.
		for n := 1 + rng.Intn(5); n > 0; n-- {
			q.Sent = append(q.Sent, SentNotice{Sender: addr(1, byte(1+rng.Intn(3))), At: now.Add(-time.Duration(rng.Intn(61)) * time.Minute)})
		}
	}
	return q, p, now
}

// describeNextFlush says which parameter decided the instant, judged from the
// queue's fields alone, or "" when it is none of them. The rate path counts
// only when the instant is the end of the rate window of the RateCap-th newest
// notice a sender sent: an older or newer notice that happens to end its window
// at the same instant does not release the cap.
func describeNextFlush(q *FeedbackQueue, p FeedbackParams, next time.Time) string {
	if next.IsZero() {
		return "none: nothing pending"
	}
	window, rate := false, false
	for _, it := range q.Pending {
		window = window || next.Equal(it.At.Add(p.MergeWindow))
	}
	sentBy := map[ident.Address][]time.Time{}
	for _, s := range q.Sent {
		sentBy[s.Sender] = append(sentBy[s.Sender], s.At)
	}
	for _, sent := range sentBy {
		if len(sent) < p.RateCap {
			continue
		}
		sort.Slice(sent, func(i, j int) bool { return sent[i].After(sent[j]) })
		rate = rate || next.Equal(sent[p.RateCap-1].Add(p.RateWindow))
	}
	switch {
	case rate && p.RateCap > 1:
		return "rate window release, cap above one"
	case rate:
		return "rate window release, cap one"
	case window:
		return "merge window"
	}
	return ""
}

var requiredNextFlushPaths = []string{
	"none: nothing pending", "merge window",
	"rate window release, cap one", "rate window release, cap above one",
}

// TestPropertyNextFlushAgreesWithFineStepFlush is the same three properties for
// the queue: Flush acts at no instant before NextFlush, acts at it, and a queue
// with nothing pending has none.
func TestPropertyNextFlushAgreesWithFineStepFlush(t *testing.T) {
	paths := map[string]int{}
	for seed := int64(1); seed <= scheduleSeeds; seed++ {
		rng := rand.New(rand.NewSource(seed))
		q, p, now := scheduleQueue(rng)
		next := mustNextFlush(t, q, p)
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d, params %+v, pending %d sent %d: %s", seed, p, len(q.Pending), len(q.Sent), fmt.Sprintf(format, args...))
		}
		if next.IsZero() != (len(q.Pending) == 0) {
			fail("NextFlush %v with %d items pending: a queue has one exactly when something is pending", next, len(q.Pending))
		}
		why := describeNextFlush(q, p, next)
		if why == "" {
			fail("NextFlush t0+%v is neither a merge window end nor a rate window end", next.Sub(t0))
		}
		paths[why]++

		start := t0.Add(-time.Hour)
		end := now.Add(4 * time.Hour)
		if !next.IsZero() {
			end = next.Add(10 * time.Minute)
		}
		for pt := start; !pt.After(end); pt = pt.Add(time.Minute) {
			for _, probe := range []time.Time{pt.Add(-time.Nanosecond), pt, pt.Add(time.Nanosecond)} {
				if (next.IsZero() || probe.Before(next)) && flushChanges(t, q, p, probe) {
					fail("Flush acts at t0+%v, before NextFlush t0+%v", probe.Sub(t0), next.Sub(t0))
				}
			}
		}
		if next.IsZero() {
			if flushChanges(t, q, p, now.Add(1000*time.Hour)) {
				fail("no NextFlush, yet Flush acts far in the future")
			}
		} else if !flushChanges(t, q, p, next) {
			fail("Flush does nothing at NextFlush t0+%v", next.Sub(t0))
		}
	}
	reportCoverage(t, paths, requiredNextFlushPaths)
}
