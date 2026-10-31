package apiwire_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func directQuiet(t *testing.T, h *harness.H) {
	t.Helper()
	if err := h.Quiesce(); err != nil {
		t.Fatal(err)
	}
}

func directScan(t *testing.T, h *harness.H) *store.Scan {
	t.Helper()
	directQuiet(t, h)
	s, err := h.Records()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Compare persisted message records, including leases and acknowledgements, so
// refusals cannot accidentally pass by returning the right code after an effect.
type persistedMessages struct {
	out []store.OutRecord
	in  []store.InRecord
}

func messageSnapshot(t *testing.T, h *harness.H) persistedMessages {
	t.Helper()
	s := directScan(t, h)
	return persistedMessages{s.Out, s.In}
}

func directRef(t *testing.T, address string) adapterproto.EndpointRef {
	t.Helper()
	a, err := ident.ParseAddress(address)
	if err != nil {
		t.Fatal(err)
	}
	return adapterproto.EndpointRef(a.Endpoint.String())
}

func directController(t *testing.T, h *harness.H, instance string) fakeadapter.Controller {
	t.Helper()
	c, err := h.Adapter(instance)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func directOut(t *testing.T, h *harness.H, id adapterproto.MessageID) store.OutRecord {
	t.Helper()
	for _, o := range directScan(t, h).Out {
		if string(o.Key.Message) == string(id) {
			return o
		}
	}
	t.Fatalf("no persisted message %s", id)
	return store.OutRecord{}
}

func TestDirectProtocolPreservesSendControlAndReplyFields(t *testing.T) {
	h := harness.New(t, harness.Options{Adapter: &harness.AdapterOptions{Runtime: true}})
	a, b, c := h.MustDirect("a"), h.MustDirect("b"), h.MustDirect("c")
	ctl := directController(t, h, harness.Instance)
	refA, refB := directRef(t, a), directRef(t, b)
	question, err := ctl.Send(h.Ctx(), adapterproto.SendParams{Endpoint: refA, To: []adapterproto.Address{adapterproto.Address(b)}, Body: "question", ContentType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	directQuiet(t, h)
	for _, tc := range []struct {
		name   string
		params adapterproto.SendParams
		kind   envelope.Kind
		preset string
	}{
		{"ordinary", adapterproto.SendParams{Endpoint: refA, To: []adapterproto.Address{adapterproto.Address(b), adapterproto.Address(c)}, Body: "ordinary body", ContentType: "text/markdown", DiscloseRecipients: true}, envelope.KindMessage, ""},
		{"reply", adapterproto.SendParams{Endpoint: refB, To: []adapterproto.Address{adapterproto.Address(a)}, Body: "reply body", ContentType: "text/markdown", ReplyTo: question.MessageID, DiscloseRecipients: true}, envelope.KindMessage, ""},
		{"creation", adapterproto.SendParams{Endpoint: refA, To: []adapterproto.Address{adapterproto.Address(h.RuntimeAddr())}, Body: "initial body", ContentType: "text/markdown", Control: &adapterproto.Control{Signal: adapterproto.SignalSessionCreate, Params: &adapterproto.ControlParams{Preset: "default"}}}, envelope.KindSessionCreate, "default"},
		{"close", adapterproto.SendParams{Endpoint: refA, To: []adapterproto.Address{adapterproto.Address(b)}, Control: &adapterproto.Control{Signal: adapterproto.SignalSessionClose}}, envelope.KindSessionClose, ""},
		{"interrupt", adapterproto.SendParams{Endpoint: refA, To: []adapterproto.Address{adapterproto.Address(b)}, Control: &adapterproto.Control{Signal: adapterproto.SignalSessionInterrupt}}, envelope.KindSessionInterrupt, ""},
		{"status", adapterproto.SendParams{Endpoint: refA, To: []adapterproto.Address{adapterproto.Address(b)}, Control: &adapterproto.Control{Signal: adapterproto.SignalEndpointStatus}}, envelope.KindEndpointStatus, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.params
			p.Thread = "thread-" + tc.name
			p.AcceptBeforeS = 173
			p.IdempotencyKey = "key-" + tc.name
			sent, err := ctl.Send(h.Ctx(), p)
			if err != nil {
				t.Fatal(err)
			}
			o := directOut(t, h, sent.MessageID)
			var tos []ident.Address
			for _, raw := range p.To {
				a, err := ident.ParseAddress(string(raw))
				if err != nil {
					t.Fatal(err)
				}
				tos = append(tos, a)
			}
			if !reflect.DeepEqual(o.Envelope.To, tos) || o.Envelope.Thread != p.Thread || string(o.Envelope.ReplyTo) != string(p.ReplyTo) || o.Envelope.ContentType != p.ContentType || o.Envelope.Preset != tc.preset || o.Outbound.Kind != tc.kind || o.Envelope.DiscloseRecipients != (p.DiscloseRecipients && len(p.To) > 1) || o.Idempotency == nil || o.Idempotency.Key != p.IdempotencyKey || o.Outbound.AcceptBefore.Sub(o.Envelope.SentAt) != 173*time.Second {
				t.Fatalf("lost fields: %+v", o)
			}
			env := envelope.Envelope{ID: delivery.MessageID(sent.MessageID), To: tos, MultiRecipient: len(tos) > 1,
				DiscloseRecipients: p.DiscloseRecipients && len(tos) > 1, Kind: tc.kind,
				ReplyTo: delivery.MessageID(p.ReplyTo), Thread: p.Thread, Seq: o.Envelope.Seq,
				SentAt: o.Envelope.SentAt, AcceptBefore: o.Outbound.AcceptBefore, ReportBy: o.Envelope.ReportBy,
				ContentType: p.ContentType, Body: []byte(p.Body), Preset: tc.preset}
			if env.Digest() != o.BodyDigest {
				t.Fatal("persisted request digest lost body or fields")
			}

			before := messageSnapshot(t, h)
			repeated, err := ctl.Send(h.Ctx(), p)
			if err != nil || repeated.MessageID != sent.MessageID {
				t.Fatalf("replay: %+v %v", repeated, err)
			}
			if after := messageSnapshot(t, h); !reflect.DeepEqual(after, before) {
				t.Fatal("replay changed persisted messages")
			}
			p.Thread = "conflicting-thread"
			_, err = ctl.Send(h.Ctx(), p)
			if pe := wireError(t, err); pe.Code != 1009 {
				t.Fatalf("conflict: %v", pe)
			}
			if after := messageSnapshot(t, h); !reflect.DeepEqual(after, before) {
				t.Fatal("conflict changed persisted messages or acknowledgements")
			}
		})
	}
	page, err := ctl.Recv(h.Ctx(), adapterproto.RecvParams{Endpoint: refB})
	if err != nil {
		t.Fatal(err)
	}
	var items []adapterproto.PartRef
	for _, m := range page.Page.Messages {
		items = append(items, adapterproto.PartRef{MessageID: m.ID, Part: 1})
	}
	if len(items) == 0 {
		t.Fatal("no real received messages")
	}
	ack, err := ctl.Ack(h.Ctx(), adapterproto.AckParams{Endpoint: refB, Lease: page.Page.Lease, Items: items})
	if err != nil || len(ack.Items) != len(items) {
		t.Fatalf("ack %+v %v", ack, err)
	}
	h.Advance(time.Minute)
	directQuiet(t, h)
	status, err := ctl.MessageStatus(h.Ctx(), adapterproto.MessageStatusParams{Endpoint: refA, MessageID: question.MessageID})
	if err != nil || len(status.Recipients) != 1 || status.Recipients[0].State != adapterproto.DeliveryDelivered {
		t.Fatalf("status %+v %v", status, err)
	}
}

func TestDirectProtocolRefusalsLeaveMessagesAndAcknowledgementsUnchanged(t *testing.T) {
	h := harness.New(t, harness.Options{Adapter: &harness.AdapterOptions{Runtime: true, ExtraInstances: []string{"other"}}})
	a, b, c := h.MustDirect("a"), h.MustDirect("b"), h.MustDirect("c")
	ctl, other := directController(t, h, harness.Instance), directController(t, h, "other")
	refA, refB, refC := directRef(t, a), directRef(t, b), directRef(t, c)
	good := adapterproto.SendParams{Endpoint: refA, To: []adapterproto.Address{adapterproto.Address(b)}, Body: "question", ContentType: "text/plain"}
	sent, err := ctl.Send(h.Ctx(), good)
	if err != nil {
		t.Fatal(err)
	}
	directQuiet(t, h)
	page, err := ctl.Recv(h.Ctx(), adapterproto.RecvParams{Endpoint: refB})
	if err != nil || len(page.Page.Messages) != 1 {
		t.Fatalf("page %+v %v", page, err)
	}
	closed := h.MustDirect("closed")
	if err := ctl.Unregister(h.Ctx(), adapterproto.UnregisterParams{Endpoint: directRef(t, closed)}); err != nil {
		t.Fatal(err)
	}
	// A session is obtained from an actual accepted creation, never fabricated.
	creation, err := ctl.Send(h.Ctx(), adapterproto.SendParams{Endpoint: refA, To: []adapterproto.Address{adapterproto.Address(h.RuntimeAddr())}, Control: &adapterproto.Control{Signal: adapterproto.SignalSessionCreate, Params: &adapterproto.ControlParams{Preset: "default"}}})
	if err != nil {
		t.Fatal(err)
	}
	directQuiet(t, h)
	var session adapterproto.EndpointRef
	for _, ep := range directScan(t, h).Endpoints {
		if ep.Kind == adapterreg.KindSession && ep.Status == store.EndpointOpen {
			session = adapterproto.EndpointRef(ep.ID.String())
		}
	}
	if session == "" {
		t.Fatal("actual session creation made no open session")
	}

	for _, ref := range []adapterproto.EndpointRef{refA, adapterproto.EndpointRef(a), directRef(t, closed), session} {
		client := ctl
		if ref == refA {
			client = other
		}
		for _, method := range []string{"send", "recv", "ack", "status"} {
			t.Run(string(ref)+"/"+method, func(t *testing.T) {
				before := messageSnapshot(t, h)
				var err error
				switch method {
				case "send":
					p := good
					p.Endpoint = ref
					_, err = client.Send(h.Ctx(), p)
				case "recv":
					_, err = client.Recv(h.Ctx(), adapterproto.RecvParams{Endpoint: ref})
				case "ack":
					_, err = client.Ack(h.Ctx(), adapterproto.AckParams{Endpoint: ref, Lease: page.Page.Lease, Items: []adapterproto.PartRef{{MessageID: sent.MessageID, Part: 1}}})
				case "status":
					_, err = client.MessageStatus(h.Ctx(), adapterproto.MessageStatusParams{Endpoint: ref, MessageID: sent.MessageID})
				}
				if pe := wireError(t, err); pe.Code != 1001 {
					t.Fatalf("%s: %+v", method, pe)
				}
				if !reflect.DeepEqual(messageSnapshot(t, h), before) {
					t.Fatal("refusal changed persisted messages or acknowledgements")
				}
			})
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*adapterproto.SendParams)
		code   int
	}{
		{"invalid address", func(p *adapterproto.SendParams) { p.To = []adapterproto.Address{"not-an-address"} }, 1008},
		{"too large", func(p *adapterproto.SendParams) { p.Body = strings.Repeat("x", envelope.MaxBody+1) }, 1009},
		{"not received", func(p *adapterproto.SendParams) { p.ReplyTo = sent.MessageID }, 1009},
		{"wrong reply recipient", func(p *adapterproto.SendParams) {
			p.Endpoint = refB
			p.ReplyTo = sent.MessageID
			p.To = []adapterproto.Address{adapterproto.Address(c)}
		}, 1009},
		{"multiple reply recipients", func(p *adapterproto.SendParams) {
			p.Endpoint = refB
			p.ReplyTo = sent.MessageID
			p.To = []adapterproto.Address{adapterproto.Address(a), adapterproto.Address(c)}
		}, 1009},
		{"control reply", func(p *adapterproto.SendParams) {
			p.Body = ""
			p.ContentType = ""
			p.Control = &adapterproto.Control{Signal: adapterproto.SignalEndpointStatus}
			p.ReplyTo = sent.MessageID
		}, 1009},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := good
			tc.mutate(&p)
			before := messageSnapshot(t, h)
			_, err := ctl.Send(h.Ctx(), p)
			if pe := wireError(t, err); pe.Code != tc.code {
				t.Fatalf("%+v", pe)
			}
			if !reflect.DeepEqual(messageSnapshot(t, h), before) {
				t.Fatal("refused send changed persisted messages or acknowledgements")
			}
		})
	}
	before := messageSnapshot(t, h)
	_, err = ctl.Ack(h.Ctx(), adapterproto.AckParams{Endpoint: refC, Lease: page.Page.Lease, Items: []adapterproto.PartRef{{MessageID: sent.MessageID, Part: 1}}})
	if pe := wireError(t, err); pe.Code != 1010 {
		t.Fatalf("foreign lease: %+v", pe)
	}
	foreignPart, err := ctl.Ack(h.Ctx(), adapterproto.AckParams{Endpoint: refB, Lease: page.Page.Lease, Items: []adapterproto.PartRef{{MessageID: creation.MessageID, Part: 1}, {MessageID: sent.MessageID, Part: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range foreignPart.Items {
		if it.Ack != adapterproto.ItemRefusedNoHandout && it.Ack != adapterproto.ItemRefusedLease {
			t.Fatalf("acknowledged unreceived part: %+v", it)
		}
	}
	if !reflect.DeepEqual(messageSnapshot(t, h), before) {
		t.Fatal("foreign lease or part changed persisted messages or acknowledgements")
	}
	var unknown string
	for _, id := range []adapterproto.MessageID{sent.MessageID, "00000000000000000000000000000000"} {
		_, err := ctl.MessageStatus(h.Ctx(), adapterproto.MessageStatusParams{Endpoint: refB, MessageID: id})
		pe := wireError(t, err)
		if pe.Code != -32602 {
			t.Fatalf("status %+v", pe)
		}
		raw, _ := json.Marshal(pe)
		if unknown != "" && unknown != string(raw) {
			t.Fatal("status disclosed message ownership")
		}
		unknown = string(raw)
	}
	if !reflect.DeepEqual(messageSnapshot(t, h), before) {
		t.Fatal("status query changed persisted messages or acknowledgements")
	}
	// Asking recv for a message the endpoint holds nothing of is the same fixed
	// refusal as for its status, whether the message exists or not.
	for _, id := range []adapterproto.MessageID{creation.MessageID, "00000000000000000000000000000000"} {
		_, err := ctl.Recv(h.Ctx(), adapterproto.RecvParams{Endpoint: refB, MessageID: id, Part: 1})
		pe := wireError(t, err)
		if pe.Code != -32602 {
			t.Fatalf("recv of an unknown message %+v", pe)
		}
		raw, _ := json.Marshal(pe)
		if string(raw) != unknown {
			t.Fatalf("recv of an unknown message answered %s, status answered %s", raw, unknown)
		}
	}
	if !reflect.DeepEqual(messageSnapshot(t, h), before) {
		t.Fatal("recv of an unknown message changed persisted messages or acknowledgements")
	}
	// A rejected target record is not receipt and grants no reply permission.
	paused := true
	if err := h.Agent("c").Declare(h.Ctx(), fakeagent.DeclareInput{Paused: &paused}); err != nil {
		t.Fatal(err)
	}
	p := good
	p.To = []adapterproto.Address{adapterproto.Address(c)}
	rejected, err := ctl.Send(h.Ctx(), p)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, in := range directScan(t, h).In {
		if in.Key.Message == delivery.MessageID(rejected.MessageID) {
			found = true
			if in.Record.State != delivery.StateRejected {
				t.Fatal("premise: message was not rejected")
			}
		}
	}
	if !found {
		t.Fatal("no rejected record")
	}
	before = messageSnapshot(t, h)
	p.Endpoint = refC
	p.To = []adapterproto.Address{adapterproto.Address(a)}
	p.ReplyTo = rejected.MessageID
	_, err = ctl.Send(h.Ctx(), p)
	if pe := wireError(t, err); pe.Code != 1009 {
		t.Fatalf("rejected reply %+v", pe)
	}
	if !reflect.DeepEqual(messageSnapshot(t, h), before) {
		t.Fatal("rejected reply changed persisted messages or acknowledgements")
	}
}
