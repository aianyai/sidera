package peer_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/peer"
)

func startPair(t *testing.T, setupA, setupB func(*peer.Peer)) (a, b *peer.Peer) {
	t.Helper()
	a, b = newPair(t, peer.Params{}, peer.Params{}, nil)
	if setupA != nil {
		setupA(a)
	}
	if setupB != nil {
		setupB(b)
	}
	a.Start()
	b.Start()
	return a, b
}

func TestCallBothDirections(t *testing.T) {
	leakGuard(t)
	handler := func(name string) func(*peer.Peer) {
		return func(p *peer.Peer) {
			p.Handle("who", func(_ context.Context, params json.RawMessage) (any, error) {
				var in struct{ N int }
				if err := json.Unmarshal(params, &in); err != nil {
					return nil, peer.NewError(peer.CodeInvalidParams, "bad params")
				}
				return map[string]any{"from": name, "n": in.N + 1}, nil
			})
		}
	}
	a, b := startPair(t, handler("a"), handler("b"))
	for _, tc := range []struct {
		caller *peer.Peer
		want   string
	}{{a, "b"}, {b, "a"}} {
		var out struct {
			From string
			N    int
		}
		if err := tc.caller.Call(context.Background(), "who", map[string]int{"n": 41}, &out); err != nil {
			t.Fatal(err)
		}
		if out.From != tc.want || out.N != 42 {
			t.Fatalf("got %+v", out)
		}
	}
	var rawResult json.RawMessage
	if err := a.Call(context.Background(), "who", map[string]int{"n": 1}, &rawResult); err != nil || !strings.Contains(string(rawResult), `"n":2`) {
		t.Fatalf("raw result %s err %v", rawResult, err)
	}
	if err := a.Call(context.Background(), "who", map[string]int{"n": 1}, nil); err != nil {
		t.Fatalf("discarding the result: %v", err)
	}
}

func TestHandlerErrorsAndEffectRule(t *testing.T) {
	leakGuard(t)
	a, _ := startPair(t, nil, func(b *peer.Peer) {
		b.Handle("app", func(context.Context, json.RawMessage) (any, error) {
			return nil, peer.NewError(1003, "capability not declared").WithData(map[string]string{"capability": "x"})
		})
		b.Handle("params", func(context.Context, json.RawMessage) (any, error) {
			return nil, peer.NewError(peer.CodeInvalidParams, "bad")
		})
		b.Handle("boom", func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New("database password is hunter2")
		})
		b.Handle("panic", func(context.Context, json.RawMessage) (any, error) { panic("oops") })
		b.Handle("code", func(context.Context, json.RawMessage) (any, error) {
			return nil, fmt.Errorf("wrapped: %w", peer.NewError(-32099, "custom server error"))
		})
		b.Handle("badresult", func(context.Context, json.RawMessage) (any, error) { return make(chan int), nil })
		b.Handle("ping", pingHandler)
	})
	ctx := context.Background()

	err := a.Call(ctx, "app", nil, nil)
	var re *peer.Error
	if !errors.As(err, &re) || re.Code != 1003 || !strings.Contains(string(re.Data), `"capability":"x"`) {
		t.Fatalf("application error: %v", err)
	}
	if !peer.NoEffect(err) || peer.ResultUnknown(err) {
		t.Fatal("an application error means no effect")
	}

	err = a.Call(ctx, "params", nil, nil)
	if !peer.NoEffect(err) {
		t.Fatalf("-32602 means no effect: %v", err)
	}

	for _, method := range []string{"boom", "panic", "badresult"} {
		err = a.Call(ctx, method, nil, nil)
		if !errors.As(err, &re) || re.Code != peer.CodeInternalError || re.Message != "internal error" {
			t.Fatalf("%s: %v, want a generic -32603", method, err)
		}
		if peer.NoEffect(err) || !peer.ResultUnknown(err) {
			t.Fatalf("%s: -32603 leaves the result unknown", method)
		}
	}

	err = a.Call(ctx, "code", nil, nil)
	if !errors.As(err, &re) || re.Code != -32099 || !peer.ResultUnknown(err) {
		t.Fatalf("an unlisted server-range code must leave the result unknown: %v", err)
	}

	err = a.Call(ctx, "missing", nil, nil)
	if !errors.As(err, &re) || re.Code != peer.CodeMethodNotFound || !peer.NoEffect(err) {
		t.Fatalf("unknown method: %v", err)
	}
	// The peer survived the panic.
	if err := a.Call(ctx, "ping", nil, nil); err != nil {
		t.Fatal(err)
	}
}

