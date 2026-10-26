package engine_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Pulling is how an agent gets what was sent to its endpoint: recv hands a page
// out under a lease, ack says the page was seen, and a lease that runs out puts
// the message back for the next recv, marked as one the agent may have seen. The
// tests run on one node whose endpoints send to each other, judge what the agent
// was handed from the pages it printed and what became of a delivery from the
// records and from the notices the sender holds.

// A delivery that goes well tells its sender nothing: the message is handed out,
// acknowledged, delivered, and the sender holds no notice, owes none and sees its
// record delivered, however long it waits.
func TestNormalDeliveryGivesNoFeedback(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	b := h.Agent("b")
	msg := send(t, h, "a", "b", "all is well")
	quiet(t, h)

	res := recvAs(t, h, b, fakeagent.RecvInput{})
	if m := wantOnly(t, res, msg); m.Body != "all is well" || m.PossiblyDuplicate || m.From != adapterproto.Address(h.Addr("a")) {
		t.Fatalf("the page shows %+v, want the message as sent, from a, not a possible duplicate", m)
	}
	acks := ackAs(t, h, b, res.Page.Lease)
	if len(acks) != 1 || acks[0].MessageID != msg || acks[0].Part != 1 || acks[0].Ack != adapterproto.ItemCompleted {
		t.Fatalf("the acknowledgement is %+v, want the one part completed", acks)
	}
	quiet(t, h)
	if in := inOf(t, h, msg); in.Record.State != delivery.StateDelivered || in.Body != nil {
		t.Fatalf("the target record is %s with body %v, want delivered and no body kept", in.Record.State, in.Body)
	}

	for i := 0; i < 48; i++ {
		h.Advance(time.Hour)
		quiet(t, h)
		if i == 0 {
			if d := sourceDelivery(t, h, msg); d.State != delivery.StateDelivered {
				t.Fatalf("the source record is %s after the next pull, want delivered", d.State)
			}
		}
	}
	if got := toldKinds(t, h, "a"); len(got) != 0 {
		t.Errorf("a was told %v, want nothing: a delivery that went well is silent", got)
	}
	if got := pending(t, h, "a"); got != 0 {
		t.Errorf("%d items of feedback are owed to a, want none", got)
	}
	wantEmpty(t, recvAs(t, h, h.Agent("a"), fakeagent.RecvInput{}))
	if err := h.CheckLedger(); err != nil {
		t.Error(err)
	}
}

// Every way a delivery goes wrong is told to the sender once, in a notice of its
// own kind, and not again by time, by pulls or by a restart: the target cannot be
// reached, it refuses, the deadline passes without a receipt, and the result of
// what the agent was handed is not known.
func TestEachAnomalyIsReportedOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(t *testing.T) *harness.H
		told []delivery.Kind
	}{
		{"the target cannot be reached", func(t *testing.T) *harness.H {
			h := nodeWith(t, harness.Options{}, "a", "b")
			h.Unreachable(true)
			msg := send(t, h, "a", "b", "to a target out of reach")
			quiet(t, h)
			for i := 0; i < 4; i++ {
				h.Advance(time.Minute)
				quiet(t, h)
			}
			h.Advance(mergeWindow)
			quiet(t, h)
			h.Unreachable(false)
			h.Advance(resendBackoff)
			quiet(t, h)
			res := recvAs(t, h, h.Agent("b"), fakeagent.RecvInput{})
			wantOnly(t, res, msg)
			ackAs(t, h, h.Agent("b"), res.Page.Lease)
			quiet(t, h)
			return h
		}, []delivery.Kind{delivery.KindUnreachable}},
		{"the target refuses", func(t *testing.T) *harness.H {
			h := nodeWith(t, harness.Options{}, "a", "b")
			declineAll(t, h, "b")
			send(t, h, "a", "b", "to an endpoint that takes nothing")
			quiet(t, h)
			return h
		}, []delivery.Kind{delivery.KindRejected}},
		{"the deadline passes without a receipt", func(t *testing.T) *harness.H {
			h, _ := heldMessage(t)
			h.Advance(delivery.DefaultAcceptBefore)
			quiet(t, h)
			h.Advance(2*time.Minute + pullInterval)
			quiet(t, h)
			return h
		}, []delivery.Kind{delivery.KindExpiredUnconfirmed}},
		{"the agent was handed the message and never acknowledged it", func(t *testing.T) *harness.H {
			h := nodeWith(t, harness.Options{}, "a", "b")
			msg := send(t, h, "a", "b", "handed out and never acknowledged")
			quiet(t, h)
			for i := 0; i < delivery.MaxLeases; i++ {
				wantOnly(t, recvAs(t, h, h.Agent("b"), fakeagent.RecvInput{}), msg)
				h.Advance(delivery.LeaseDuration)
				quiet(t, h)
			}
			h.Advance(pullInterval)
			quiet(t, h)
			return h
		}, []delivery.Kind{delivery.KindDeliveryUnknown}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.run(t)
			settledOnce(t, h, tc.told, "no_such_alert", 0)
			if err := h.CheckLedger(); err != nil {
				t.Error(err)
			}
		})
	}
}

