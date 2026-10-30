package acceptance

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

type directSendController struct {
	fakeadapter.Controller
	requests []adapterproto.SendParams
	failure  error
}

func (c *directSendController) Send(_ context.Context, p adapterproto.SendParams) (adapterproto.SendResult, error) {
	c.requests = append(c.requests, p)
	if c.failure != nil {
		return adapterproto.SendResult{}, c.failure
	}
	return adapterproto.SendResult{MessageID: "00000000000000000000000000000001"}, nil
}

func (*directSendController) MessageStatus(context.Context, adapterproto.MessageStatusParams) (adapterproto.MessageStatusResult, error) {
	panic("a confirmed direct send must not depend on a subsequent status query")
}

func directSendAddress(t *testing.T, value byte) adapterproto.Address {
	t.Helper()
	node, err := ident.NodeIDFromBytes(bytes.Repeat([]byte{1}, ident.NodeIDLen))
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := ident.EndpointIDFromBytes(bytes.Repeat([]byte{value}, ident.EndpointIDLen))
	if err != nil {
		t.Fatal(err)
	}
	return adapterproto.Address((ident.Address{Node: node, Endpoint: endpoint}).String())
}

func TestDirectSendReportsOnlyAvailableReplayEvidence(t *testing.T) {
	first, second := directSendAddress(t, 2), directSendAddress(t, 3)
	for _, tc := range []struct {
		name string
		p    adapterproto.SendParams
		want fakeagent.ReplayState
	}{
		{"fresh-multiple", adapterproto.SendParams{To: []adapterproto.Address{second, first}, Body: "A real accepted send keeps recipient order."}, fakeagent.ReplayFresh},
		{"keyed-multiple", adapterproto.SendParams{To: []adapterproto.Address{second, first}, Body: "A keyed send has no public replay flag.", IdempotencyKey: "message-key"}, fakeagent.ReplayUnknown},
		{"reply", adapterproto.SendParams{To: []adapterproto.Address{first}, Body: "Reply to the actual sender.", ReplyTo: "00000000000000000000000000000002"}, fakeagent.ReplayFresh},
		{"keyed-control", adapterproto.SendParams{To: []adapterproto.Address{first}, IdempotencyKey: "retained-control-key", Control: &adapterproto.Control{Signal: adapterproto.SignalSessionClose}}, fakeagent.ReplayUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctl := &directSendController{}
			p := tc.p
			p.Endpoint = "sender"
			wantTo := make([]string, len(p.To))
			for i, to := range p.To {
				wantTo[i] = string(to)
			}
			calls := 1
			if p.IdempotencyKey != "" {
				calls = 2
			}
			wantRequests := make([]adapterproto.SendParams, calls)
			for i := range calls {
				wantRequests[i] = p
				got, err := sendDirect(context.Background(), ctl, p)
				if err != nil || got.MessageID != "00000000000000000000000000000001" || got.Replay != tc.want || !reflect.DeepEqual(got.To, wantTo) {
					t.Fatalf("send result: %+v, %v", got, err)
				}
			}
			if !reflect.DeepEqual(ctl.requests, wantRequests) {
				t.Fatalf("requests did not reach the controller unchanged: %+v", ctl.requests)
			}
		})
	}
}

func TestDirectSendFailureSuppliesNoAcceptedResult(t *testing.T) {
	failure := errors.New("send was not confirmed")
	ctl := &directSendController{failure: failure}
	got, err := sendDirect(context.Background(), ctl, adapterproto.SendParams{Endpoint: "sender", To: []adapterproto.Address{directSendAddress(t, 2)}, Body: "Unconfirmed send."})
	if !errors.Is(err, failure) || got.MessageID != "" || got.To != nil || got.Replay != fakeagent.ReplayUnknown {
		t.Fatalf("unconfirmed send has an accepted result: %+v, %v", got, err)
	}
}
