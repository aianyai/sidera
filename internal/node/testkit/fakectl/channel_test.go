package fakectl_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/sdk/go/peer"
)

const limit = 10 * time.Second

func ctxWithLimit(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	t.Cleanup(cancel)
	return ctx
}

func listen(t *testing.T) (*fakectl.Server, context.Context) {
	t.Helper()
	leakGuard(t)
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := fakectl.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() {
		_ = srv.Close()
		cancel()
	})
	return srv, ctxWithLimit(t)
}

func dial(t *testing.T, srv *fakectl.Server, hello fakectl.HelloParams, setup func(*fakectl.Client)) *fakectl.Client {
	t.Helper()
	ctx := ctxWithLimit(t)
	c, err := fakectl.Dial(ctx, srv.Addr())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if setup != nil {
		setup(c)
	}
	if err := c.Start(ctx, hello); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

var adapterHello = fakectl.HelloParams{Role: fakectl.RoleAdapter, ID: "fake", PID: 4242}

func TestListenIsOnTheLoopbackOnly(t *testing.T) {
	srv, _ := listen(t)
	host, _, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("the control channel listens on %s, not on the loopback", srv.Addr())
	}
}

// TestHelloIdentifiesTheConnection: the server learns who connected, and a wait
// for an id returns once that process has said hello.
func TestHelloIdentifiesTheConnection(t *testing.T) {
	srv, ctx := listen(t)
	dial(t, srv, adapterHello, nil)
	conn, err := srv.Wait(ctx, "fake")
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := conn.Hello(); got != adapterHello {
		t.Fatalf("Hello() = %+v, want %+v", got, adapterHello)
	}
}