// A message whose lease runs out unacknowledged is handed out again, marked as a
// possible duplicate, until the fourth lease runs out: the first hand-out and three
// redeliveries. Then the node stops, the delivery is delivery_unknown, and the
// sender is told once. The agent that comes late with an acknowledgement corrects
// it, and that is told once too.
func TestNeverAcknowledgedStopsAfterThirdRedeliveryAndReportsOnce(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	b := h.Agent("b")
	msg := send(t, h, "a", "b", "never acknowledged")
	quiet(t, h)

	var leases []adapterproto.LeaseRef
	for i := 1; i <= 1+delivery.MaxRedeliveries; i++ {
		res := recvAs(t, h, b, fakeagent.RecvInput{})
		m := wantOnly(t, res, msg)
		if m.PossiblyDuplicate != (i > 1) {
			t.Errorf("hand-out %d: possibly_duplicate is %v, want %v", i, m.PossiblyDuplicate, i > 1)
		}
		if slices.Contains(leases, res.Page.Lease) {
			t.Errorf("hand-out %d reuses the lease of an earlier page", i)
		}
		leases = append(leases, res.Page.Lease)
		if in := inOf(t, h, msg); in.Record.Lease.Handouts != i || !in.Record.Lease.Active {
			t.Fatalf("after hand-out %d the record counts %d hand-outs, active %v", i, in.Record.Lease.Handouts, in.Record.Lease.Active)
		}
		h.Advance(delivery.LeaseDuration)
		quiet(t, h)
	}
	wantEmpty(t, recvAs(t, h, b, fakeagent.RecvInput{}))
	in := inOf(t, h, msg)
	if in.Record.State != delivery.StateDeliveryUnknown || in.Record.Unknown != delivery.UnknownHandedOver || in.Record.Lease.Handouts != delivery.MaxLeases {
		t.Fatalf("the target record is %s (%s) after %d hand-outs, want delivery_unknown, handed_over, %d", in.Record.State, in.Record.Unknown, in.Record.Lease.Handouts, delivery.MaxLeases)
	}
	if in.Body != nil {
		t.Error("the record whose outcome is unknown still holds the body")
	}

	h.Advance(pullInterval)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateDeliveryUnknown {
		t.Fatalf("the source record is %s, want delivery_unknown", d.State)
	}
	settledOnce(t, h, []delivery.Kind{delivery.KindDeliveryUnknown}, "no_such_alert", 0)

	// The agent did see it, and says so with the lease of an early page.
	acks := ackAs(t, h, b, leases[0], adapterproto.PartRef{MessageID: msg, Part: 1})
	if len(acks) != 1 || acks[0].Ack != adapterproto.ItemCompleted {
		t.Fatalf("the late acknowledgement is %+v, want completed", acks)
	}
	h.Advance(pullInterval)
	quiet(t, h)
	h.Advance(mergeWindow)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateDelivered {
		t.Fatalf("the source record is %s after the late acknowledgement, want delivered", d.State)
	}
	want := []delivery.Kind{delivery.KindDeliveryUnknown, delivery.KindUnknownCorrected}
	if got := toldKinds(t, h, "a"); !slices.Equal(got, want) {
		t.Errorf("a was told %v, want %v", got, want)
	}
	if err := h.CheckLedger(); err != nil {
		t.Error(err)
	}
}

