package agentapi_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func endpointClient(t testing.TB, h *harness.H, token adapterproto.Token) *agentapi.Client {
	t.Helper()
	home := socketHome(t)
	addr, err := ipc.Locate(home, ipc.KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	l, err := agentapi.Listen(addr, held{}, ipc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := agentapi.New(agentapi.Config{Service: h.Engine()})
	if err != nil {
		t.Fatal(err)
	}
	s.EnableMethods()
	ended := make(chan error, 1)
	go func() { ended <- s.Serve(l) }()
	t.Cleanup(func() {
		s.Close()
		if err := <-ended; err != nil {
			t.Error(err)
		}
	})
	c, err := agentapi.NewClient(agentapi.ClientConfig{Home: home, Token: token, ProbeToken: adapterproto.Token("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestClientUsesTheEndpointCredentialForMethods(t *testing.T) {
	h, token, address := sessionNode(t)
	c := endpointClient(t, h, token)
	me, err := c.Me(h.Ctx())
	if err != nil || me.Address != address || me.Frontends[adapterproto.FrontendCLI].RecvWaitMaxS != 20 {
		t.Fatalf("Me: %+v, %v", me, err)
	}
	paused := true
	d, err := c.Declare(h.Ctx(), agentapi.DeclarationPatch{Paused: &paused})
	if err != nil || !d.Declaration.Paused {
		t.Fatalf("Declare: %+v, %v", d, err)
	}
	peers, err := c.Peers(h.Ctx())
	if err != nil || len(peers.Nodes) != 1 || !peers.Nodes[0].Self {
		t.Fatalf("Peers: %+v, %v", peers, err)
	}
	page, err := c.Directory(h.Ctx(), agentapi.DirectoryRequest{})
	if err != nil || len(page.Endpoints) < 2 {
		t.Fatalf("Directory: %+v, %v", page, err)
	}
}

func TestClientSendsAMessageWithoutAutomaticReplay(t *testing.T) {
	h, token, _ := sessionNode(t)
	c := endpointClient(t, h, token)
	in := agentapi.SendRequest{To: []string{h.Addr("creator")}, Body: "sent through the real socket", IdempotencyKey: "client-send"}
	sent, err := c.Send(h.Ctx(), in)
	if err != nil || sent.MessageID == "" || sent.Replayed || len(sent.To) != 1 {
		t.Fatalf("Send: %+v, %v", sent, err)
	}
	replay, err := c.Send(h.Ctx(), in)
	if err != nil || replay.MessageID != sent.MessageID || !replay.Replayed {
		t.Fatalf("replay: %+v, %v", replay, err)
	}
	status, err := c.Status(h.Ctx(), sent.MessageID)
	if err != nil || len(status.Recipients) != 1 || string(status.Recipients[0].To) != h.Addr("creator") {
		t.Fatalf("Status: %+v, %v", status, err)
	}
}

func TestClientRejectsInvalidTextWithoutAcceptingARewrittenMessage(t *testing.T) {
	for _, field := range []string{"body", "idempotency key"} {
		t.Run(field, func(t *testing.T) {
			h, token, _ := sessionNode(t)
			c := endpointClient(t, h, token)
			in := agentapi.SendRequest{To: []string{h.Addr("creator")}, Body: "message", IdempotencyKey: "client-invalid-text"}
			if field == "body" {
				in.Body = "message\xff"
			} else {
				in.IdempotencyKey += "\xff"
			}
			got, err := c.Send(h.Ctx(), in)
			if e := apiwire.FromError(err); e == nil || e.Category != apiwire.CategoryInvalidArgument || e.Code != "malformed_request" {
				t.Errorf("invalid text: %+v, %v; want invalid_argument/malformed_request", got, err)
			}
			in.Body = strings.ToValidUTF8(in.Body, "\ufffd")
			in.IdempotencyKey = strings.ToValidUTF8(in.IdempotencyKey, "\ufffd")
			got, err = c.Send(h.Ctx(), in)
			if err != nil || got.MessageID == "" || got.Replayed {
				t.Fatalf("valid replacement: %+v, %v; the rejected request must not consume its key", got, err)
			}
		})
	}
}

func TestClientReceivesThePageBytesIncludingTheNewline(t *testing.T) {
	h, token, address := sessionNode(t)
	c := endpointClient(t, h, token)
	sent, err := h.Agent("creator").Send(h.Ctx(), fakeagent.SendInput{To: []string{address}, Body: "the exact page"})
	if err != nil {
		t.Fatal(err)
	}
	h.Quiesce()
	raw, err := c.Recv(h.Ctx(), agentapi.RecvRequest{Frontend: adapterproto.FrontendMCP})
	if err != nil {
		t.Fatal(err)
	}
	var page adapterproto.RecvPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != sent.MessageID || !strings.HasSuffix(string(raw), "\n") || !strings.HasPrefix(string(raw), `{"lease":`) {
		t.Fatalf("Recv: %s", raw)
	}
	acked, err := c.Ack(h.Ctx(), agentapi.AckRequest{Lease: page.Lease})
	if err != nil || len(acked.Items) != 1 || acked.Items[0].Ack != adapterproto.ItemCompleted {
		t.Fatalf("Ack: %+v, %v", acked, err)
	}
	reply, err := c.Reply(h.Ctx(), agentapi.ReplyRequest{MessageID: sent.MessageID, Body: "the client answers"})
	if err != nil || len(reply.To) != 1 || reply.To[0] != h.Addr("creator") {
		t.Fatalf("Reply: %+v, %v", reply, err)
	}
}

func TestClientControlReturnsAcceptanceAndStatusCarriesTheReply(t *testing.T) {
	h, token, _ := sessionNode(t)
	c := endpointClient(t, h, token)
	sent, err := c.Control(h.Ctx(), agentapi.ControlRequest{To: h.RuntimeAddr(), Signal: adapterproto.SignalSessionCreate, Preset: "default"})
	if err != nil || sent.MessageID == "" || sent.To != h.RuntimeAddr() || sent.Replayed {
		t.Fatalf("Control: %+v, %v", sent, err)
	}
	for range 4 {
		h.Quiesce()
		h.Advance(engine.DefaultParams().PullInterval)
	}
	h.Quiesce()
	status, err := c.Status(h.Ctx(), sent.MessageID)
	if err != nil || status.ControlReply == nil || status.ControlReply.Create == nil || status.ControlReply.Create.Outcome != adapterproto.CreateReplyCreated {
		t.Fatalf("Status: %+v, %v", status, err)
	}
}

func TestRecvWithMalformedEarlierLeaseStillHandsOutThePage(t *testing.T) {
	h, token, address := sessionNode(t)
	c := endpointClient(t, h, token)
	sent, err := h.Agent("creator").Send(h.Ctx(), fakeagent.SendInput{
		To: []string{address}, Body: "read despite the failed earlier acknowledgement",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.Quiesce()
	raw, err := c.Recv(h.Ctx(), agentapi.RecvRequest{
		Frontend: adapterproto.FrontendCLI, AckLease: "not-a-lease",
	})
	if err != nil {
		t.Fatalf("a malformed earlier lease blocked recv: %v", err)
	}
	var page adapterproto.RecvPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != sent.MessageID {
		t.Fatalf("recv: %s; want the queued message after the failed acknowledgement", raw)
	}
}
