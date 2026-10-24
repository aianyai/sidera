package envelope

import (
	"encoding/hex"
	"reflect"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestGoldenIdentifiers(t *testing.T) {
	if got := hex.EncodeToString(nodeOf(t, keyIdentityA).Bytes()); got != goldenNodeA {
		t.Errorf("node A = %s, want %s", got, goldenNodeA)
	}
	if got := hex.EncodeToString(nodeOf(t, keyIdentityB).Bytes()); got != goldenNodeB {
		t.Errorf("node B = %s, want %s", got, goldenNodeB)
	}
}

// goldenRecord checks one record vector from every side: the bytes decode and
// verify under the descriptor, the digest and the signed bytes are the ones
// computed independently, and re-sealing the expected payload gives the same
// bytes (Ed25519 signatures are deterministic).
func goldenRecord[P any](t *testing.T, name string, golden []byte, digest, signed string, d record.Descriptor[P], want P) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		rec := mustDecodeRecord(t, golden)
		v, err := record.Verify(d, rec)
		requireNoErr(t, err)
		if !reflect.DeepEqual(v.Payload(), want) {
			t.Errorf("payload = %+v, want %+v", v.Payload(), want)
		}
		if got := v.Digest().String(); got != digest {
			t.Errorf("digest = %s, want %s", got, digest)
		}
		if got := hex.EncodeToString(record.SignedBytes(rec.Type, rec.Payload)); got != signed {
			t.Errorf("signed bytes = %s, want %s", got, signed)
		}
		sealed, err := record.Seal(d, want, signerOf(t, keyNetworkA))
		requireNoErr(t, err)
		enc, err := sealed.Encode()
		requireNoErr(t, err)
		if hex.EncodeToString(enc) != hex.EncodeToString(golden) {
			t.Errorf("sealing the expected payload gives other bytes:\n got %x\nwant %x", enc, golden)
		}
		again, err := rec.Encode()
		requireNoErr(t, err)
		if hex.EncodeToString(again) != hex.EncodeToString(golden) {
			t.Errorf("decode then encode changed the bytes")
		}
	})
}

func TestGoldenStatusReports(t *testing.T) {
	d := StatusReportDescriptor(nodeOf(t, keyIdentityA), acceptedFor(t, keyIdentityA, keyNetworkA), t0.Add(time.Hour))
	b := nodeOf(t, keyIdentityB)
	goldenRecord(t, "delivered", goldenReportDelivered, goldenReportDeliveredDigest, goldenReportDeliveredSignedBytes, d, StatusReport{
		Sender: b, Message: "ctl-0001", Recipient: ep(3), Status: delivery.StatusReport{State: delivery.StateDelivered},
		ReportedAt: t0.Add(760 * time.Second), DeliveredAt: t0.Add(700 * time.Second),
	})
	goldenRecord(t, "accepted and blocked", goldenReportBlocked, goldenReportBlockedDigest, goldenReportBlockedSignedBytes, d, StatusReport{
		Sender: b, Message: "msg-0001", Recipient: ep(1),
		Status:     delivery.StatusReport{State: delivery.StateAccepted, Blocked: true, BlockedCause: delivery.CauseAdapterOffline},
		ReportedAt: t0.Add(120 * time.Second),
	})
	goldenRecord(t, "rejected", goldenReportRejected, goldenReportRejectedDigest, goldenReportRejectedSignedBytes, d, StatusReport{
		Sender: b, Message: "msg-0003", Recipient: ep(1),
		Status:     delivery.StatusReport{State: delivery.StateRejected, Reject: delivery.RejectBacklogExpired},
		ReportedAt: t0.Add(4000 * time.Second),
	})
	goldenRecord(t, "delivery unknown, lost at target", goldenReportLostAtTarget, goldenReportLostAtTargetDigest, goldenReportLostAtTargetSignedBytes, d, StatusReport{
		Sender: b, Message: "msg-0004", Recipient: ep(1),
		Status:     delivery.StatusReport{State: delivery.StateDeliveryUnknown, Unknown: delivery.UnknownLostAtTarget},
		ReportedAt: t0.Add(900 * time.Second),
	})
	goldenRecord(t, "delivery unknown, handed over (no key 9)", goldenReportHandedOver, goldenReportHandedOverDigest, goldenReportHandedOverSignedBytes, d, StatusReport{
		Sender: b, Message: "msg-0005", Recipient: ep(1),
		Status:     delivery.StatusReport{State: delivery.StateDeliveryUnknown, Unknown: delivery.UnknownHandedOver},
		ReportedAt: t0.Add(950 * time.Second),
	})
	goldenRecord(t, "rejected, backlog full", goldenReportBacklogFull, goldenReportBacklogFullDigest, goldenReportBacklogFullSignedBytes, d, StatusReport{
		Sender: b, Message: "msg-0004", Recipient: ep(1),
		Status:     delivery.StatusReport{State: delivery.StateRejected, Reject: delivery.RejectBacklogFull},
		ReportedAt: t0.Add(4100 * time.Second),
	})
}