// TestEffectClassification lists the effect rule by hand.
func TestEffectClassification(t *testing.T) {
	for _, code := range []int{-32700, -32600, -32601, -32602, -32001, -32002, -32003, -32004, -32022, 1001, 1002, 1011, 1050, 1099} {
		if err := peer.NewError(code, "x"); !peer.NoEffect(err) || peer.ResultUnknown(err) {
			t.Errorf("code %d must mean no effect", code)
		}
	}
	for _, code := range []int{-32603, -32800, -32000, -32005, -32099, -32768, 0, 1, 1000, 1100, 5000} {
		if err := peer.NewError(code, "x"); peer.NoEffect(err) || !peer.ResultUnknown(err) {
			t.Errorf("code %d must leave the result unknown", code)
		}
	}
	notSentCtx := fmt.Errorf("%w: %w", peer.ErrNotSent, context.Canceled)
	for name, tc := range map[string]struct {
		err      error
		noEffect bool
	}{
		"local overload":              {peer.ErrOverloaded, true},
		"not sent":                    {peer.ErrNotSent, true},
		"message too large":           {peer.ErrMessageTooLarge, true},
		"context done before sending": {notSentCtx, true},
		"wrapped remote error":        {fmt.Errorf("call: %w", peer.NewError(1005, "x")), true},
		"closed":                      {peer.ErrClosed, false},
		"cancelled after sending":     {context.Canceled, false},
		"deadline exceeded":           {context.DeadlineExceeded, false},
		"malformed response":          {peer.ErrInvalidResponse, false},
		"write timeout":               {peer.ErrWriteTimeout, false},
		"remote closed":               {peer.ErrRemoteClosed, false},
		"arbitrary error":             {errors.New("x"), false},
	} {
		if got := peer.NoEffect(tc.err); got != tc.noEffect {
			t.Errorf("%s: NoEffect = %v, want %v", name, got, tc.noEffect)
		}
		if got := peer.ResultUnknown(tc.err); got == tc.noEffect {
			t.Errorf("%s: ResultUnknown = %v, want %v", name, got, !tc.noEffect)
		}
	}
	if peer.NoEffect(nil) || peer.ResultUnknown(nil) {
		t.Error("nil is neither")
	}
	if !errors.Is(peer.NewError(peer.CodeOverloaded, "x"), peer.ErrOverloaded) {
		t.Error("a remote -32001 should match ErrOverloaded")
	}
	if errors.Is(peer.NewError(peer.CodeInternalError, "x"), peer.ErrOverloaded) {
		t.Error("only -32001 matches ErrOverloaded")
	}
}

