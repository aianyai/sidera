package console

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

const (
	ownerEndpoint = "ep-owner"
	nodeName      = "node-1"
	lease         = "11111111111111111111111111111111"
)

// daemon is a stand-in for the daemon's side of the adapter protocol: it answers
// the endpoint.* calls of the owner endpoint from canned results and records what
// it was asked.
type daemon struct {
	mu       sync.Mutex
	register []adapterproto.RegisterParams
	sends    []adapterproto.SendParams
	statuses []adapterproto.MessageStatusParams
	recvs    []adapterproto.RecvParams
	acks     []adapterproto.AckParams

	// registerGate, when set, holds every endpoint.register until it is closed,
	// as the daemon does until it is running.
	registerGate chan struct{}

	pages        []adapterproto.RecvPage // handed out one per recv; then an empty page
	statusAnswer func(n int) adapterproto.MessageStatusResult
}

func decode[T any](raw json.RawMessage) T {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		panic(err)
	}
	return v
}

// harness runs the adapter role over pipes against the stub daemon, with a client
// that reaches its control channel.
type harness struct {
	t      *testing.T
	d      *daemon
	peer   *peer.Peer
	cancel context.CancelFunc
	done   chan error
	client *client
	out    *bytes.Buffer
	errOut *bytes.Buffer
}

