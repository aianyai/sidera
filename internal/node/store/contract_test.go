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
		{"delivery.Record", reflect.TypeOf(delivery.Record{}), "Side State Sub Reject Control AcceptBefore ReportBy BacklogDeadline Reported Blocked BlockedCause Lease"},
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

// Every field of the stored types, set to a value that is not the zero value,
// comes back: nothing is dropped on the way to the disk.
func TestEveryStoredFieldSurvivesTheDisk(t *testing.T) {
	lid1, _ := delivery.ParseLeaseID("00112233445566778899aabbccddeeff")
	lid2, _ := delivery.ParseLeaseID("ffeeddccbbaa99887766554433221100")
	full := delivery.Record{
		Side: delivery.SideTarget, State: delivery.StateAccepted, Sub: delivery.SubNotified, Reject: delivery.RejectNone, Control: false,
		AcceptBefore: base.Add(time.Minute), ReportBy: base.Add(5 * time.Hour), BacklogDeadline: base.Add(30 * time.Minute),
		Reported: delivery.KindSet(0).With(delivery.KindUnreachable).With(delivery.KindDeliveryBlocked).With(delivery.KindUnknownCorrected),
		Blocked:  true, BlockedCause: delivery.CauseNativeUnavailable,
		Lease: delivery.Lease{Handouts: 3, IDs: []delivery.LeaseID{lid1, lid2}, Active: true, IssuedAt: base.Add(time.Second), ExpiresAt: base.Add(time.Hour),
			Chunks: 3, HandedOut: []bool{true, true, false}, Acked: []bool{true, false, false}},
	}
	in := tgtIn(1)
	in.Rev = 7
	in.Record = full
	in.Auth = delivery.Authorization{Kind: delivery.AuthorizationSuspend}
	in.ReplyDecision = reply.Decision{Reason: reply.ReasonArrivalWindow, Until: base.Add(25 * time.Hour)}
	in.Handout = store.HandoutFields{ContentType: "text/plain; charset=utf-8", ReplyTo: msgID(8), Thread: "thread", Seq: 3, SentAt: base.Add(time.Second)}
	in.AcceptedAt = base.Add(2 * time.Second)
	data, err := store.EncodeRecord(in)
	if err != nil {
		t.Fatal(err)
	}
	got, dmg := store.DecodeRecord(store.InDir, in.Name(), data)
	if dmg != nil {
		t.Fatalf("%+v", dmg)
	}
	if !equalRecords(got, in) {
		t.Errorf("a field was dropped:\n got  %#v\n want %#v", got, in)
	}

	out := srcOut(1, false)
	out.Outbound.Recipients[0].Delivery = func() delivery.Record {
		r := delivery.NewSourceRecord(out.Outbound.AcceptBefore)
		r.Reported = delivery.KindSet(0).With(delivery.KindRejected)
		return r
	}()
	out.Outbound.Recipients[0].Arrival = envelope.Arrival{DeliveredAt: base.Add(time.Hour), ArrivedAt: base.Add(time.Minute)}
	out.Outbound.ControlReplyDigest = [32]byte{1, 2, 3}
	out.Outbound.Kind = envelope.KindSessionCreate
	out.Key.Control = true
	out.Envelope.Preset = "p"
	derived := envelope.Tracked{Address: addr(2, 6), Delivery: delivery.NewSourceRecord(out.Outbound.AcceptBefore), Arrival: envelope.Arrival{ArrivedAt: base}}
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