func TestCloseEndsPendingCallsWithErrClosed(t *testing.T) {
	leakGuard(t)
	var started sync.WaitGroup
	started.Add(3)
	handlerCtxDone := make(chan struct{}, 3)
	a, b := startPair(t, nil, func(b *peer.Peer) {
		b.Handle("block", func(ctx context.Context, _ json.RawMessage) (any, error) {
			started.Done()
			<-ctx.Done()
			handlerCtxDone <- struct{}{}
			return nil, ctx.Err()
		})
	})
	results := make(chan error, 3)
	for range 3 {
		go func() { results <- a.Call(context.Background(), "block", nil, nil) }()
	}
	started.Wait()

	var closers sync.WaitGroup
	for range 4 { // Close is idempotent and safe to race
		closers.Add(1)
		go func() { defer closers.Done(); a.Close() }()
	}
	closers.Wait()
	for range 3 {
		select {
		case err := <-results:
			if err != peer.ErrClosed {
				t.Fatalf("pending call ended with %v, want ErrClosed", err)
			}
		case <-time.After(wait):
			t.Fatal("pending call did not end")
		}
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := a.Err(); err != peer.ErrClosed {
		t.Fatalf("Err = %v, want ErrClosed", err)
	}
	if err := a.Call(context.Background(), "block", nil, nil); err != peer.ErrClosed {
		t.Fatalf("Call after Close = %v", err)
	}
	if err := a.Notify(context.Background(), "x", nil); err != peer.ErrClosed {
		t.Fatalf("Notify after Close = %v", err)
	}
	waitDone(t, a)

	// The other side sees the connection end and cancels its handlers.
	waitDone(t, b)
	if err := b.Err(); err != peer.ErrRemoteClosed {
		t.Fatalf("remote Err = %v, want ErrRemoteClosed", err)
	}
	for range 3 {
		select {
		case <-handlerCtxDone:
		case <-time.After(wait):
			t.Fatal("handler context was not cancelled")
		}
	}
}

func TestCloseWithoutStartAndCloseFromHandler(t *testing.T) {
	leakGuard(t)
	a, b := newPair(t, peer.Params{}, peer.Params{}, nil)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b.Close()

	// A handler may close its own peer.
	a, b = startPair(t, nil, nil)
	a.Handle("bye", func(context.Context, json.RawMessage) (any, error) {
		a.Close()
		return struct{}{}, nil
	})
	b.Call(context.Background(), "bye", nil, nil)
	waitDone(t, a)
}

func TestCallContextCancellation(t *testing.T) {
	leakGuard(t)
	handlerCancelled := make(chan struct{})
	started := make(chan struct{})
	a, _ := startPair(t, nil, func(b *peer.Peer) {
		b.Handle("wait", func(ctx context.Context, _ json.RawMessage) (any, error) {
			close(started)
			<-ctx.Done() // only ends because $/cancel_request arrived
			close(handlerCancelled)
			return nil, ctx.Err()
		})
	})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- a.Call(ctx, "wait", nil, nil) }()
	<-started
	cancel()
	if err := <-errc; err != context.Canceled || !peer.ResultUnknown(err) {
		t.Fatalf("Call = %v, want context.Canceled with an unknown result", err)
	}
	select {
	case <-handlerCancelled:
	case <-time.After(wait):
		t.Fatal("the handler was not cancelled by the caller's context")
	}

	// A context that is already done sends nothing and has no effect.
	if err := a.Call(ctx, "wait", nil, nil); !errors.Is(err, context.Canceled) || !peer.NoEffect(err) {
		t.Fatalf("Call with a done context = %v", err)
	}
	if err := a.Notify(ctx, "x", nil); !errors.Is(err, context.Canceled) || !peer.NoEffect(err) {
		t.Fatalf("Notify with a done context = %v", err)
	}
}

func TestOutboundMessageValidation(t *testing.T) {
	leakGuard(t)
	p, r, _ := newRaw(t, peer.Params{MaxLineBytes: 200}, nil)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		params any
		want   error
	}{
		"too large":             {map[string]string{"s": strings.Repeat("x", 300)}, peer.ErrMessageTooLarge},
		"params not structured": {"text", peer.ErrNotSent},
		"params number":         {5, peer.ErrNotSent},
		"params do not marshal": {make(chan int), peer.ErrNotSent},
	} {
		err := p.Call(ctx, "m", tc.params, nil)
		if !errors.Is(err, tc.want) || !peer.NoEffect(err) {
			t.Errorf("%s: Call = %v", name, err)
		}
		if err := p.Notify(ctx, "m", tc.params); !errors.Is(err, tc.want) {
			t.Errorf("%s: Notify = %v", name, err)
		}
	}
	if err := p.Call(ctx, "", nil, nil); !errors.Is(err, peer.ErrNotSent) {
		t.Errorf("empty method: %v", err)
	}
	r.expectNoLineFor(100 * time.Millisecond) // none of the refused messages was written
}

func TestResultTooLargeIsAnInternalError(t *testing.T) {
	leakGuard(t)
	_, r, _ := newRaw(t, peer.Params{MaxLineBytes: 200}, func(p *peer.Peer) {
		p.Handle("big", func(context.Context, json.RawMessage) (any, error) { return strings.Repeat("x", 500), nil })
	})
	r.send(`{"jsonrpc":"2.0","id":1,"method":"big"}`)
	if f := r.recv(); f.errCode() != peer.CodeInternalError {
		t.Fatalf("got %+v", f)
	}
}