func TestGoldenControlReplies(t *testing.T) {
	d := ControlReplyDescriptor(nodeOf(t, keyIdentityA), acceptedFor(t, keyIdentityA, keyNetworkA), t0.Add(time.Hour))
	b := nodeOf(t, keyIdentityB)
	goldenRecord(t, "create created", goldenReplyCreated, goldenReplyCreatedDigest, goldenReplyCreatedSignedBytes, d, ControlReply{
		Sender: b, Message: "ctl-0001", Recipient: ep(2), IssuedAt: t0.Add(650 * time.Second),
		Result: ControlResult{Signal: adapterproto.SignalSessionCreate, Create: &CreateResult{
			Outcome: adapterproto.CreateReplyCreated, SessionEndpoint: ep(3)}},
	})
	goldenRecord(t, "create failed", goldenReplyFailed, goldenReplyFailedDigest, goldenReplyFailedSignedBytes, d, ControlReply{
		Sender: b, Message: "ctl-0002", Recipient: ep(2), IssuedAt: t0.Add(30 * time.Second),
		Result: ControlResult{Signal: adapterproto.SignalSessionCreate, Create: &CreateResult{
			Outcome: adapterproto.CreateReplyFailed, Reason: adapterproto.CreateReasonPresetUnavailable}},
	})
	goldenRecord(t, "close unconfirmed", goldenReplyClose, goldenReplyCloseDigest, goldenReplyCloseSignedBytes, d, ControlReply{
		Sender: b, Message: "ctl-0003", Recipient: ep(3), IssuedAt: t0.Add(40 * time.Second),
		Result: ControlResult{Signal: adapterproto.SignalSessionClose, Close: &adapterproto.ControlCloseReply{
			Outcome: adapterproto.CloseReplyUnconfirmed, Reason: adapterproto.CloseReasonAdapterOffline}},
	})
	goldenRecord(t, "interrupt failed", goldenReplyInterrupt, goldenReplyInterruptDigest, goldenReplyInterruptSignedBytes, d, ControlReply{
		Sender: b, Message: "ctl-0005", Recipient: ep(3), IssuedAt: t0.Add(50 * time.Second),
		Result: ControlResult{Signal: adapterproto.SignalSessionInterrupt, Interrupt: &adapterproto.ControlInterruptReply{
			Outcome: adapterproto.InterruptReplyFailed, Reason: adapterproto.InterruptReasonNativeError}},
	})
	goldenRecord(t, "endpoint status", goldenReplyStatus, goldenReplyStatusDigest, goldenReplyStatusSignedBytes, d, ControlReply{
		Sender: b, Message: "ctl-0004", Recipient: ep(1), IssuedAt: t0.Add(90 * time.Second),
		Result: ControlResult{Signal: adapterproto.SignalEndpointStatus, Status: &adapterproto.ControlStatusReply{
			Activity: adapterproto.ActivityBusy, Queued: 3,
			Conditions: []adapterproto.ControlCondition{adapterproto.ControlConditionAdapterOffline, adapterproto.ControlConditionServerRequestRejected},
			Presets:    []adapterproto.PresetAvailability{{Name: "default", Available: true}, {Name: "fast", Available: false}},
			AsOf:       t0.Unix() + 90,
		}},
	})
}

