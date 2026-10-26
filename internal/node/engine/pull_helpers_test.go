package engine_test

import (
	"path"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The helpers of the tests of pulling. What an agent was handed is what its recv
// printed, and what became of a delivery is read from the records on the disk and
// from the notices the sender holds.

// recvAs asks for a page as the agent of the endpoint and returns what it printed.
func recvAs(t testing.TB, h *harness.H, a fakeagent.Agent, in fakeagent.RecvInput) fakeagent.RecvResult {
	t.Helper()
	if in.Frontend == "" {
		in.Frontend = adapterproto.FrontendCLI
	}
	res, err := a.Recv(h.Ctx(), in)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	return res
}

// ackAs acknowledges the items of the page the lease names, or all of it when there
// are none, and returns the outcome of each part.
func ackAs(t testing.TB, h *harness.H, a fakeagent.Agent, lease adapterproto.LeaseRef, items ...adapterproto.PartRef) []adapterproto.PartAck {
	t.Helper()
	res, err := a.Ack(h.Ctx(), fakeagent.AckInput{Lease: lease, Items: items})
	if err != nil {
		t.Fatalf("ack of %s: %v", lease, err)
	}
	return res.Items
}

// pageIDs are the identifiers of the messages on a page, in the order printed.
func pageIDs(res fakeagent.RecvResult) []adapterproto.MessageID {
	var out []adapterproto.MessageID
	for _, m := range res.Page.Messages {
		out = append(out, m.ID)
	}
	return out
}

// wantOnly fails the test unless the page holds exactly the message, whole.
func wantOnly(t testing.TB, res fakeagent.RecvResult, msg adapterproto.MessageID) adapterproto.PageMessage {
	t.Helper()
	if len(res.Page.Messages) != 1 || res.Page.Messages[0].ID != msg || res.Page.Messages[0].Part != nil {
		t.Fatalf("the page holds %v, want only the message %s, whole", pageIDs(res), msg)
	}
	return res.Page.Messages[0]
}

// wantEmpty fails the test when the page holds a message.
func wantEmpty(t testing.TB, res fakeagent.RecvResult) {
	t.Helper()
	if len(res.Page.Messages) != 0 {
		t.Fatalf("the page holds %v, want none", pageIDs(res))
	}
}

// newSession asks the runtime endpoint for a session as the agent of from and
// returns the agent of the session and its address. A session endpoint comes only
// from a creation.
func newSession(t testing.TB, h *harness.H, from string) (fakeagent.Agent, string) {
	t.Helper()
	return newSessionOn(t, h, harness.Instance, from)
}

// newSessionOn is newSession on another instance of the fixture: the session is made by
// that instance's adapter and runtime.
func newSessionOn(t testing.TB, h *harness.H, instance, from string) (fakeagent.Agent, string) {
	t.Helper()
	msg := createOn(t, h, instance, from, "default")
	quiet(t, h)
	h.Advance(pullInterval) // the reply reaches its sender by the sender's pull
	quiet(t, h)
	r := controlReply(t, h, from, msg)
	if r == nil || r.Create == nil || r.Create.Outcome != adapterproto.CreateReplyCreated || r.Create.SessionEndpoint == "" {
		t.Fatalf("the creation reply is %+v, want a session", r)
	}
	id, err := ident.ParseEndpointID(string(r.Create.SessionEndpoint))
	if err != nil {
		t.Fatal(err)
	}
	return h.Session(string(r.Create.SessionEndpoint)), ident.Address{Node: harness.NodeID(), Endpoint: id}.String()
}

// sendToAddr sends a text from the endpoint registered as from to an address.
func sendToAddr(t testing.TB, h *harness.H, from, to, body string) adapterproto.MessageID {
	t.Helper()
	res, err := h.Agent(from).Send(h.Ctx(), fakeagent.SendInput{To: []string{to}, Body: body})
	if err != nil {
		t.Fatalf("send %s -> %s: %v", from, to, err)
	}
	return res.MessageID
}

// removeBody removes the body file of the record from the disk of the node, which
// must be down.
func removeBody(t testing.TB, h *harness.H, owner store.Record) {
	t.Helper()
	p := path.Join(store.BodiesDir, store.BodyFileName(owner.Name()))
	if err := h.Disk().Remove(fsx.Tag{Tx: "offline", Role: "f"}, p); err != nil {
		t.Fatalf("removing %s: %v", p, err)
	}
}

// bigText is a text of n bytes of valid UTF-8, English and Chinese mixed, so that a
// cut of it can fall inside a character, whose first line says which message it is.
func bigText(label string, n int) string {
	var b strings.Builder
	b.WriteString(label + "\n")
	for i := 0; b.Len() < n; i++ {
		if i%7 == 3 {
			b.WriteString("你好，世界。")
		} else {
			b.WriteString("the quick brown fox jumps over the lazy dog. ")
		}
	}
	s := b.String()
	for len(s) > n || !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
