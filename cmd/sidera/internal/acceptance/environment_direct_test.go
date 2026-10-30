package acceptance

import (
	"errors"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"testing"
	"time"
)

func TestDirectProtocolLifecycleAcrossGeneration(t *testing.T) {
	f, ctx := fixture(t)
	if err := f.prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(ctx, GenerationSpec{Number: 1}); err != nil {
		t.Fatal(err)
	}
	e := newEnvironment(ctx, f)
	sender, err := e.Direct("sender")
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := e.Direct("receiver")
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.Direct("receiver")
	if err != nil || again != receiver {
		t.Fatalf("stable registration: %q %v", again, err)
	}
	if _, err := e.Agent("sender").Me(ctx); !errors.Is(err, fakeagent.ErrNotAvailable) {
		t.Fatalf("direct Me fabricated a session: %v", err)
	}
	sendAndRead := func(body string) {
		t.Helper()
		sent, err := e.Agent("sender").Send(ctx, fakeagent.SendInput{To: []string{receiver}, Body: body})
		if err != nil || sent.Replay != fakeagent.ReplayFresh {
			t.Fatalf("unkeyed send: %+v %v", sent, err)
		}
		if err := e.Quiesce(); err != nil {
			t.Fatal(err)
		}
		page, err := e.Agent("receiver").Recv(ctx, fakeagent.RecvInput{Frontend: adapterproto.FrontendCLI})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Page.Messages) != 1 || page.Page.Messages[0].ID != sent.MessageID || page.Page.Messages[0].Body != body || string(page.Page.Messages[0].From) != sender {
			t.Fatalf("actual received page: %+v", page.Page)
		}
		ack, err := e.Agent("receiver").Ack(ctx, fakeagent.AckInput{Lease: page.Page.Lease})
		if err != nil || len(ack.Items) != 1 {
			t.Fatalf("ack: %+v %v", ack, err)
		}
		if err := e.Quiesce(); err != nil {
			t.Fatal(err)
		}
		e.Advance(time.Minute)
		if err := e.Quiesce(); err != nil {
			t.Fatal(err)
		}
		status, err := e.Agent("sender").Status(ctx, fakeagent.StatusInput{MessageID: sent.MessageID})
		if err != nil || len(status.Recipients) != 1 || status.Recipients[0].State != adapterproto.DeliveryDelivered {
			t.Fatalf("status: %+v %v", status, err)
		}
	}
	sendAndRead("Read through the actual adapter protocol.")
	if err := f.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(ctx, GenerationSpec{Number: 2}); err != nil {
		t.Fatal(err)
	}
	if err := e.resetGeneration(ctx); err != nil {
		t.Fatal(err)
	}
	sendAndRead("The same agent must use the replacement adapter connection.")
}
