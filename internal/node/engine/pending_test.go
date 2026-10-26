package engine_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

// A direct endpoint is told, with endpoint.pending, that a message is ready to be handed
// out to it. The notification is best effort and says there is something to read: an
// agent that misses it reads on its own, so these tests judge only what reached the
// adapter, from the ledger the fake adapter wrote. The notification travels on a
// connection that Quiesce does not watch, so each test waits for the entry it expects
// and reads the ones before it: the notifications of a connection arrive in order, so a
// notice that should not have been sent shows as an entry ahead of the one waited for.

// pendingNotices are the endpoint.pending notifications that reached the adapter, in
// the order they arrived.
func pendingNotices(t testing.TB, h *harness.H) []ledger.Entry {
	t.Helper()
	log, err := h.Ledger()
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	return log.Kind(ledger.KindEndpointPendingReceived)
}

// waitForNotices returns the notifications once n have reached the adapter.
func waitForNotices(t testing.TB, h *harness.H, n int) []ledger.Entry {
	t.Helper()
	var got []ledger.Entry
	waitUntil(t, func() bool { got = pendingNotices(t, h); return len(got) >= n })
	return got
}

// wantNotice fails the test unless the entry tells the endpoint of name that count
// messages wait.
func wantNotice(t testing.TB, h *harness.H, e ledger.Entry, name string, count int) {
	t.Helper()
	if want := addrOf(t, h, name).Endpoint.String(); e.Attrs.Endpoint != want || e.Attrs.Count != count {
		t.Errorf("the adapter was told that %d messages wait for %s, want %d for %s (%s)", e.Attrs.Count, e.Attrs.Endpoint, count, name, want)
	}
}

// A message that becomes ready for a direct endpoint is announced once, with the number
// of messages that wait for it.
func TestADirectEndpointIsToldOnceThatAMessageWaits(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a", "b")
	send(t, h, "a", "b", "first")
	got := waitForNotices(t, h, 1)
	wantNotice(t, h, got[0], "b", 1)

	send(t, h, "a", "b", "second")
	got = waitForNotices(t, h, 2)
	if len(got) != 2 {
		t.Fatalf("the adapter received %d notices after two messages, want 2", len(got))
	}
	wantNotice(t, h, got[1], "b", 2)
}

// A message that a waiting recv takes is not announced: the agent has it already, and
// what is announced is what waits.
func TestAMessageHandedToAWaitingRecvIsNotAnnounced(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a", "b")
	got := make(chan fakeagent.RecvResult, 1)
	go func() {
		res, err := h.Agent("b").Recv(h.Ctx(), withCLI(fakeagent.RecvInput{WaitS: 30}))
		if err != nil {
			t.Error(err)
		}
		got <- res
	}()
	waitUntil(t, func() bool { return engine.Waiting(h.Engine()) == 1 })
	msg := send(t, h, "a", "b", "taken by the waiting recv")
	wantOnly(t, <-got, msg)

	// Two messages that wait: the notices are one for each, and the second counts both.
	send(t, h, "a", "b", "left to wait")
	send(t, h, "a", "b", "left to wait too")
	notices := waitForNotices(t, h, 2)
	if len(notices) != 2 {
		t.Fatalf("the adapter received %d notices, want one for each message that waited", len(notices))
	}
	wantNotice(t, h, notices[0], "b", 1)
	wantNotice(t, h, notices[1], "b", 2)
}

// A session endpoint is woken by a prompt, and endpoint.pending is not a method of a
// session endpoint.
func TestASessionEndpointIsNotToldWithEndpointPending(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a", "b")
	_, addr := idleSession(t, h, "a")
	sendToAddr(t, h, "a", addr, "for the session")
	quiet(t, h)
	send(t, h, "a", "b", "for the direct endpoint")
	notices := waitForNotices(t, h, 1)
	if len(notices) != 1 {
		t.Fatalf("the adapter received %d notices, want only the one for the direct endpoint", len(notices))
	}
	wantNotice(t, h, notices[0], "b", 1)
}
