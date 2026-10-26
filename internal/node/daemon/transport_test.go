package daemon_test

import (
	"context"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A started daemon has a transport, and it is the loopback with no fault armed: a
// message from one endpoint of the node reaches another, without the test moving
// the clock, and the sender is told the target accepted it. Without a transport
// every send fails inside the engine; with a knob armed (a delay, a dropped
// receipt, an unreachable node) the message would wait or the sender would be told
// nothing.
func TestAStartedDaemonCarriesAMessageOverTheLoopbackWithNoFaultArmed(t *testing.T) {
	noLeaks(t)
	c, clk := initialized(t)
	installChat(t, openHome(t, c), installedChat())
	a := newAdapters(t, clk, installedChat(), nil)
	c.AdapterHost = a.host
	d := started(t, c)
	a.probed(t, d)

	ctx := context.Background()
	e := d.Engine()
	var refs [2]adapterproto.EndpointRef
	for i, key := range []string{"a", "b"} {
		r, err := e.RegisterDirect(ctx, chat, adapterproto.RegisterParams{Key: key, DisplayName: key})
		if err != nil {
			t.Fatalf("RegisterDirect(%s): %v", key, err)
		}
		refs[i] = r.Endpoint
	}
	st, err := e.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	to, err := ident.ParseAddress(st.Node.String() + "/" + string(refs[1]))
	if err != nil {
		t.Fatal(err)
	}

	sender := engine.ByAdapter(chat, refs[0])
	sent, err := e.Send(ctx, sender, engine.SendRequest{To: []ident.Address{to}, Body: []byte("hello"), ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("Send: %v; want the message accepted for sending (a node without a transport fails here)", err)
	}
	var last adapterproto.DeliveryState
	if err := waitFor(func() bool {
		res, err := e.MessageStatus(ctx, sender, adapterproto.MessageID(sent.Message))
		if err != nil || len(res.Recipients) != 1 {
			return false
		}
		last = res.Recipients[0].State
		return last != adapterproto.DeliveryQueued
	}); err != nil {
		t.Fatalf("the delivery is still %q although nothing delayed it; want it to have reached the target through the loopback", last)
	}
	if last != adapterproto.DeliveryAccepted && last != adapterproto.DeliveryDelivered {
		t.Errorf("the delivery is %q, want accepted or delivered", last)
	}
}