func newHarness(t *testing.T, d *daemon) *harness {
	t.Helper()
	// A Unix socket path is short: the default temporary directory, not t.TempDir().
	dir, err := os.MkdirTemp("", "cn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	aR, dW := io.Pipe()
	dR, aW := io.Pipe()
	p, err := peer.New(dR, dW, peer.Config{})
	if err != nil {
		t.Fatal(err)
	}
	p.Handle(adapterproto.MethodEndpointRegister, func(ctx context.Context, raw json.RawMessage) (any, error) {
		if d.registerGate != nil {
			select {
			case <-d.registerGate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		d.register = append(d.register, decode[adapterproto.RegisterParams](raw))
		return adapterproto.RegisterResult{Endpoint: ownerEndpoint}, nil
	})
	p.Handle(adapterproto.MethodEndpointSend, func(_ context.Context, raw json.RawMessage) (any, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.sends = append(d.sends, decode[adapterproto.SendParams](raw))
		return adapterproto.SendResult{MessageID: adapterproto.MessageID("msg-out-" + string(rune('0'+len(d.sends))))}, nil
	})
	p.Handle(adapterproto.MethodEndpointMessageStatus, func(_ context.Context, raw json.RawMessage) (any, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.statuses = append(d.statuses, decode[adapterproto.MessageStatusParams](raw))
		return d.statusAnswer(len(d.statuses)), nil
	})
	p.Handle(adapterproto.MethodEndpointRecv, func(_ context.Context, raw json.RawMessage) (any, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.recvs = append(d.recvs, decode[adapterproto.RecvParams](raw))
		if len(d.pages) == 0 {
			return adapterproto.RecvResult{Page: adapterproto.RecvPage{Lease: lease, Messages: []adapterproto.PageMessage{}}}, nil
		}
		page := d.pages[0]
		d.pages = d.pages[1:]
		return adapterproto.RecvResult{Page: page}, nil
	})
	p.Handle(adapterproto.MethodEndpointAck, func(_ context.Context, raw json.RawMessage) (any, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		ack := decode[adapterproto.AckParams](raw)
		d.acks = append(d.acks, ack)
		var res adapterproto.AckResult
		for _, it := range ack.Items {
			res.Items = append(res.Items, adapterproto.PartAck{MessageID: it.MessageID, Part: it.Part, Ack: adapterproto.ItemRecorded})
		}
		return res, nil
	})
	p.Start()

	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, d: d, peer: p, cancel: cancel, done: make(chan error, 1), out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	go func() { h.done <- serve(ctx, aR, aW, slog.New(slog.DiscardHandler)) }()
	t.Cleanup(func() {
		_ = p.Close()
		select {
		case <-h.done:
		case <-time.After(10 * time.Second):
			t.Error("the adapter did not stop after the daemon closed the stream")
		}
		cancel()
	})

	call := func(method string, params, result any) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := p.Call(ctx, method, params, result); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
	}
	var init adapterproto.InitializeResult
	call(adapterproto.MethodInitialize, adapterproto.InitializeParams{
		Protocol: adapterproto.SupportedRange(), Daemon: adapterproto.DaemonInfo{Version: "test"},
		Instance: adapterproto.InstanceInfo{ID: AdapterID, LockPath: filepath.Join(dir, "adapter.lock"), StateDir: dir},
	}, &init)
	if init.Capabilities.Direct == nil || init.Capabilities.Direct.MaxEndpoints != 8 || init.Capabilities.Runtime != nil {
		t.Fatalf("capabilities %+v, want direct with 8 endpoints and no runtime", init.Capabilities)
	}
	call(adapterproto.MethodAdapterStart, adapterproto.StartParams{
		Node: nodeName, Config: adapterproto.ConfigValues{}, Presets: []adapterproto.PresetConfig{},
		Sessions: []adapterproto.SessionAttach{}, DirectEndpoints: []adapterproto.DirectAttach{},
	}, &adapterproto.StartResult{})

	h.client = &client{env: Env{Stdout: h.out, Stderr: h.errOut, Stdin: strings.NewReader("")}, dir: dir, dial: dialLocal}
	if d.registerGate == nil {
		h.waitRegistered()
	}
	return h
}

// waitRegistered waits until the adapter has registered the owner endpoint, which
// it does after it answered adapter.start.
func (h *harness) waitRegistered() {
	h.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if h.run("whoami") == exitOK {
			return
		}
	}
	h.t.Fatalf("the owner endpoint was not registered: stderr %q", h.errOut.String())
}

// run parses and executes a client command against the adapter.
func (h *harness) run(args ...string) int {
	h.t.Helper()
	h.out.Reset()
	h.errOut.Reset()
	env := h.client.env
	env.Getenv = func(string) string { return "" }
	inv, err := parseCommand(append(args, "--home", "/unused"), env)
	if err != nil {
		h.t.Fatalf("parsing %v: %v", args, err)
	}
	inv.pollEvery = 5 * time.Millisecond
	return h.client.execute(context.Background(), inv)
}

func TestStartRegistersTheOwnerEndpointAndWhoamiNamesIt(t *testing.T) {
	d := &daemon{}
	h := newHarness(t, d)
	if len(d.register) != 1 || d.register[0].Key != "owner" || d.register[0].DisplayName == "" {
		t.Fatalf("register calls %+v, want one with key owner", d.register)
	}
	if code := h.run("whoami"); code != exitOK || strings.TrimSpace(h.out.String()) != nodeName+"/"+ownerEndpoint {
		t.Fatalf("whoami: exit %d, output %q, stderr %q", code, h.out.String(), h.errOut.String())
	}
}

// The daemon serves endpoint.register only once it is running, which is after it
// has the answer to adapter.start: the answer must not wait for the registration.
func TestStartIsAnsweredBeforeTheOwnerEndpointIsRegistered(t *testing.T) {
	d := &daemon{registerGate: make(chan struct{})}
	h := newHarness(t, d) // fails if adapter.start waits for endpoint.register

	if code := h.run("whoami"); code != exitFailed || !strings.Contains(h.errOut.String(), "not registered yet") {
		t.Errorf("whoami while starting: exit %d, stderr %q, want a refusal saying the endpoint is not registered yet", code, h.errOut.String())
	}
	close(d.registerGate)
	h.waitRegistered()
	if got := strings.TrimSpace(h.out.String()); got != nodeName+"/"+ownerEndpoint {
		t.Errorf("whoami after registration: %q", got)
	}
}

func TestNewSendsTheControlSignalAndPollsForTheReply(t *testing.T) {
	d := &daemon{statusAnswer: func(n int) adapterproto.MessageStatusResult {
		res := adapterproto.MessageStatusResult{Recipients: []adapterproto.RecipientStatus{{To: "node-2/rt", State: adapterproto.DeliveryDelivered}}}
		if n >= 3 {
			res.ControlReply = &adapterproto.ControlReply{Signal: adapterproto.SignalSessionCreate,
				Create: &adapterproto.ControlCreateReply{Outcome: adapterproto.CreateReplyCreated, SessionEndpoint: "ep-session"}}
		}
		return res
	}}
	h := newHarness(t, d)
	code := h.run("new", "node-2/rt", "--preset", "dev", "--body", "do the thing")
	if code != exitOK {
		t.Fatalf("new: exit %d, stderr %q", code, h.errOut.String())
	}
	if got := h.out.String(); got != "message: msg-out-1\nsession: node-1/ep-session\n" {
		t.Errorf("output %q", got)
	}
	s := d.sends[0]
	if s.Endpoint != ownerEndpoint || len(s.To) != 1 || s.To[0] != "node-2/rt" || s.Body != "do the thing" || s.ContentType != "text/plain" ||
		s.Control == nil || s.Control.Signal != adapterproto.SignalSessionCreate || s.Control.Params.Preset != "dev" {
		t.Errorf("endpoint.send %+v", s)
	}
	if len(d.statuses) != 3 || d.statuses[0].MessageID != "msg-out-1" || d.statuses[0].Endpoint != ownerEndpoint {
		t.Errorf("message_status calls %+v, want three for msg-out-1", d.statuses)
	}
}

func TestNewReportsAFailedCreationAndARejectedDelivery(t *testing.T) {
	failed := adapterproto.MessageStatusResult{
		Recipients: []adapterproto.RecipientStatus{{To: "node-2/rt", State: adapterproto.DeliveryDelivered}},
		ControlReply: &adapterproto.ControlReply{Signal: adapterproto.SignalSessionCreate,
			Create: &adapterproto.ControlCreateReply{Outcome: adapterproto.CreateReplyFailed, Reason: adapterproto.CreateReasonPresetUnknown}},
	}
	rejected := adapterproto.MessageStatusResult{
		Recipients: []adapterproto.RecipientStatus{{To: "node-2/rt", State: adapterproto.DeliveryRejected, Reason: adapterproto.RejectUnauthorized}},
	}
	for name, tc := range map[string]struct {
		status adapterproto.MessageStatusResult
		want   string
	}{
		"failed creation":   {failed, "preset_unknown"},
		"rejected delivery": {rejected, "unauthorized"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, &daemon{statusAnswer: func(int) adapterproto.MessageStatusResult { return tc.status }})
			if code := h.run("new", "node-2/rt", "--preset", "dev"); code != exitFailed || !strings.Contains(h.errOut.String(), tc.want) {
				t.Errorf("exit %d, stderr %q, want exit 1 naming %s", code, h.errOut.String(), tc.want)
			}
		})
	}
}