func goldenEnvelope(t *testing.T, name string, golden []byte, want Envelope) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		got, err := Decode(golden, testLimits)
		requireNoErr(t, err)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("decoded = %+v\nwant      %+v", got, want)
		}
		enc, err := want.Encode(testLimits)
		requireNoErr(t, err)
		if hex.EncodeToString(enc) != hex.EncodeToString(golden) {
			t.Errorf("encoding the expected envelope gives other bytes:\n got %x\nwant %x", enc, golden)
		}
	})
}

func TestGoldenEnvelopes(t *testing.T) {
	nodeA, nodeB := nodeOf(t, keyIdentityA), nodeOf(t, keyIdentityB)
	accept := t0.Add(600 * time.Second)
	reportBy := accept.Add(10920 * time.Second)
	if !reportBy.Equal(delivery.ReportBy(accept)) {
		t.Fatalf("the independent report_by offset disagrees with delivery.ReportBy: %v vs %v", reportBy, delivery.ReportBy(accept))
	}
	goldenEnvelope(t, "message", goldenEnvelopeMessage, Envelope{
		ID: "msg-0001", To: []ident.Address{addr(nodeA, 1)}, Kind: KindMessage,
		Seq:    map[ident.EndpointID]uint64{ep(1): 1},
		SentAt: t0, AcceptBefore: accept, ReportBy: reportBy,
		ContentType: "text/plain", Body: []byte("hello"),
	})
	goldenEnvelope(t, "session create with an initial message", goldenEnvelopeSessionCreate, Envelope{
		ID: "ctl-0001", To: []ident.Address{addr(nodeA, 2)}, Kind: KindSessionCreate,
		Seq:    map[ident.EndpointID]uint64{ep(2): 7},
		SentAt: t0, AcceptBefore: accept, ReportBy: reportBy,
		ContentType: "text/plain; charset=utf-8", Body: []byte("start here"), Preset: "default",
	})
	goldenEnvelope(t, "reply with evidence to disclosed recipients", goldenEnvelopeReply, Envelope{
		ID: "msg-0002", To: []ident.Address{addr(nodeB, 0x0a), addr(nodeB, 0x0b)},
		MultiRecipient: true, DiscloseRecipients: true, Kind: KindMessage,
		ReplyTo: "ctl-0001", Thread: "t1",
		Evidence: []record.Record{mustDecodeRecord(t, goldenReplyCreated), mustDecodeRecord(t, goldenReportDelivered)},
		Seq:      map[ident.EndpointID]uint64{ep(0x0a): 3, ep(0x0b): 4},
		SentAt:   t0, AcceptBefore: accept, ReportBy: reportBy,
		ContentType: "text/plain", Body: []byte("done"),
	})
}

func TestGoldenRequestDigest(t *testing.T) {
	for name, c := range map[string]struct {
		envelope []byte
		want     string
	}{
		"a session.create with an initial message":      {goldenEnvelopeSessionCreate, goldenSessionCreateRequestDigest},
		"a reply with a thread to disclosed recipients": {goldenEnvelopeReply, goldenReplyRequestDigest},
	} {
		t.Run(name, func(t *testing.T) {
			e, err := Decode(c.envelope, testLimits)
			requireNoErr(t, err)
			d := e.Digest()
			if got := hex.EncodeToString(d[:]); got != c.want {
				t.Errorf("request digest = %s, want %s", got, c.want)
			}
		})
	}
}
