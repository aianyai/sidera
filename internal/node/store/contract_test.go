package store_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/reply"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The records store the contract types field by field, so a field a contract
// package adds later would be dropped without a sign. These lists are the fields
// the stored shapes carry; the test fails when a type gains one, and whoever
// adds it decides how it is stored and extends the list and the round trip below.
func TestStoredContractTypesHaveNoFieldTheRecordsDoNotCarry(t *testing.T) {
	for _, c := range []struct {
		name   string
		typ    reflect.Type
		fields string
	}{
		{"delivery.Record", reflect.TypeOf(delivery.Record{}), "Side State Sub Reject Unknown StateAt Lost Control Exempt AcceptBefore ReportBy BacklogDeadline Reported Blocked BlockedCause Lease"},
		{"store.OutEnvelope", reflect.TypeOf(store.OutEnvelope{}), "To ReplyTo Thread Seq SentAt ReportBy ContentType Preset DiscloseRecipients"},
		{"delivery.Lease", reflect.TypeOf(delivery.Lease{}), "Handouts IDs Active IssuedAt ExpiresAt Chunks HandedOut Acked"},
		{"delivery.DedupRecord", reflect.TypeOf(delivery.DedupRecord{}), "Key Digest RetainUntil Reply HasReply"},
		{"delivery.Authorization", reflect.TypeOf(delivery.Authorization{}), "Kind Reject"},
		{"delivery.Item", reflect.TypeOf(delivery.Item{}), "Ref Feedback At"},
		{"delivery.Feedback", reflect.TypeOf(delivery.Feedback{}), "Kind Cause Reject State"},
		{"delivery.DeliveryRef", reflect.TypeOf(delivery.DeliveryRef{}), "Sender Message Recipient"},
		{"envelope.Outbound", reflect.TypeOf(envelope.Outbound{}), "Message From Kind BodyNonEmpty AcceptBefore Recipients ControlReplyDigest Derived"},
		{"envelope.Tracked", reflect.TypeOf(envelope.Tracked{}), "Address Delivery Arrival"},
		{"envelope.Arrival", reflect.TypeOf(envelope.Arrival{}), "DeliveredAt ArrivedAt"},
		{"reply.Decision", reflect.TypeOf(reply.Decision{}), "Reason Until"},
		{"handoff.Attempt", reflect.TypeOf(handoff.Attempt{}), "At Outcome Kind"},
		{"handoff.BlockedReport", reflect.TypeOf(handoff.BlockedReport{}), "Cause Since"},
		{"adapterproto.ProcessIdentity", reflect.TypeOf(adapterproto.ProcessIdentity{}), "PID StartKey StartedAtMS Container"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for i := 0; i < c.typ.NumField(); i++ {
				if f := c.typ.Field(i); f.IsExported() {
					got = append(got, f.Name)
				}
			}
			want := strings.Fields(c.fields)
			sort.Strings(got)
			sort.Strings(want)
			if strings.Join(got, " ") != strings.Join(want, " ") {
				t.Errorf("%s has the fields %v; the records carry %v. A field was added or removed: store it, then update this list", c.name, got, want)
			}
		})
	}
}

