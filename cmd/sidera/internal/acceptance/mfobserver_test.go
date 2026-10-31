//go:build diagnostic

package acceptance

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

func TestMFObserverDisabledDoesNotInspect(t *testing.T) {
	t.Setenv("SIDERA_CI_DIAGNOSTIC_MF_OBSERVER", "")
	point, err := fsx.ParsePoint("source-update/out.write#4")
	if err != nil {
		t.Fatal("fixed observer target could not be parsed")
	}
	// A nil environment fails immediately if the disabled observer inspects it.
	mfObserveCheck(crashwalk.RunPlan{Targets: []crashwalk.Target{{Gen: 1, Point: point}}}, "management-instance", nil)
}

func TestMFObserverProjectionAllowlist(t *testing.T) {
	const secret = "synthetic-mf-private-canary"
	node, _ := ident.NodeIDFromBytes(bytes.Repeat([]byte{1}, ident.NodeIDLen))
	sender, _ := ident.EndpointIDFromBytes(bytes.Repeat([]byte{2}, ident.EndpointIDLen))
	runtime, _ := ident.EndpointIDFromBytes(bytes.Repeat([]byte{3}, ident.EndpointIDLen))
	address := ident.Address{Node: node, Endpoint: runtime}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	item := delivery.Item{Ref: delivery.DeliveryRef{Sender: ident.Address{Node: node, Endpoint: sender}, Message: "control-id", Recipient: address}, Feedback: delivery.Feedback{Kind: delivery.KindDeliveryUnknown}, At: now, Source: secret}
	out := store.OutRecord{
		Key:      store.OutKey{Sender: sender, Message: "control-id", Recipients: []ident.Address{address}, Control: true},
		Outbound: envelope.Outbound{Recipients: []envelope.Tracked{{Address: address, Delivery: delivery.Record{State: delivery.StateDeliveryUnknown, StateAt: now}}}},
		Envelope: store.OutEnvelope{Thread: secret, Preset: secret, ContentType: secret},
		Body:     &store.BodyRef{Len: 13}, ControlReply: []byte(secret),
		Idempotency: &store.Idempotency{Key: secret}, Feedback: []delivery.Item{item},
	}
	cleared := item
	cleared.Feedback.Kind = delivery.KindExpiredUnconfirmed
	scan := &store.Scan{
		Out:       []store.OutRecord{out},
		In:        []store.InRecord{{Key: store.TargetKey{SenderNode: node, Sender: ident.SystemEndpoint(), Message: "notice-id", Recipient: sender}, Record: delivery.Record{State: delivery.StateDelivered, StateAt: now}, Handout: store.HandoutFields{Thread: secret, ContentType: secret}, Covers: []store.CoverRef{store.CoverOf(out.Name(), item), store.CoverOf(out.Name(), cleared)}}},
		Endpoints: []store.EndpointRecord{{ID: runtime, Status: store.EndpointClosed, Token: []byte(secret), Description: secret, DisplayName: secret, Declaration: []byte(`{"env":"` + secret + `"}`), Session: &store.SessionInfo{NativeRef: secret, Preset: secret, ToolAccess: []string{secret}}}},
		Unknown:   []string{secret}, Bodies: []string{secret}, Archive: []string{secret},
	}
	entries := []ledger.Entry{{Writer: "agent/" + sender.String(), Step: "queue-instance-records", Kind: ledger.KindClientResult, Attrs: ledger.Attrs{Action: "control", Category: "ok", MessageID: "control-id", Detail: secret, CWD: secret, LockPath: secret, IdemKey: secret}}}
	o := mfProject(scan, entries, sender, address, now)
	raw, err := json.Marshal(o)
	if err != nil {
		t.Fatal("projection could not be encoded")
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("private data crossed management feedback projection")
	}
	if o.ControlMatches != 1 || o.SourceMatches != 1 || len(o.Feedback) != 1 || len(o.Notices) != 2 || o.Notices[0].PendingMatches != 1 || o.Notices[1].PendingMatches != 0 {
		t.Fatal("projection lost pending or already cleared coverage evidence")
	}
}
