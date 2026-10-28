package engine_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/agentapi"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

type agentAPILock struct{}

func (agentAPILock) Held() bool { return true }

// This integration test lives with the engine fixture so its existing test-only
// waiter observation can synchronize the HTTP request without a production hook.
func TestRecvWaitClampedToFrontendDeclaration(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "sender")
	_, address := newSession(t, h, "sender")
	scan, err := h.Records()
	if err != nil {
		t.Fatal(err)
	}
	var token adapterproto.Token
	for _, ep := range scan.Endpoints {
		if strings.HasSuffix(address, "/"+ep.ID.String()) {
			token = adapterproto.Token(base64.RawURLEncoding.EncodeToString(ep.Token))
		}
	}
	if token == "" {
		t.Fatal("the created session has no token")
	}
	home, err := os.MkdirTemp("", "ar")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	addr, err := ipc.Locate(home, ipc.KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	l, err := agentapi.Listen(addr, agentAPILock{}, ipc.Options{})
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
	c, err := agentapi.NewClient(agentapi.ClientConfig{Home: home, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	me, err := c.Me(h.Ctx())
	if err != nil || me.Frontends[adapterproto.FrontendCLI].RecvWaitMaxS != 20 {
		t.Fatalf("Me: %+v, %v", me, err)
	}
	ctx, cancel := context.WithTimeout(h.Ctx(), 5*time.Second)
	defer cancel()
	type answer struct {
		raw []byte
		err error
	}
	result := make(chan answer, 1)
	go func() {
		seconds := int64(86400)
		raw, err := c.Recv(ctx, agentapi.RecvRequest{Frontend: adapterproto.FrontendCLI, WaitS: &seconds})
		result <- answer{raw, err}
	}()
	for engine.Waiting(h.Engine()) != 1 {
		select {
		case out := <-result:
			t.Fatalf("recv ended before waiting: %s, %v", out.raw, out.err)
		case <-ctx.Done():
			t.Fatal("the HTTP receive did not register a waiter")
		default:
		}
		h.Quiesce()
		runtime.Gosched()
	}
	h.Advance(19 * time.Second)
	h.Quiesce()
	if engine.Waiting(h.Engine()) != 1 {
		t.Fatal("the receive stopped waiting before the declared CLI limit")
	}
	select {
	case out := <-result:
		t.Fatalf("the HTTP receive ended before the declared CLI limit: %s, %v", out.raw, out.err)
	default:
	}
	h.Advance(time.Second)
	h.Quiesce()
	select {
	case out := <-result:
		var page adapterproto.RecvPage
		if err := json.Unmarshal(out.raw, &page); err != nil {
			t.Fatalf("recv: %s, %v, decode: %v", out.raw, out.err, err)
		}
		if out.err != nil || len(page.Messages) != 0 || page.Lease == "" {
			t.Fatalf("recv: %s, %v; want an empty page at the declared CLI limit", out.raw, out.err)
		}
	case <-ctx.Done():
		t.Fatal("the HTTP receive did not end at the declared CLI limit")
	}
}