func TestWaitEndsWithItsContext(t *testing.T) {
	srv, _ := listen(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := srv.Wait(ctx, "nobody"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v, want the context's error", err)
	}
}

// TestClockAndStepReachTheClientBeforeTheCallReturns: the fixture advances the
// clock of every fake process and then waits for an answer, so that the next
// step runs on a clock every process has seen.
func TestClockAndStepReachTheClientBeforeTheCallReturns(t *testing.T) {
	srv, ctx := listen(t)
	clk := fakectl.NewClock(1000)
	var step string
	var mu sync.Mutex
	dial(t, srv, adapterHello, func(c *fakectl.Client) {
		c.HandleClock(clk.SetMS)
		c.HandleStep(func(s string) { mu.Lock(); step = s; mu.Unlock() })
	})
	conn, err := srv.Wait(ctx, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.SetClock(ctx, 5000); err != nil {
		t.Fatalf("SetClock: %v", err)
	}
	if got := clk.Now().UnixMilli(); got != 5000 {
		t.Fatalf("the clock reads %d after SetClock returned, want 5000", got)
	}
	if err := conn.SetStep(ctx, "b receives"); err != nil {
		t.Fatalf("SetStep: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if step != "b receives" {
		t.Fatalf("step = %q", step)
	}
}

// TestKnobCallsTheClientsHandler: arguments arrive as sent, the result comes
// back, and an error of the handler is an error of the call.
func TestKnobCallsTheClientsHandler(t *testing.T) {
	srv, ctx := listen(t)
	dial(t, srv, adapterHello, func(c *fakectl.Client) {
		c.HandleKnob(func(_ context.Context, name string, args json.RawMessage) (any, error) {
			switch name {
			case "echo":
				return map[string]any{"args": string(args)}, nil
			case "fail":
				return nil, peer.NewError(1042, "knob refused")
			}
			return nil, peer.NewError(peer.CodeMethodNotFound, "no such knob")
		})
	})
	conn, _ := srv.Wait(ctx, "fake")

	var out struct {
		Args string `json:"args"`
	}
	if err := conn.Knob(ctx, "echo", map[string]int{"n": 3}, &out); err != nil {
		t.Fatalf("Knob(echo): %v", err)
	}
	if out.Args != `{"n":3}` {
		t.Fatalf("the handler got args %s", out.Args)
	}
	var pe *peer.Error
	if err := conn.Knob(ctx, "fail", nil, nil); !errors.As(err, &pe) || pe.Code != 1042 {
		t.Fatalf("Knob(fail) = %v, want the handler's error 1042", err)
	}
	if err := conn.Knob(ctx, "Bad Name", nil, nil); err == nil {
		t.Fatal("a knob name outside the vocabulary was sent")
	}
}

func TestReportsReachTheServer(t *testing.T) {
	srv, ctx := listen(t)
	c := dial(t, srv, adapterHello, nil)
	conn, _ := srv.Wait(ctx, "fake")
	if err := c.Report(ctx, "paused", "create.after-native"); err != nil {
		t.Fatalf("Report: %v", err)
	}
	select {
	case r := <-conn.Reports():
		if r.Name != "paused" || r.Detail != "create.after-native" {
			t.Fatalf("report %+v", r)
		}
	case <-ctx.Done():
		t.Fatal("the report did not arrive")
	}
}

// TestRequestsBeforeHelloAreRefused: a connection that has not said who it is
// cannot make the server act.
func TestRequestsBeforeHelloAreRefused(t *testing.T) {
	srv, ctx := listen(t)
	srv.Handle("runtime.ping", func(context.Context, *fakectl.Conn, json.RawMessage) (any, error) { return struct{}{}, nil })
	raw, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	p, err := peer.New(raw, raw, peer.Config{})
	if err != nil {
		t.Fatal(err)
	}
	p.Start()
	defer p.Close()
	err = p.Call(ctx, "runtime.ping", nil, nil)
	var pe *peer.Error
	if !errors.As(err, &pe) || pe.Code != peer.CodeNotInitialized {
		t.Fatalf("a request before hello got %v, want %d", err, peer.CodeNotInitialized)
	}
}

// TestServerHandlersSeeTheConnectionTheyServe: the runtime process serves the
// adapter and the fixture on one listener and must know which one asked.
func TestServerHandlersSeeTheConnectionTheyServe(t *testing.T) {
	srv, ctx := listen(t)
	got := make(chan fakectl.HelloParams, 1)
	srv.Handle("runtime.who", func(_ context.Context, c *fakectl.Conn, _ json.RawMessage) (any, error) {
		got <- c.Hello()
		return struct{}{}, nil
	})
	c := dial(t, srv, adapterHello, nil)
	if err := c.Call(ctx, "runtime.who", nil, nil); err != nil {
		t.Fatal(err)
	}
	if h := <-got; h != adapterHello {
		t.Fatalf("handler saw %+v", h)
	}
}

func TestBroadcastReachesEveryConnectionOfARole(t *testing.T) {
	srv, ctx := listen(t)
	var mu sync.Mutex
	seen := map[string]int{}
	handler := func(id string) func(*fakectl.Client) {
		return func(c *fakectl.Client) {
			c.Handle("runtime.turn.started", func(context.Context, json.RawMessage) (any, error) { return nil, nil })
			c.HandleNotification("runtime.turn.started", func(context.Context, json.RawMessage) {
				mu.Lock()
				seen[id]++
				mu.Unlock()
			})
		}
	}
	dial(t, srv, fakectl.HelloParams{Role: fakectl.RoleAdapter, ID: "a1", PID: 1}, handler("a1"))
	dial(t, srv, fakectl.HelloParams{Role: fakectl.RoleAdapter, ID: "a2", PID: 2}, handler("a2"))
	dial(t, srv, fakectl.HelloParams{Role: fakectl.RoleRuntime, ID: "r1", PID: 3}, handler("r1"))
	for _, id := range []string{"a1", "a2", "r1"} {
		if _, err := srv.Wait(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	srv.Broadcast(ctx, fakectl.RoleAdapter, "runtime.turn.started", map[string]string{"turn": "t1"})
	deadline := time.Now().Add(limit)
	for {
		mu.Lock()
		done := seen["a1"] == 1 && seen["a2"] == 1
		extra := seen["r1"]
		mu.Unlock()
		if done {
			if extra != 0 {
				t.Fatalf("a connection of another role got the broadcast")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("broadcast did not arrive: %v", seen)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestClientEndsWhenTheControlConnectionDrops: a fake process whose fixture is
// gone must end, so no test leaves orphans behind.
func TestClientEndsWhenTheControlConnectionDrops(t *testing.T) {
	srv, ctx := listen(t)
	c := dial(t, srv, adapterHello, nil)
	conn, _ := srv.Wait(ctx, "fake")
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-ctx.Done():
		t.Fatal("the client did not notice that the server was gone")
	}
	select {
	case <-conn.Done():
	case <-ctx.Done():
		t.Fatal("the server connection did not end")
	}
}

func TestCloseEndsEveryGoroutine(t *testing.T) {
	srv, ctx := listen(t)
	for i, id := range []string{"x1", "x2", "x3"} {
		dial(t, srv, fakectl.HelloParams{Role: fakectl.RoleAdapter, ID: id, PID: int64(i + 1)}, nil)
		if _, err := srv.Wait(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	// leakGuard, registered by listen, fails the test if a goroutine remains.
}

func TestHelloWithAnInvalidIdentityIsRefused(t *testing.T) {
	srv, _ := listen(t)
	ctx := ctxWithLimit(t)
	c, err := fakectl.Dial(ctx, srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Start(ctx, fakectl.HelloParams{Role: "daemon", ID: "x", PID: 1})
	if err == nil || !strings.Contains(err.Error(), "role") {
		t.Fatalf("Start with an invalid role = %v", err)
	}
}