func TestMalformedResponseFailsTheCall(t *testing.T) {
	leakGuard(t)
	for name, body := range map[string]string{
		"result and error":   `"result":{},"error":{"code":1,"message":"x"}`,
		"error without code": `"error":{"message":"x"}`,
		"error not object":   `"error":"nope"`,
	} {
		t.Run(name, func(t *testing.T) {
			p, r, _ := newRaw(t, peer.Params{}, nil)
			errc := make(chan error, 1)
			go func() { errc <- p.Call(context.Background(), "m", nil, nil) }()
			req := r.recv()
			r.send(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,` + body + `}`)
			select {
			case err := <-errc:
				if !errors.Is(err, peer.ErrInvalidResponse) || !peer.ResultUnknown(err) {
					t.Fatalf("Call = %v", err)
				}
			case <-time.After(wait):
				t.Fatal("call hung on a malformed response")
			}
		})
	}
}

func TestResultDecodeFailureIsReportedWithUnknownEffect(t *testing.T) {
	leakGuard(t)
	a, _ := startPair(t, nil, func(b *peer.Peer) {
		b.Handle("text", func(context.Context, json.RawMessage) (any, error) { return "just text", nil })
	})
	var into struct{ X int }
	err := a.Call(context.Background(), "text", nil, &into)
	if err == nil || !peer.ResultUnknown(err) {
		t.Fatalf("Call = %v; the other side acted, so the effect is not 'none'", err)
	}
}

func TestParamsResolution(t *testing.T) {
	d := peer.DefaultParams()
	want := peer.Params{MaxLineBytes: 1 << 20, MaxPendingRequests: 64, MaxConcurrentHandlers: 16,
		MaxPendingNotifications: 256, MaxOutbound: 256, WriteTimeout: 10 * time.Second}
	if d != want {
		t.Fatalf("DefaultParams = %+v", d)
	}
	for _, bad := range []peer.Params{
		{MaxLineBytes: -1}, {MaxPendingRequests: -1}, {MaxConcurrentHandlers: -1},
		{MaxPendingNotifications: -1}, {MaxOutbound: -1}, {WriteTimeout: -1},
	} {
		if _, err := peer.New(strings.NewReader(""), nopWriteCloser{}, peer.Config{Params: bad}); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if _, err := peer.New(strings.NewReader(""), nopWriteCloser{}, peer.Config{}); err != nil {
		t.Fatal(err)
	}
}

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

func TestRegistrationPanicsOnMisuse(t *testing.T) {
	p, err := peer.New(strings.NewReader(""), nopWriteCloser{}, peer.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]func(){
		"empty method":       func() { p.Handle("", pingHandler) },
		"nil handler":        func() { p.Handle("m", nil) },
		"cancel as request":  func() { p.Handle(peer.MethodCancelRequest, pingHandler) },
		"cancel as notified": func() { p.HandleNotification(peer.MethodCancelRequest, func(context.Context, json.RawMessage) {}) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			f()
		}()
	}
}

func TestNoGoroutineLeakAfterManyPeers(t *testing.T) {
	leakGuard(t)
	for i := range 20 {
		a, b := startPair(t, nil, func(b *peer.Peer) {
			b.Handle("m", pingHandler)
			b.HandleNotification("n", func(context.Context, json.RawMessage) {})
		})
		for range 5 {
			if err := a.Call(context.Background(), "m", nil, nil); err != nil {
				t.Fatal(err)
			}
			a.Notify(context.Background(), "n", nil)
		}
		if i%2 == 0 {
			a.Close()
		} else {
			b.Close()
		}
		waitDone(t, a)
		waitDone(t, b)
		a.Close()
		b.Close()
	}
	deadline := time.Now().Add(wait)
	for len(peerGoroutines()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines left:\n%s", strings.Join(peerGoroutines(), "\n\n"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Close waits for the peer's reader and writer: once it returns, neither is
// left running. (Request workers end as soon as their handler returns.)
func TestCloseWaitsForReaderAndWriter(t *testing.T) {
	leakGuard(t)
	for range 50 {
		p, _, _ := newRaw(t, peer.Params{}, nil) // the only peer alive
		p.Close()
		for _, stack := range peerGoroutines() {
			if strings.Contains(stack, "readLoop") || strings.Contains(stack, "writeLoop") {
				t.Fatalf("Close returned while a reader or writer goroutine was alive:\n%s", stack)
			}
		}
	}
}