// Long messages are handed out whole while they fit and in parts when they do not;
// every page fits the budget, every part is seen once, in order, and acknowledged
// once, so the acknowledgements counted equal the parts the agent was shown, and the
// delivery is delivered only when the last part of the message is.
func TestLargeMessagesWholeOrPartsAndAckCountsMatch(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	b := h.Agent("b")
	budget := handoff.DefaultBudget()
	bodies := map[adapterproto.MessageID]string{}
	short := send(t, h, "a", "b", "a short one that fits whole")
	bodies[short] = "a short one that fits whole"
	for i := 0; i < 5; i++ {
		body := bigText(fmt.Sprintf("long message %d", i), 60*1024)
		bodies[send(t, h, "a", "b", body)] = body
	}
	quiet(t, h)

	seen := map[adapterproto.MessageID][]string{}
	total := map[adapterproto.MessageID]int{}
	visible, acked, pages := 0, 0, 0
	for ; pages < 200; pages++ {
		res := recvAs(t, h, b, fakeagent.RecvInput{})
		if len(res.Page.Messages) == 0 {
			break
		}
		if got := budget.Measure(string(res.Raw)); got > budget.Limit {
			t.Fatalf("page %d measures %d, beyond the budget of %d", pages, got, budget.Limit)
		}
		var want []adapterproto.PartRef
		for _, m := range res.Page.Messages {
			index, parts := 1, 1
			if m.Part != nil {
				index, parts = m.Part.Index, m.Part.Total
			}
			if m.PossiblyDuplicate {
				t.Errorf("message %s part %d is marked as a possible duplicate on its first sight", m.ID, index)
			}
			if index != len(seen[m.ID])+1 {
				t.Fatalf("message %s shows part %d after %d parts: the parts come in order, each once", m.ID, index, len(seen[m.ID]))
			}
			if (index < parts) != (m.Part != nil && m.Part.Next != nil) {
				t.Errorf("message %s part %d/%d has a pointer to the next part %v", m.ID, index, parts, m.Part)
			}
			seen[m.ID] = append(seen[m.ID], m.Body)
			total[m.ID] = parts
			visible++
			want = append(want, adapterproto.PartRef{MessageID: m.ID, Part: index})
		}
		acks := ackAs(t, h, b, res.Page.Lease)
		if len(acks) != len(want) {
			t.Fatalf("page %d: %d acknowledgements for %d parts shown", pages, len(acks), len(want))
		}
		for i, a := range acks {
			acked++
			last := len(seen[a.MessageID]) == total[a.MessageID]
			if a.MessageID != want[i].MessageID || a.Part != want[i].Part {
				t.Fatalf("the acknowledgement %d is of %s part %d, want %s part %d", i, a.MessageID, a.Part, want[i].MessageID, want[i].Part)
			}
			if wantAck := map[bool]adapterproto.ItemAck{true: adapterproto.ItemCompleted, false: adapterproto.ItemRecorded}[last]; a.Ack != wantAck {
				t.Errorf("%s part %d of %d: %s, want %s", a.MessageID, a.Part, total[a.MessageID], a.Ack, wantAck)
			}
		}
		quiet(t, h)
		for id, parts := range seen {
			state := inOf(t, h, id).Record.State
			if done := len(parts) == total[id]; done != (state == delivery.StateDelivered) {
				t.Errorf("message %s has %d of %d parts acknowledged and is %s", id, len(parts), total[id], state)
			}
		}
	}
	if len(seen) != len(bodies) {
		t.Fatalf("the agent was handed %d messages, want %d", len(seen), len(bodies))
	}
	for id, want := range bodies {
		if got := strings.Join(seen[id], ""); got != want {
			t.Errorf("message %s read back differently: %d bytes, want %d", id, len(got), len(want))
		}
	}
	if visible != acked || total[short] != 1 {
		t.Errorf("%d parts were shown and %d acknowledged; the short message has %d parts", visible, acked, total[short])
	}
	if pages < 5+1 {
		t.Errorf("only %d pages for five messages of 60 KiB", pages)
	}
	h.Advance(pullInterval)
	quiet(t, h)
	for id := range bodies {
		if d := sourceDelivery(t, h, id); d.State != delivery.StateDelivered {
			t.Errorf("the source record of %s is %s, want delivered", id, d.State)
		}
	}
	if got := toldKinds(t, h, "a"); len(got) != 0 {
		t.Errorf("a was told %v, want nothing", got)
	}
}

