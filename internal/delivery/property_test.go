package delivery

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/authz"
)

// propertySeeds is how many random deliveries the property test runs. It is
// large enough that every path in requiredPaths is reached at least
// minPathCount times; the test fails when one is not, so a generator that
// stopped reaching a path cannot pass silently.
const (
	propertySeeds = 10000
	minPathCount  = 20
)

// requiredPaths are the paths of the rules that the random deliveries must
// reach often enough to mean something.
var requiredPaths = []string{
	"renewed", "renew refused: no active turn", "renew refused: cap reached", "requeue",
	"delivery_unknown by lease count", "delivery_unknown by settle deadline",
	"hand-out skipped", "hand-out denied", "ack recorded", "ack refused: lease",
	"source expired", "feedback expired_corrected", "feedback unknown_corrected", "feedback blocked_recovered",
	"delivery_unknown by backlog deadline", "delivery_unknown by denied hand-out", "delivery_unknown by closed endpoint",
	"rejected by backlog deadline", "delivered",
}

// TestPropertyEveryDeliveryClosesAndNoFeedbackRepeats drives a source record
// and a target record with random, time-ordered events and checks the
// guarantees that make "no feedback means delivered" safe: whatever happens,
// the target has settled the delivery by the settle deadline, both sides are
// final after report_by, the sender never hears of the same anomaly of the
// same delivery twice, and, checked against the states the source actually
// went through and not against the code that decides them, the sender hears
// of every anomaly it went through at least once.
func TestPropertyEveryDeliveryClosesAndNoFeedbackRepeats(t *testing.T) {
	paths := map[string]int{}
	ran := 0
	for seed := int64(1); seed <= propertySeeds; seed++ {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			ran++
			runRandomDelivery(t, seed, paths, func(*testing.T, Record) {})
		})
	}
	if t.Failed() || ran < propertySeeds {
		return
	}
	for _, p := range requiredPaths {
		if paths[p] < minPathCount {
			t.Errorf("path %q reached %d times, want at least %d: the generator no longer exercises it", p, paths[p], minPathCount)
		}
	}
	names := make([]string, 0, len(paths))
	for p := range paths {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		t.Logf("%-45s %d", p, paths[p])
	}
}

// op is one kind of operation of the random deliveries.
type op int

const (
	opHandout op = iota
	opRenew
	opAck
	opNotify
	opBlock
	opUnblock
	opCloseEndpoint
	opPull
	opSourceClock
	opCount
)

// randomStep returns how far the clock moves between two operations. The steps
// are on the scale of the lease (5 minutes): mostly well inside one lease,
// sometimes across a lease expiry, rarely across the deadlines of the queue.
func randomStep(rng *rand.Rand) time.Duration {
	switch p := rng.Intn(100); {
	case p < 80:
		return time.Duration(rng.Intn(91)) * time.Second
	case p < 95:
		return 3*time.Minute + time.Duration(rng.Int63n(int64(12*time.Minute)+1))
	default:
		return 30*time.Minute + time.Duration(rng.Int63n(int64(150*time.Minute)+1))
	}
}

