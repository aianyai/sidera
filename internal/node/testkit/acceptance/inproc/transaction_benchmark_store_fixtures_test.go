package inproc

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/reply"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Fixed typed inputs shared by the store benchmark owners. These construct no
// disk state and make no transitions: each sample calls its real owner to prepare it.
type transactionBenchmarkStoreRecords struct {
	body             []byte
	out              store.OutRecord
	in               store.InRecord
	ctl              store.CtlRecord
	endpoint         store.EndpointRecord
	creatingEndpoint store.EndpointRecord
	derived          store.InRecord
}

func newTransactionBenchmarkStoreRecords(b *testing.B) transactionBenchmarkStoreRecords {
	b.Helper()
	at := harness.DefaultStart
	body := []byte("transaction benchmark message")
	ref := store.BodyRefOf(body)
	sender, target := transactionBenchmarkStoreEndpoint(b, 1), transactionBenchmarkStoreEndpoint(b, 2)
	node, err := ident.NodeIDFromBytes(bytes.Repeat([]byte{3}, ident.NodeIDLen))
	if err != nil {
		b.Fatal(err)
	}
	to := ident.Address{Node: node, Endpoint: target}
	accept := at.Add(time.Hour)
	reportBy := delivery.ReportBy(accept)
	out := store.OutRecord{
		Key: store.OutKey{Sender: sender, Message: "benchmark-source", Recipients: []ident.Address{to}},
		Outbound: envelope.Outbound{Message: "benchmark-source", From: sender, Kind: envelope.KindMessage,
			BodyNonEmpty: true, AcceptBefore: accept,
			Recipients: []envelope.Tracked{{Address: to, Delivery: delivery.NewSourceRecord(at, accept)}}},
		Envelope: store.OutEnvelope{To: []ident.Address{to}, Seq: map[ident.EndpointID]uint64{target: 1},
			SentAt: at, ReportBy: reportBy, ContentType: "text/plain"},
		Body: &ref, BodyDigest: delivery.BodyDigest(ref.Digest),
	}
	targetKey := store.TargetKey{SenderNode: node, Sender: sender, Message: "benchmark-target", Recipient: target}
	record, err := delivery.NewTargetRecord(at, accept, reportBy, delivery.DefaultEndpointMaxQueue)
	if err != nil {
		b.Fatal(err)
	}
	in := store.InRecord{Key: targetKey, Record: record,
		Dedup: delivery.NewDedupRecord(targetKey.Dedup(), delivery.BodyDigest(ref.Digest), reportBy),
		Auth:  delivery.Authorization{Kind: delivery.AuthorizationAllow}, ReplyDecision: reply.Decision{Reason: reply.ReasonDisabled},
		Handout: store.HandoutFields{ContentType: "text/plain", SentAt: at}, AcceptedAt: at, Body: &ref}
	ctlKey := targetKey
	ctlKey.Message = "benchmark-control"
	ctlKey.Recipient = transactionBenchmarkStoreEndpoint(b, 4)
	ctl := store.CtlRecord{Key: ctlKey, Record: delivery.NewTargetControlRecord(at, accept, reportBy),
		Dedup: delivery.NewDedupRecord(ctlKey.Dedup(), delivery.BodyDigest(ref.Digest), reportBy),
		Kind:  envelope.KindSessionCreate, Preset: "default", SessionEndpoint: target, AcceptedAt: at, SentAt: at,
		Initial: &store.InitialMessage{ContentType: "text/plain", Body: ref},
		Intent:  &store.CreateIntent{Attempt: adapterproto.AttemptID("0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6"), Endpoint: target, State: store.IntentCreating}}
	ep := store.EndpointRecord{ID: target, Kind: adapterreg.KindDirect, Instance: "benchmark",
		Status: store.EndpointOpen, Token: bytes.Repeat([]byte{4}, store.TokenLen), Declaration: []byte(`{"name":"benchmark"}`)}

	creating := ep
	creating.Kind, creating.Status = adapterreg.KindSession, store.EndpointCreating
	creating.Session = &store.SessionInfo{Preset: "default"}
	creating.CreatedBy = strings.TrimSuffix(ctl.Name(), ".rec")
	derived := in
	derived.Key = ctl.Key
	derived.Key.Recipient = ctl.Intent.Endpoint
	derived.Dedup = delivery.NewDedupRecord(derived.Key.Dedup(), delivery.BodyDigest(ref.Digest), reportBy)
	derived.Derived = &store.DerivedInfo{From: ctl.Key.Recipient, AuthRequest: []byte(`{"kind":"session.create"}`)}
	return transactionBenchmarkStoreRecords{body: body, out: out, in: in, ctl: ctl, endpoint: ep, creatingEndpoint: creating, derived: derived}
}

func transactionBenchmarkStoreEndpoint(b *testing.B, value byte) ident.EndpointID {
	b.Helper()
	id, err := ident.EndpointIDFromBytes(bytes.Repeat([]byte{value}, ident.EndpointIDLen))
	if err != nil {
		b.Fatal(err)
	}
	return id
}