// A lease belongs to the endpoint it was handed to. The token of another endpoint
// cannot acknowledge it, by naming the lease or by naming the message, and what the
// right endpoint holds is untouched.
func TestOtherEndpointTokenCannotAcknowledge(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	s1, addr1 := newSession(t, h, "a")
	s2, addr2 := newSession(t, h, "a")
	m1 := sendToAddr(t, h, "a", addr1, "for the first session")
	m2 := sendToAddr(t, h, "a", addr2, "for the second session")
	quiet(t, h)

	p1 := recvAs(t, h, s1, fakeagent.RecvInput{})
	wantOnly(t, p1, m1)
	p2 := recvAs(t, h, s2, fakeagent.RecvInput{})
	wantOnly(t, p2, m2)

	for _, tc := range []struct {
		name  string
		lease adapterproto.LeaseRef
		items []adapterproto.PartRef
	}{
		{"the lease of the first, all of its page", p1.Page.Lease, nil},
		{"the lease of the first, naming its message", p1.Page.Lease, []adapterproto.PartRef{{MessageID: m1, Part: 1}}},
	} {
		_, err := s2.Ack(h.Ctx(), fakeagent.AckInput{Lease: tc.lease, Items: tc.items})
		if fakeagent.CategoryOf(err) != fakeagent.CategoryNotFound || fakeagent.CodeOf(err) != "lease_unknown" {
			t.Errorf("%s: the second session got %v, want not_found/lease_unknown", tc.name, err)
		}
	}
	// With a lease of its own it still cannot reach a message that is not its own.
	acks := ackAs(t, h, s2, p2.Page.Lease, adapterproto.PartRef{MessageID: m1, Part: 1})
	if len(acks) != 1 || acks[0].Ack != adapterproto.ItemRefusedNoHandout {
		t.Errorf("naming the message of another endpoint under its own lease got %+v, want refused_no_handout", acks)
	}
	quiet(t, h)
	if in := inOf(t, h, m1); in.Record.State != delivery.StateAccepted || slices.Contains(in.Record.Lease.Acked, true) {
		t.Fatalf("the record of the first session is %s with acknowledged parts %v, want it untouched", in.Record.State, in.Record.Lease.Acked)
	}

	acks = ackAs(t, h, s1, p1.Page.Lease)
	if len(acks) != 1 || acks[0].Ack != adapterproto.ItemCompleted {
		t.Errorf("the first session's own acknowledgement is %+v, want completed", acks)
	}
	quiet(t, h)
	if in := inOf(t, h, m2); in.Record.State != delivery.StateAccepted {
		t.Errorf("the record of the second session is %s, want accepted: it did not acknowledge", in.Record.State)
	}
}
