package engine_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func statusOf(t testing.TB, h *harness.H, from string, msg adapterproto.MessageID) adapterproto.MessageStatusResult {
	t.Helper()
	res, err := h.Agent(from).Status(h.Ctx(), fakeagent.StatusInput{MessageID: msg})
	if err != nil {
		t.Fatalf("status of %s for %s: %v", msg, from, err)
	}
	if err := res.Validate(); err != nil {
		t.Fatalf("the status of %s is not a valid result: %v", msg, err)
	}
	return res
}

func TestTheStatusOfAMessageIsTheStateOfEachDeliveryAsTheSenderHoldsIt(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b", "c")
	if err := h.Agent("c").Declare(h.Ctx(), fakeagent.DeclareInput{Paused: boolp(true)}); err != nil {
		t.Fatal(err)
	}

	// Nothing has come back yet.
	h.Unreachable(true)
	res, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("b"), h.Addr("c")}, Body: "to two"})
	if err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	got := statusOf(t, h, "a", res.MessageID)
	want := []adapterproto.RecipientStatus{
		{To: adapterproto.Address(h.Addr("b")), State: adapterproto.DeliveryQueued},
		{To: adapterproto.Address(h.Addr("c")), State: adapterproto.DeliveryQueued},
	}
	if !sameRecipients(got.Recipients, want) {
		t.Errorf("before any receipt: %+v, want %+v", got.Recipients, want)
	}
	if got.ControlReply != nil {
		t.Errorf("a message that carries no control signal has a control reply: %+v", got.ControlReply)
	}

	// The receipt comes: b took the message, c refused it.
	h.Unreachable(false)
	h.Advance(resendBackoff)
	quiet(t, h)
	got = statusOf(t, h, "a", res.MessageID)
	want = []adapterproto.RecipientStatus{
		{To: adapterproto.Address(h.Addr("b")), State: adapterproto.DeliveryAccepted},
		{To: adapterproto.Address(h.Addr("c")), State: adapterproto.DeliveryRejected, Reason: adapterproto.RejectEndpointDeclined},
	}
	if !sameRecipients(got.Recipients, want) {
		t.Errorf("after the receipt: %+v, want %+v", got.Recipients, want)
	}
}

func TestOnlyTheSenderOfAMessageIsToldItsStatus(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	msg := send(t, h, "a", "b", "q")
	quiet(t, h)
	for _, c := range []struct{ name, from, id string }{
		{"an identifier nobody sent", "a", "no-such-message"},
		{"the recipient of the message", "b", string(msg)},
	} {
		_, err := h.Agent(c.from).Status(h.Ctx(), fakeagent.StatusInput{MessageID: adapterproto.MessageID(c.id)})
		if fakeagent.CategoryOf(err) != fakeagent.CategoryNotFound || fakeagent.CodeOf(err) != "message_unknown" {
			t.Errorf("%s: %v, want not_found/message_unknown", c.name, err)
		}
	}
}

func TestTheStatusOfAMessageSurvivesARestart(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	msg := send(t, h, "a", "b", "q")
	quiet(t, h)
	h.Restart()
	h.MustDirect("a")
	h.MustDirect("b")
	quiet(t, h)
	got := statusOf(t, h, "a", msg)
	if len(got.Recipients) != 1 || got.Recipients[0].State != adapterproto.DeliveryAccepted {
		t.Errorf("after a restart: %+v, want one accepted delivery", got.Recipients)
	}
}

func sameRecipients(a, b []adapterproto.RecipientStatus) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