func TestNewThatGetsNoReplyIsUnsettled(t *testing.T) {
	h := newHarness(t, &daemon{statusAnswer: func(int) adapterproto.MessageStatusResult {
		return adapterproto.MessageStatusResult{Recipients: []adapterproto.RecipientStatus{{To: "node-2/rt", State: adapterproto.DeliveryAccepted}}}
	}})
	code := h.run("new", "node-2/rt", "--preset", "dev", "--timeout", "1")
	if code != exitUnsettled || !strings.Contains(h.errOut.String(), "status 'msg-out-1'") {
		t.Errorf("exit %d, stderr %q, want exit 3 pointing at status", code, h.errOut.String())
	}
}

func TestRecvPrintsThenAcknowledgesAndFollowsPartsAndPages(t *testing.T) {
	part1 := adapterproto.PageMessage{ID: "m-2", From: "node-3/agent", Body: "first half ",
		Part: &adapterproto.PagePart{Index: 1, Total: 2, Label: "part 1/2", Next: &adapterproto.PageNext{Msg: "m-2", Part: 2}}}
	part2 := adapterproto.PageMessage{ID: "m-2", From: "node-3/agent", Body: "second half",
		Part: &adapterproto.PagePart{Index: 2, Total: 2, Label: "part 2/2"}}
	d := &daemon{pages: []adapterproto.RecvPage{
		{Lease: lease, More: 1, Messages: []adapterproto.PageMessage{{ID: "m-1", From: "node-3/agent", Body: "hello owner"}}},
		{Lease: lease, Messages: []adapterproto.PageMessage{part1}},
		{Lease: lease, Messages: []adapterproto.PageMessage{part2}},
	}}
	h := newHarness(t, d)
	if code := h.run("recv", "--wait", "5"); code != exitOK {
		t.Fatalf("recv: exit %d, stderr %q", code, h.errOut.String())
	}
	out := h.out.String()
	for _, want := range []string{"from node-3/agent  id m-1\nhello owner\n", "id m-2  part 1/2\nfirst half ", "id m-2  part 2/2\nsecond half"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q lacks %q", out, want)
		}
	}
	if len(d.recvs) != 3 || d.recvs[0].WaitS == nil || *d.recvs[0].WaitS != 5 || *d.recvs[1].WaitS != 0 ||
		d.recvs[2].MessageID != "m-2" || d.recvs[2].Part != 2 {
		t.Errorf("endpoint.recv calls %+v, want the wait, a no-wait follow-up and part 2 of m-2", d.recvs)
	}
	var acked []string
	for _, a := range d.acks {
		if a.Endpoint != ownerEndpoint || a.Lease != lease {
			t.Errorf("ack %+v", a)
		}
		for _, it := range a.Items {
			acked = append(acked, string(it.MessageID)+":"+string(rune('0'+it.Part)))
		}
	}
	if strings.Join(acked, " ") != "m-1:1 m-2:1 m-2:2" {
		t.Errorf("acknowledged %v, want m-1:1 m-2:1 m-2:2", acked)
	}
}

