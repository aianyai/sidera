package engine_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/reply"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A delivery queued for a local endpoint, as the intake of a message from a node
// elsewhere leaves it: accepted, waiting for the endpoint to read it. It is
// written through the store's own method, so the disk holds what the store
// holds for a real one.

func msgID(i int) delivery.MessageID { return delivery.MessageID(fmt.Sprintf("msg-%04d", i)) }

func bodyOf(i int) []byte { return []byte(fmt.Sprintf("body of message %d", i)) }

func digestOf(i int) delivery.BodyDigest {
	var d delivery.BodyDigest
	for j := range d {
		d[j] = byte(i + j)
	}
	return d
}

// queuedIn is message i, from node 2's endpoint 3, queued for recipient.
func queuedIn(t testing.TB, i int, recipient ident.EndpointID) store.InRecord {
	t.Helper()
	k := store.TargetKey{SenderNode: mustNode(t, 2), Sender: mustEndpoint(t, 3), Message: msgID(i), Recipient: recipient}
	accept := t0.Add(time.Duration(i) * time.Second)
	reportBy := delivery.ReportBy(accept)
	rec, err := delivery.NewTargetRecord(t0, accept, reportBy, delivery.DefaultEndpointMaxQueue)
	if err != nil {
		t.Fatal(err)
	}
	body := store.BodyRefOf(bodyOf(i))
	return store.InRecord{
		Key: k, Record: rec,
		Dedup:         delivery.NewDedupRecord(k.Dedup(), digestOf(i), reportBy),
		Auth:          delivery.Authorization{Kind: delivery.AuthorizationAllow},
		ReplyDecision: reply.Decision{Reason: reply.ReasonDisabled},
		Handout:       store.HandoutFields{ContentType: "text/plain", SentAt: t0},
		AcceptedAt:    t0,
		Body:          &body,
	}
}

// queue writes n deliveries, numbered from first, for recipient.
func (r *rig) queue(first, n int, recipient ident.EndpointID) {
	r.t.Helper()
	for i := first; i < first+n; i++ {
		if _, err := r.st.Deliver(bodyOf(i), []store.InRecord{queuedIn(r.t, i, recipient)}); err != nil {
			r.t.Fatalf("Deliver %d: %v", i, err)
		}
	}
}

// scan reads the disk.
func (r *rig) scan() *store.Scan {
	r.t.Helper()
	sc, err := r.st.Scan()
	if err != nil {
		r.t.Fatalf("Scan: %v", err)
	}
	return sc
}

// commits is how many transactions of the declaration reached their commit.
func (r *rig) commits(tx string) int {
	for _, c := range r.w.Coverage() {
		if c.Key.Tag.Tx == tx && c.Key.Step == fsx.StepCommit {
			return c.Hits
		}
	}
	return -1
}

// sessionCtlRecord is a control record of a session creation whose session was
// created: the record a crash between the control record and the endpoint's
// opening leaves.
func sessionCtlRecord(t testing.TB, i int, session ident.EndpointID) store.CtlRecord {
	t.Helper()
	k := store.TargetKey{SenderNode: mustNode(t, 2), Sender: mustEndpoint(t, 3), Message: msgID(i), Recipient: mustEndpoint(t, 4)}
	accept := t0.Add(time.Duration(i) * time.Second)
	reportBy := delivery.ReportBy(accept)
	return store.CtlRecord{
		Key: k, Record: delivery.NewTargetControlRecord(accept, accept, reportBy),
		Dedup: delivery.NewDedupRecord(k.Dedup(), digestOf(i), reportBy),
		Kind:  envelope.KindSessionCreate, Preset: "default", SessionEndpoint: session,
		AcceptedAt: t0, SentAt: t0,
		Intent: &store.CreateIntent{
			Attempt: adapterproto.AttemptID("0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6"), Endpoint: session,
			State: store.IntentCreated, NativeRef: "native-1",
		},
	}
}