// The delivery records that cover, between them, every field of delivery.Record
// and every kind of feedback it can have reported. Each is a record the delivery
// rules make, by a constructor and the events that lead to its state: a record
// with every field set at once would be one no rule produces.
func deliveryRecordsOfEveryShape(t *testing.T) (source, target []delivery.Record) {
	t.Helper()
	accept := base.Add(time.Minute)
	src := func() delivery.Record { return delivery.NewSourceRecord(base, accept) }

	// Contact fails, the result becomes unknown, then it is delivered: the notice
	// of the failure, of the unknown result and its correction.
	a := src()
	a = applied(t, a, delivery.Event{Type: delivery.EventContactFailing, Cause: delivery.CauseNetwork, Since: base.Add(-delivery.UnreachableHintAfter)})
	a = applied(t, a, delivery.Event{Type: delivery.EventDeliveryUnknown})
	a = applied(t, a, delivery.Event{Type: delivery.EventDelivered})

	// Expired, received, blocked and unblocked: four notices of the sender's.
	b := src()
	b = applied(t, b, delivery.Event{Type: delivery.EventExpiryQueried, QueriedAt: delivery.ExpiryJudgeableAfter(accept).Add(time.Nanosecond)})
	b = applied(t, b, delivery.Event{Type: delivery.EventReceipt})
	b = applied(t, b, delivery.Event{Type: delivery.EventBlocked, Cause: delivery.CauseRuntimeAuth})
	b = applied(t, b, delivery.Event{Type: delivery.EventUnblocked})

	// Rejected at the target, and the target's answer that it holds no record of
	// a delivery it had accepted.
	c := applied(t, src(), delivery.Event{Type: delivery.EventRejected, Reject: delivery.RejectBacklogFull})
	d := applied(t, applied(t, src(), delivery.Event{Type: delivery.EventReceipt}), delivery.Event{Type: delivery.EventNoRecordAtTarget})

	// The sender's own loss.
	e, _ := lostRecord(base.Add(time.Hour))

	// A target message with two chunks, the first acknowledged, notified and blocked.
	f := tgtIn(1).Record
	f, _, err := f.Handout(base.Add(time.Minute), delivery.AuthorizationAllow, 2, 1, fixedLease)
	if err != nil {
		t.Fatal(err)
	}
	f, _, _, err = f.Ack(base.Add(time.Minute), delivery.Ack{Part: 1, Lease: fixedLease, TokenMatches: true})
	if err != nil {
		t.Fatal(err)
	}
	f = applied(t, f, delivery.Event{Type: delivery.EventNotified})
	f = applied(t, f, delivery.Event{Type: delivery.EventBlocked, Cause: delivery.CauseRuntimeAuth})

	// A target message whose backlog deadline passed: rejected with its reason.
	g := tgtIn(2).Record
	g, _, err = g.Tick(g.BacklogDeadline.Add(time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	// A control record, delivered by its committed reply.
	h := deliveredRecord(sessionCtl(1, 5).Record)
	// A notice of the system endpoint: exempt traffic, handed out without a second check.
	k, _, err := noticeIn(3).Record.Handout(base.Add(time.Minute), 0, 1, 1, fixedLease)
	if err != nil {
		t.Fatal(err)
	}
	return []delivery.Record{a, b, c, d, e}, []delivery.Record{f, g, h, k}
}

// Every field of the stored types, set to a value that is not the zero value in
// at least one record, comes back: nothing is dropped on the way to the disk.
func TestEveryStoredFieldSurvivesTheDisk(t *testing.T) {
	source, target := deliveryRecordsOfEveryShape(t)

	var kinds delivery.KindSet
	var all []delivery.Record
	for n, r := range source {
		out := srcOut(10+n, false)
		out.Outbound.Recipients[0].Delivery = r
		out.Body = nil
		data, err := store.EncodeRecord(out)
		if err != nil {
			t.Fatalf("source record %d (%v): %v", n, r.State, err)
		}
		got, dmg := store.DecodeRecord(store.OutDir, out.Name(), data)
		if dmg != nil {
			t.Fatalf("source record %d: %+v", n, dmg)
		}
		if !equalRecords(got, out) {
			t.Errorf("source record %d lost a field:\n got  %#v\n want %#v", n, got, out)
		}
		kinds |= r.Reported
		all = append(all, r)
	}
	var dedups []delivery.DedupRecord
	for n, r := range target {
		in := tgtIn(20 + n)
		in.Rev = uint64(n + 1)
		in.Record = r
		if r.Control {
			all = append(all, r)
			continue // the control record of a session is round-tripped below
		}
		in.Auth = delivery.Authorization{Kind: delivery.AuthorizationSuspend}
		if r.Exempt {
			in.Key = noticeKey(20 + n)
			in.Dedup = delivery.NewDedupRecord(in.Key.Dedup(), digestOf(20+n), r.ReportBy)
			in.Auth = delivery.Authorization{}
		}
		if !r.Exempt {
			in.ReplyDecision = reply.Decision{Reason: reply.ReasonArrivalWindow, Until: base.Add(25 * time.Hour)}
		} else {
			in.ReplyDecision = reply.Decision{}
		}
		in.Handout = store.HandoutFields{ContentType: "text/plain; charset=utf-8", ReplyTo: msgID(8), Thread: "thread", Seq: 3, SentAt: base.Add(time.Second)}
		in.AcceptedAt = base.Add(2 * time.Second)
		data, err := store.EncodeRecord(in)
		if err != nil {
			t.Fatalf("target record %d (%v): %v", n, r.State, err)
		}
		got, dmg := store.DecodeRecord(store.InDir, in.Name(), data)
		if dmg != nil {
			t.Fatalf("target record %d: %+v", n, dmg)
		}
		if !equalRecords(got, in) {
			t.Errorf("target record %d lost a field:\n got  %#v\n want %#v", n, got, in)
		}
		dedups = append(dedups, in.Dedup)
		all = append(all, in.Record)
	}

	// Every field of the delivery record is set in some record above, and every
	// kind of feedback is reported in some.
	for fi := 0; fi < reflect.TypeOf(delivery.Record{}).NumField(); fi++ {
		name := reflect.TypeOf(delivery.Record{}).Field(fi).Name
		seen := false
		for _, r := range all {
			seen = seen || !reflect.ValueOf(r).Field(fi).IsZero()
		}
		if !seen {
			t.Errorf("no record of the test has the field Record.%s set: the test does not show it survives the disk", name)
		}
	}
	for k := delivery.KindUnreachable; k <= delivery.LastRecordKind; k++ {
		if !kinds.Has(k) {
			t.Errorf("no source record of the test reports the feedback kind %s", k)
		}
	}

	var data []byte
	var err error
	var got store.Record
	var dmg *store.Damage
	out := srcOut(1, false)
	out.Outbound.Recipients[0].Delivery = applied(t, delivery.NewSourceRecord(base, out.Outbound.AcceptBefore),
		delivery.Event{Type: delivery.EventRejected, Reject: delivery.RejectBacklogFull})
	out.Outbound.Recipients[0].Arrival = envelope.Arrival{DeliveredAt: base.Add(time.Hour), ArrivedAt: base.Add(time.Minute)}
	out.Outbound.ControlReplyDigest = [32]byte{1, 2, 3}
	out.Outbound.Kind = envelope.KindSessionCreate
	out.Key.Control = true
	out.Envelope.Preset = "p"
	derived := envelope.Tracked{Address: addr(2, 6), Delivery: delivery.NewSourceRecord(base, out.Outbound.AcceptBefore), Arrival: envelope.Arrival{ArrivedAt: base}}
	out.Outbound.Derived = &derived
	out.ControlReply = []byte{0, 1, 2, 255}
	out.Idempotency = &store.Idempotency{Key: "k-1", RetainUntil: base.Add(48 * time.Hour)}
	out.Feedback = []delivery.Item{{
		Ref:      delivery.DeliveryRef{Sender: addr(local, 1), Message: msgID(1), Recipient: addr(2, 2)},
		Feedback: delivery.Feedback{Kind: delivery.KindRejected, Cause: delivery.CauseNetwork, Reject: delivery.RejectBacklogFull, State: delivery.StateRejected}, At: base,
	}}
	data, err = store.EncodeRecord(out)
	if err != nil {
		t.Fatal(err)
	}
	got, dmg = store.DecodeRecord(store.OutDir, out.Name(), data)
	if dmg != nil {
		t.Fatalf("%+v", dmg)
	}
	if !equalRecords(got, out) {
		t.Errorf("a field was dropped:\n got  %#v\n want %#v", got, out)
	}

	// A control record's stored reply and a target record's digest.
	c := sessionCtl(1, 5)
	c.Dedup.Digest = delivery.BodyDigest{9, 9, 9}
	c.Dedup, _ = c.Dedup.WithReply([]byte("stored reply"))
	data, err = store.EncodeRecord(c)
	if err != nil {
		t.Fatal(err)
	}
	got, dmg = store.DecodeRecord(store.CtlDir, c.Name(), data)
	if dmg != nil || !equalRecords(got, c) {
		t.Errorf("a control record's stored reply was dropped: %+v\n got %#v\nwant %#v", dmg, got, c)
	}
}