func TestRecvWithNothingWaitingSaysSoAndAcknowledgesNothing(t *testing.T) {
	d := &daemon{}
	h := newHarness(t, d)
	if code := h.run("recv"); code != exitOK || h.out.Len() != 0 || !strings.Contains(h.errOut.String(), "no messages") || len(d.acks) != 0 {
		t.Errorf("exit %d, stdout %q, stderr %q, acks %v", code, h.out.String(), h.errOut.String(), d.acks)
	}
}

func TestReplyGoesToTheSenderSeenInRecv(t *testing.T) {
	d := &daemon{pages: []adapterproto.RecvPage{{Lease: lease, Messages: []adapterproto.PageMessage{{ID: "m-1", From: "node-3/agent", Body: "question"}}}}}
	h := newHarness(t, d)
	if code := h.run("reply", "m-1", "--body", "answer"); code != exitFailed || !strings.Contains(h.errOut.String(), "--to") {
		t.Errorf("reply before recv: exit %d, stderr %q, want a refusal asking for --to", code, h.errOut.String())
	}
	if len(d.sends) != 0 {
		t.Fatal("a reply to an unseen message reached the daemon")
	}
	h.run("recv")
	if code := h.run("reply", "m-1", "--body", "answer"); code != exitOK {
		t.Fatalf("reply: exit %d, stderr %q", code, h.errOut.String())
	}
	s := d.sends[0]
	if s.ReplyTo != "m-1" || len(s.To) != 1 || s.To[0] != "node-3/agent" || s.Body != "answer" || s.Control != nil {
		t.Errorf("endpoint.send %+v", s)
	}
	if code := h.run("reply", "m-9", "--to", "node-4/other", "--body", "late"); code != exitOK || d.sends[1].To[0] != "node-4/other" {
		t.Errorf("reply with --to: exit %d, send %+v", code, d.sends[1])
	}
}

func TestSendAndStatusAndTheDaemonsRefusal(t *testing.T) {
	d := &daemon{statusAnswer: func(int) adapterproto.MessageStatusResult {
		return adapterproto.MessageStatusResult{Recipients: []adapterproto.RecipientStatus{{To: "node-3/agent", State: adapterproto.DeliveryRejected, Reason: adapterproto.RejectEndpointClosed}}}
	}}
	h := newHarness(t, d)
	if code := h.run("send", "--to", "node-3/agent", "--body", "hi"); code != exitOK || h.out.String() != "message: msg-out-1\n" {
		t.Errorf("send: exit %d, output %q, stderr %q", code, h.out.String(), h.errOut.String())
	}
	if code := h.run("status", "msg-out-1"); code != exitOK || h.out.String() != "to node-3/agent: rejected (endpoint_closed)\n" {
		t.Errorf("status: exit %d, output %q", code, h.out.String())
	}
	// A request the SDK refuses before it reaches the daemon comes back as a failure.
	if code := h.run("send", "--to", "node-3/agent", "--body-file", "-"); code != exitFailed || len(d.sends) != 1 {
		t.Errorf("send without a body: exit %d, sends %d", code, len(d.sends))
	}
}

func TestTheClientFindsNoAdapterWhenNoneIsRunning(t *testing.T) {
	dir, err := os.MkdirTemp("", "cn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	c := &client{env: Env{Stdout: io.Discard, Stderr: &bytes.Buffer{}}, dir: dir, dial: dialLocal}
	if _, err := c.call(context.Background(), request{Op: opWhoami}); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("call with no adapter: %v", err)
	}
}