// observe is called with every record of both sides after every step, so a
// test can check the records the walk produces.
func runRandomDelivery(t *testing.T, seed int64, paths map[string]int, observe func(*testing.T, Record)) {
	rng := rand.New(rand.NewSource(seed))
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("seed %d: "+format, append([]any{seed}, args...)...)
	}

	ab := t0.Add(time.Duration(1+rng.Intn(60)) * time.Minute)
	src := NewSourceRecord(ab)
	reportBy := src.ReportBy
	settle := SettleBy(reportBy)
	chunks := 1 + rng.Intn(3)
	maxQueues := []time.Duration{5 * time.Minute, 30 * time.Minute, time.Hour, MaxQueueCap}
	maxQueue := maxQueues[rng.Intn(len(maxQueues))]
	acceptedAt := t0.Add(time.Duration(rng.Int63n(int64(ab.Sub(t0)))))
	targetReachable := rng.Intn(5) > 0
	// How the agent behind the recipient endpoint behaves differs between
	// deliveries: a busy agent renews its lease again and again and answers
	// rarely, which is what reaches the renewal cap and the lease count.
	weights := map[op]int{opHandout: 3, opRenew: 2, opAck: 2, opNotify: 1, opBlock: 1, opUnblock: 1, opCloseEndpoint: 1, opPull: 4, opSourceClock: 1}
	if rng.Intn(3) == 0 {
		weights[opRenew], weights[opAck], weights[opCloseEndpoint] = 14, 0, 0
	}
	// On a poor link most of the sender's pulls are lost, so it learns of the
	// receipt late, after it has judged the delivery expired.
	pullLoss := 4
	if rng.Intn(4) == 0 {
		pullLoss = 1
	}
	var wheel []op
	for o := op(0); o < opCount; o++ {
		for i := 0; i < weights[o]; i++ {
			wheel = append(wheel, o)
		}
	}

	var tgt *Record
	var pages []LeaseID // every page identifier a hand-out used
	var q FeedbackQueue
	reportedCount := map[Kind]int{}
	visits := []State{StateQueued} // the states the source went through, in order
	srcApplied := func(n Record, outs ...Outcome) {
		for _, out := range outs {
			if out.Result == ResultApplied && out.To == StateExpiredUnconfirmed && out.From != out.To {
				paths["source expired"]++
			}
			for _, fb := range out.Feedback {
				paths["feedback "+fb.Kind.String()]++
				reportedCount[fb.Kind]++
				if reportedCount[fb.Kind] > 1 {
					fail("feedback %s produced twice", fb.Kind)
				}
				_ = q.Add(Item{Ref: DeliveryRef{Sender: addr(1, 1), Message: "m", Recipient: addr(2, 2)}, Feedback: fb, At: t0})
			}
		}
		if n.State != visits[len(visits)-1] {
			visits = append(visits, n.State)
		}
		// The deliveries here are healthy: the only unknown result the source
		// can hold is the one the target handed over, and only in that state.
		if wantCause := causeOfState(n.State); n.Unknown != wantCause {
			fail("source in %s with unknown cause %s, want %s", n.State, n.Unknown, wantCause)
		}
		src = n
		observe(t, src)
	}
	apply := func(r Record, now time.Time, ev Event) (Record, Outcome) {
		n, out, err := r.Apply(now, ev)
		if err != nil {
			fail("Apply(%+v) in %s at %v: %v", ev, r.State, now.Sub(t0), err)
		}
		return n, out
	}
	srcApply := func(now time.Time, ev Event) {
		n, out := apply(src, now, ev)
		srcApplied(n, out)
	}
	srcPull := func(now time.Time) {
		if tgt == nil || tgt.State == StateClosedUnconfirmed {
			return
		}
		n, outs, err := src.ApplyStatusReport(now, tgt.StatusReport())
		if err != nil {
			fail("ApplyStatusReport(%+v) in %s at %v: %v", tgt.StatusReport(), src.State, now.Sub(t0), err)
		}
		srcApplied(n, outs...)
	}
	tgtTick := func(now time.Time) {
		if tgt == nil {
			return
		}
		before := *tgt
		n, outs, err := tgt.Tick(now)
		if err != nil {
			fail("target Tick at %v: %v", now.Sub(t0), err)
		}
		tgt = &n
		for _, out := range outs {
			switch {
			case out.Requeued:
				paths["requeue"]++
			case out.To == StateDeliveryUnknown && out.From == StateAccepted:
				switch {
				case before.Lease.Active && !now.Before(settle):
					paths["delivery_unknown by settle deadline"]++
				case before.Lease.Active && !now.Before(before.Lease.ExpiresAt) && before.Lease.Handouts >= MaxLeases:
					paths["delivery_unknown by lease count"]++
				default:
					paths["delivery_unknown by backlog deadline"]++
				}
			case out.To == StateRejected:
				paths["rejected by backlog deadline"]++
			}
		}
	}
	checkTarget := func(now time.Time) {
		if tgt == nil {
			return
		}
		if tgt.State == StateQueued || tgt.State == StateExpiredUnconfirmed {
			fail("target in source-only state %s", tgt.State)
		}
		if tgt.Lease.Handouts > MaxLeases {
			fail("%d leases issued", tgt.Lease.Handouts)
		}
		if tgt.Lease.Active && tgt.Lease.ExpiresAt.Sub(tgt.Lease.IssuedAt) > LeaseRenewalCap {
			fail("lease lives %v", tgt.Lease.ExpiresAt.Sub(tgt.Lease.IssuedAt))
		}
		if tgt.Lease.Handouts > 0 && tgt.State == StateRejected {
			fail("rejected (%s) although the body was handed out %d times", tgt.Reject, tgt.Lease.Handouts)
		}
		if tgt.State == StateDeliveryUnknown && tgt.Lease.Handouts == 0 {
			fail("delivery_unknown without a hand-out")
		}
		if wantCause := causeOfState(tgt.State); tgt.Unknown != wantCause {
			fail("target in %s with unknown cause %s, want %s", tgt.State, tgt.Unknown, wantCause)
		}
		anyHandedOut := false
		for i, out := range tgt.Lease.HandedOut {
			anyHandedOut = anyHandedOut || out
			if tgt.Lease.Acked[i] && !out {
				fail("chunk %d acknowledged but never handed out", i+1)
			}
		}
		if tgt.Lease.Handouts > 0 && !anyHandedOut {
			fail("a lease was issued but no chunk was handed out")
		}
		if tgt.State == StateDelivered {
			for i, done := range tgt.Lease.Acked {
				if !done {
					fail("delivered with chunk %d unacknowledged", i+1)
				}
			}
		}
		if !now.Before(settle) {
			switch tgt.State {
			case StateDelivered, StateRejected, StateDeliveryUnknown, StateClosedUnconfirmed:
			default:
				fail("target still %s at %v, settle deadline %v", tgt.State, now.Sub(t0), settle.Sub(t0))
			}
		}
		if now.After(reportBy) && !tgt.State.IsTerminal() {
			fail("target not final after report_by: %s", tgt.State)
		}
	}
	// What the source concluded must match what the target ended with.
	checkAgreement := func() {
		switch src.State {
		case StateDelivered:
			if tgt == nil || tgt.State != StateDelivered {
				fail("source delivered, target %v", tgt)
			}
		case StateRejected:
			if tgt == nil || tgt.State != StateRejected || tgt.Reject != src.Reject {
				fail("source rejected for %s, target %v", src.Reject, tgt)
			}
		}
	}

	now := t0
	end := reportBy.Add(ReportBySafetyMargin + time.Minute)
	for ops := 0; now.Before(end) && ops < 600; ops++ {
		now = now.Add(randomStep(rng))
		if tgt == nil && targetReachable && !now.Before(acceptedAt) && !now.After(ab) {
			if v, err := Intake(now, ab, authz.TrafficMessage, allow, roomy); err != nil || v.Outcome != IntakeAccept {
				fail("intake %+v %v", v, err)
			}
			r, err := NewTargetRecord(now, ab, reportBy, maxQueue)
			if err != nil {
				fail("%v", err)
			}
			tgt = &r
		}
		tgtTick(now)
		checkTarget(now)

		switch wheel[rng.Intn(len(wheel))] {
		case opHandout:
			if tgt != nil {
				auth := []AuthorizationKind{AuthorizationAllow, AuthorizationAllow, AuthorizationAllow, AuthorizationSuspend, AuthorizationDeny}[rng.Intn(5)]
				if rng.Intn(4) != 0 && auth == AuthorizationDeny {
					auth = AuthorizationAllow
				}
				// Every hand-out is a page of its own here: one identifier each.
				page, part := freshLease(), 1+rng.Intn(chunks)
				wasDup := tgt.PossibleDuplicate(part)
				n, res, err := tgt.Handout(now, auth, chunks, part, page)
				if err != nil {
					fail("Handout: %v", err)
				}
				if res.Action == HandoutGranted || res.Action == HandoutResent {
					if res.Lease != page || !n.Lease.issued(page) || res.PossibleDuplicate != wasDup {
						fail("Handout %+v: page %v, ids %v, was a possible duplicate %v", res, page, n.Lease.IDs, wasDup)
					}
					pages = append(pages, page)
				}
				switch res.Action {
				case HandoutSkipped:
					paths["hand-out skipped"]++
				case HandoutDenied:
					paths["hand-out denied"]++
					if res.Outcome.To == StateDeliveryUnknown {
						paths["delivery_unknown by denied hand-out"]++
					}
				}
				tgt = &n
			}
		case opRenew:
			if tgt != nil {
				n, res := tgt.Renew(now, rng.Intn(6) != 0)
				switch res {
				case Renewed:
					paths["renewed"]++
				case RenewNoActiveTurn:
					paths["renew refused: no active turn"]++
				case RenewCapReached:
					paths["renew refused: cap reached"]++
				}
				tgt = &n
			}
		case opAck:
			if tgt != nil {
				// Mostly the agent acknowledges what it was given; sometimes
				// the acknowledgement names any chunk.
				part := 1 + rng.Intn(chunks)
				if given := handedOutParts(tgt); len(given) > 0 && rng.Intn(3) != 0 {
					part = given[rng.Intn(len(given))]
				}
				a := Ack{Part: part, Lease: tgt.Lease.current(), TokenMatches: rng.Intn(6) != 0}
				if len(pages) > 0 {
					// Any page the message was handed out on is a valid name.
					a.Lease = pages[rng.Intn(len(pages))]
				}
				if rng.Intn(8) == 0 {
					a.Lease = LeaseID{}
				}
				n, v, out, err := tgt.Ack(now, a)
				if err != nil {
					fail("Ack: %v", err)
				}
				switch v {
				case AckRefusedLease:
					paths["ack refused: lease"]++
				case AckRecorded:
					paths["ack recorded"]++
				case AckCompleted:
					paths["delivered"]++
					if out.From == StateDeliveryUnknown {
						paths["delivered after delivery_unknown"]++
					}
				}
				tgt = &n
			}
		case opNotify:
			if tgt != nil {
				n, _ := apply(*tgt, now, Event{Type: EventNotified})
				tgt = &n
			}
		case opBlock:
			if tgt != nil {
				causes := []Cause{CauseAdapterOffline, CauseRuntimeAuth, CauseNativeUnavailable}
				n, _ := apply(*tgt, now, Event{Type: EventBlocked, Cause: causes[rng.Intn(3)]})
				tgt = &n
			}
		case opUnblock:
			if tgt != nil {
				n, _ := apply(*tgt, now, Event{Type: EventUnblocked})
				tgt = &n
			}
		case opCloseEndpoint:
			if tgt != nil && tgt.State == StateAccepted && rng.Intn(4) == 0 {
				n, out := apply(*tgt, now, Event{Type: EventRejected, Reject: RejectEndpointClosed})
				if out.To == StateDeliveryUnknown {
					paths["delivery_unknown by closed endpoint"]++
				}
				tgt = &n
			}
		case opPull:
			// A quarter of the operations are the sender pulling the target's
			// report; some of the pulls are lost on the way.
			if rng.Intn(pullLoss) != 0 {
				srcPull(now)
			}
		case opSourceClock:
			if rng.Intn(2) == 0 {
				causes := []Cause{CauseNetwork, CauseTrustSuspended}
				srcApply(now, Event{Type: EventContactFailing, Cause: causes[rng.Intn(2)], Since: t0})
			} else {
				srcApply(now, Event{Type: EventExpiryQueried, QueriedAt: now})
			}
		}
		if tgt != nil {
			observe(t, *tgt)
		}
		srcApply(now, Event{Type: EventReportByPassed})
		checkAgreement()
		if now.After(reportBy) && !src.State.IsTerminal() {
			fail("source not final after report_by: %s", src.State)
		}
	}

	// Run out the clock: everything must close.
	now = end
	tgtTick(now)
	checkTarget(now)
	if tgt != nil {
		observe(t, *tgt)
	}
	srcPull(now)
	srcApply(now, Event{Type: EventReportByPassed})
	checkAgreement()
	if !src.State.IsTerminal() {
		fail("source not final at the end: %s", src.State)
	}
	if tgt != nil && !tgt.State.IsTerminal() {
		fail("target not final at the end: %s", tgt.State)
	}

	// The sender hears of every anomaly its delivery went through, judged from
	// the states the source visited and not from the rules that decide what
	// to report.
	anomalyKind := map[State]Kind{
		StateRejected:           KindRejected,
		StateDeliveryUnknown:    KindDeliveryUnknown,
		StateExpiredUnconfirmed: KindExpiredUnconfirmed,
	}
	visitedAnomaly := false
	for i, st := range visits {
		var next State
		if i+1 < len(visits) {
			next = visits[i+1]
		}
		if k, ok := anomalyKind[st]; ok {
			visitedAnomaly = true
			if reportedCount[k] == 0 {
				fail("source visited %s but never reported %s; visited %v", st, k, visits)
			}
		}
		if st == StateExpiredUnconfirmed && (next == StateAccepted || next == StateDelivered) && reportedCount[KindExpiredCorrected] == 0 {
			fail("expired_unconfirmed was followed by %s without a correction; visited %v", next, visits)
		}
		if st == StateDeliveryUnknown && next == StateDelivered && reportedCount[KindUnknownCorrected] == 0 {
			fail("delivery_unknown was followed by delivered without a correction; visited %v", visits)
		}
	}
	if src.State == StateDelivered && reportedCount[KindDeliveryBlocked] > 0 && reportedCount[KindBlockedRecovered] == 0 {
		fail("delivered after a reported block without telling that the block ended; visited %v", visits)
	}
	if !visitedAnomaly {
		for k := range reportedCount {
			if k != KindUnreachable && k != KindDeliveryBlocked && k != KindBlockedRecovered {
				fail("feedback %s although the source went through %v", k, visits)
			}
		}
	}

	// The queue sends every decided notice once and none twice.
	seen := map[Kind]int{}
	notices, err := q.Flush(now.Add(24*time.Hour), testParams)
	for len(q.Pending) > 0 && err == nil {
		var more []Notice
		now = now.Add(time.Hour)
		more, err = q.Flush(now.Add(24*time.Hour), testParams)
		notices = append(notices, more...)
	}
	if err != nil {
		fail("%v", err)
	}
	for _, n := range notices {
		for _, it := range n.Items {
			seen[it.Feedback.Kind]++
		}
	}
	for k, n := range seen {
		if n != 1 {
			fail("kind %s sent %d times", k, n)
		}
	}
	if len(seen) != len(reportedCount) {
		fail("decided %v, sent %v", reportedCount, seen)
	}
}

// causeOfState is the unknown cause a healthy delivery shows in a state: the
// cause is set exactly while the state is delivery_unknown, and a delivery that
// was not lost to storage corruption is always handed over.
func causeOfState(s State) UnknownCause {
	if s == StateDeliveryUnknown {
		return UnknownHandedOver
	}
	return UnknownCauseNone
}

// handedOutParts lists the chunks of the record that were handed out.
func handedOutParts(r *Record) []int {
	var parts []int
	for i, out := range r.Lease.HandedOut {
		if out {
			parts = append(parts, i+1)
		}
	}
	return parts
}
