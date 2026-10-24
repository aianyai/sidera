package peer_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/peer"
)

// The dialect under test borrows nothing from the adapter protocol: another
// method name, another params member, a lenient reader, silence after a cancel
// and two codes that are none of the peer's own. Distinct values make a rule
// that falls back to the default visible.
const (
	dialectCancelMethod = "test/cancelled"
	dialectOverloaded   = -31001
	dialectLineTooLong  = -31002
)

// lenientCancelTarget reads {"requestId": <id>} and ignores every other member.
func lenientCancelTarget(params json.RawMessage) (peer.RequestID, bool) {
	var body struct {
		RequestID *peer.RequestID `json:"requestId"`
	}
	if json.Unmarshal(params, &body) != nil || body.RequestID == nil {
		return peer.RequestID{}, false
	}
	return *body.RequestID, true
}

func testDialect() peer.Dialect {
	return peer.Dialect{
		CancelMethod: dialectCancelMethod,
		CancelTarget: lenientCancelTarget,
		CancelParams: func(id peer.RequestID) any {
			return map[string]any{"requestId": id, "reason": "test"}
		},
		SilentOnCancel:  true,
		OverloadedCode:  dialectOverloaded,
		LineTooLongCode: dialectLineTooLong,
	}
}

// newDialectRaw is newRaw with a Config. Its peers do not write to the wire log
// that the root module checks against the adapter protocol's message schema:
// what a dialect sends is not that protocol's.
func newDialectRaw(t *testing.T, cfg peer.Config, setup func(*peer.Peer), drain bool) (*peer.Peer, *raw, *logBuf) {
	t.Helper()
	toPeerR, toPeerW := io.Pipe()
	fromPeerR, fromPeerW := io.Pipe()
	logs := &logBuf{}
	cfg.Logger = logs.logger()
	p, err := peer.New(toPeerR, fromPeerW, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(p)
	}
	p.Start()
	r := &raw{t: t, toPeer: toPeerW, fromPeer: fromPeerR, br: bufio.NewReader(fromPeerR), lines: make(chan lineResult, 4096)}
	if drain {
		go r.pump()
	}
	t.Cleanup(func() {
		p.Close()
		toPeerW.Close()
		fromPeerR.Close()
	})
	return p, r, logs
}

// awaitSignal waits for one value on ch, failing the test instead of hanging.
func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(wait):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func cancelNote(params string) string {
	return `{"jsonrpc":"2.0","method":"` + dialectCancelMethod + `","params":` + params + `}`
}

// answerAfterRelease sends request id (a ping) until the peer serves it, which
// shows that the id of an earlier request is no longer in flight. A refusal
// because the id is still in flight is -32600 and only ever transient here.
func answerAfterRelease(t *testing.T, r *raw, id string) frame {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		r.send(`{"jsonrpc":"2.0","id":` + id + `,"method":"ping"}`)
		f := r.recv()
		if f.errCode() != peer.CodeInvalidRequest {
			return f
		}
		if time.Now().After(deadline) {
			t.Fatalf("id %s is still in flight after the request was cancelled: %+v", id, f)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDialectCancelledRequestIsNotAnswered(t *testing.T) {
	leakGuard(t)
	cfg := peer.Config{Dialect: testDialect()}

	t.Run("a handler that honours the cancel leaves nothing on the wire and its id is released", func(t *testing.T) {
		started := make(chan struct{}, 1)
		returned := make(chan struct{}, 1)
		_, r, _ := newDialectRaw(t, cfg, func(p *peer.Peer) {
			p.Handle("wait", func(ctx context.Context, _ json.RawMessage) (any, error) {
				started <- struct{}{}
				<-ctx.Done()
				returned <- struct{}{}
				return nil, ctx.Err()
			})
			p.Handle("ping", pingHandler)
		}, true)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"wait"}`)
		<-started
		r.send(cancelNote(`{"requestId":1}`))
		awaitSignal(t, returned, "the handler to return after the cancel")
		// The answer, if one were owed, is queued right after the handler returns.
		r.expectNoLineFor(100 * time.Millisecond)
		if f := answerAfterRelease(t, r, "1"); string(f.ID) != "1" || f.Error != nil {
			t.Fatalf("got %+v, want the id 1 to be reusable and answered normally", f)
		}
	})

	t.Run("a handler that ignores the cancel is not answered either", func(t *testing.T) {
		started := make(chan struct{}, 1)
		release := make(chan struct{})
		_, r, _ := newDialectRaw(t, cfg, func(p *peer.Peer) {
			p.Handle("stubborn", func(context.Context, json.RawMessage) (any, error) {
				started <- struct{}{}
				<-release
				return map[string]string{"done": "yes"}, nil
			})
			p.Handle("ping", pingHandler)
		}, true)
		r.send(`{"jsonrpc":"2.0","id":"abc","method":"stubborn"}`)
		<-started
		r.send(cancelNote(`{"requestId":"abc"}`))
		// The read loop answers this on its own, so once the reply is here the cancel was processed.
		r.send(`{"jsonrpc":"2.0","id":7,"method":"no.such.method"}`)
		if f := r.recv(); string(f.ID) != "7" {
			t.Fatalf("got %+v", f)
		}
		close(release)
		r.expectNoLineFor(100 * time.Millisecond)
		if f := answerAfterRelease(t, r, `"abc"`); string(f.ID) != `"abc"` || f.Error != nil {
			t.Fatalf("got %+v, want the string id to be reusable", f)
		}
	})

	t.Run("a request cancelled while queued never starts its handler and is not answered", func(t *testing.T) {
		started := make(chan struct{}, 4)
		release := make(chan struct{})
		var ran atomic.Int32
		_, r, _ := newDialectRaw(t, peer.Config{Params: peer.Params{MaxConcurrentHandlers: 1}, Dialect: testDialect()}, func(p *peer.Peer) {
			p.Handle("slow", func(context.Context, json.RawMessage) (any, error) {
				ran.Add(1)
				started <- struct{}{}
				<-release
				return struct{}{}, nil
			})
			p.Handle("ping", pingHandler)
		}, true)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"slow"}`)
		<-started
		r.send(`{"jsonrpc":"2.0","id":2,"method":"slow"}`)
		r.send(cancelNote(`{"requestId":2}`))
		// The read loop answers this on its own, so once the reply is here the cancel was processed.
		r.send(`{"jsonrpc":"2.0","id":3,"method":"no.such.method"}`)
		if f := r.recv(); string(f.ID) != "3" {
			t.Fatalf("got %+v", f)
		}
		close(release)
		if f := r.recv(); string(f.ID) != "1" || f.Error != nil {
			t.Fatalf("got %+v, want only the answer to request 1", f)
		}
		r.expectNoLineFor(100 * time.Millisecond)
		if ran.Load() != 1 {
			t.Fatalf("handler ran %d times, want 1: request 2 was cancelled before it started", ran.Load())
		}
		if f := answerAfterRelease(t, r, "2"); string(f.ID) != "2" || f.Error != nil {
			t.Fatalf("got %+v, want the id 2 to be reusable", f)
		}
	})
}

func TestDialectCancelTargetReadsTheParamsItIsGiven(t *testing.T) {
	leakGuard(t)
	var seen atomic.Value // json.RawMessage of the last notification
	d := testDialect()
	d.CancelTarget = func(params json.RawMessage) (peer.RequestID, bool) {
		seen.Store(append(json.RawMessage(nil), params...))
		return lenientCancelTarget(params)
	}
	started := make(chan struct{}, 1)
	_, r, _ := newDialectRaw(t, peer.Config{Dialect: d}, func(p *peer.Peer) {
		p.Handle("wait", func(ctx context.Context, _ json.RawMessage) (any, error) {
			started <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		})
		p.Handle("ping", pingHandler)
	}, true)
	r.send(`{"jsonrpc":"2.0","id":1,"method":"wait"}`)
	<-started
	// Members the adapter protocol's strict decoding would refuse are fine for a lenient reader.
	const params = `{"requestId":1,"reason":"user abandoned it","_meta":{"k":1}}`
	r.send(cancelNote(params))
	r.expectNoLineFor(100 * time.Millisecond)
	if got, _ := seen.Load().(json.RawMessage); string(got) != params {
		t.Fatalf("CancelTarget was given %q, want %q", got, params)
	}
	// Proof that request 1 was cancelled and released: its id can be used again.
	if f := answerAfterRelease(t, r, "1"); string(f.ID) != "1" || f.Error != nil {
		t.Fatalf("got %+v", f)
	}
}

func TestDialectCancelTargetFalseCancelsNothing(t *testing.T) {
	leakGuard(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var cancelled atomic.Bool
	_, r, _ := newDialectRaw(t, peer.Config{Dialect: testDialect()}, func(p *peer.Peer) {
		p.Handle("wait", func(ctx context.Context, _ json.RawMessage) (any, error) {
			started <- struct{}{}
			select {
			case <-ctx.Done():
				cancelled.Store(true)
				return nil, ctx.Err()
			case <-release:
				return struct{}{}, nil
			}
		})
	}, true)
	// The zero value of a missing id is 0, so request 0 shows that none of these
	// is read as a cancel of it.
	r.send(`{"jsonrpc":"2.0","id":0,"method":"wait"}`)
	<-started
	for _, params := range []string{
		`{}`, `{"requestId":null}`, `{"requestId":""}`, `{"requestId":1.5}`, `{"requestId":[0]}`,
		`"junk"`, `[0]`,
		// The adapter protocol's member is not this dialect's.
		`{"request_id":0}`,
	} {
		r.send(cancelNote(params))
	}
	r.send(`{"jsonrpc":"2.0","method":"` + dialectCancelMethod + `"}`)
	// The adapter protocol's own cancel is an unknown notification under this dialect.
	r.send(`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"request_id":0}}`)
	// The read loop answers this on its own, so once the reply is here the cancels were processed.
	r.send(`{"jsonrpc":"2.0","id":1,"method":"no.such.method"}`)
	if f := r.recv(); string(f.ID) != "1" || f.errCode() != peer.CodeMethodNotFound {
		t.Fatalf("got %+v", f)
	}
	close(release)
	if f := r.recv(); string(f.ID) != "0" || f.Error != nil {
		t.Fatalf("request 0 was disturbed: %+v", f)
	}
	if cancelled.Load() {
		t.Fatal("the handler's context was cancelled")
	}
}

// A CancelTarget that returns an id the request id rule refuses cancels nothing,
// so a reader that reports "found" for an unset id cannot cancel request 0.
func TestDialectCancelTargetWithAnInvalidIDCancelsNothing(t *testing.T) {
	leakGuard(t)
	d := testDialect()
	d.CancelTarget = func(json.RawMessage) (peer.RequestID, bool) { return peer.RequestID{}, true }
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	_, r, _ := newDialectRaw(t, peer.Config{Dialect: d}, func(p *peer.Peer) {
		p.Handle("wait", func(ctx context.Context, _ json.RawMessage) (any, error) {
			started <- struct{}{}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
				return struct{}{}, nil
			}
		})
	}, true)
	r.send(`{"jsonrpc":"2.0","id":0,"method":"wait"}`)
	<-started
	r.send(cancelNote(`{"requestId":0}`))
	r.send(`{"jsonrpc":"2.0","id":1,"method":"no.such.method"}`)
	if f := r.recv(); string(f.ID) != "1" {
		t.Fatalf("got %+v", f)
	}
	close(release)
	if f := r.recv(); string(f.ID) != "0" || f.Error != nil {
		t.Fatalf("request 0 was disturbed: %+v", f)
	}
}

// A dialect that only renames the notification keeps "answered exactly once".
func TestDialectWithoutSilentOnCancelStillAnswersCancelledRequests(t *testing.T) {
	leakGuard(t)
	d := testDialect()
	d.SilentOnCancel = false
	started := make(chan struct{}, 1)
	_, r, _ := newDialectRaw(t, peer.Config{Dialect: d}, func(p *peer.Peer) {
		p.Handle("wait", func(ctx context.Context, _ json.RawMessage) (any, error) {
			started <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		})
		p.Handle("ping", pingHandler)
	}, true)
	r.send(`{"jsonrpc":"2.0","id":1,"method":"wait"}`)
	<-started
	r.send(cancelNote(`{"requestId":1}`))
	if f := r.recv(); string(f.ID) != "1" || f.errCode() != peer.CodeRequestCancelled {
		t.Fatalf("got %+v, want id 1 answered with -32800", f)
	}
	r.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if f := r.recv(); string(f.ID) != "2" {
		t.Fatalf("got %+v: id 1 was answered twice", f)
	}
}

// A cancellation that arrives after the answer was queued changes nothing: the
// request is answered once and the late cancel finds nothing in flight.
func TestDialectCancelAfterTheAnswerWasQueuedStillGetsThatOneAnswer(t *testing.T) {
	leakGuard(t)
	started2 := make(chan struct{}, 1)
	release2 := make(chan struct{})
	// One worker serves requests in order, and nothing reads the peer's output
	// yet: when request 2's handler starts, the answer to request 1 is queued
	// and has not reached the wire.
	_, r, _ := newDialectRaw(t, peer.Config{Params: peer.Params{MaxConcurrentHandlers: 1}, Dialect: testDialect()}, func(p *peer.Peer) {
		p.Handle("quick", func(context.Context, json.RawMessage) (any, error) {
			return map[string]string{"done": "yes"}, nil
		})
		p.Handle("slow", func(context.Context, json.RawMessage) (any, error) {
			started2 <- struct{}{}
			<-release2
			return struct{}{}, nil
		})
	}, false)
	r.send(`{"jsonrpc":"2.0","id":1,"method":"quick"}`)
	r.send(`{"jsonrpc":"2.0","id":2,"method":"slow"}`)
	<-started2
	r.send(cancelNote(`{"requestId":1}`))
	// Answered by the read loop itself, so it is queued after the cancel was processed.
	r.send(`{"jsonrpc":"2.0","id":3,"method":"no.such.method"}`)
	go r.pump()
	if f := r.recv(); string(f.ID) != "1" || f.Error != nil || !strings.Contains(string(f.Result), "yes") {
		t.Fatalf("got %+v, want request 1's own result as the first line", f)
	}
	if f := r.recv(); string(f.ID) != "3" || f.errCode() != peer.CodeMethodNotFound {
		t.Fatalf("got %+v, want the unknown method's reply", f)
	}
	close(release2)
	if f := r.recv(); string(f.ID) != "2" || f.Error != nil {
		t.Fatalf("got %+v", f)
	}
	r.expectNoLineFor(100 * time.Millisecond)
}

func TestDialectReplacementCodesAreSent(t *testing.T) {
	leakGuard(t)

	t.Run("overloaded", func(t *testing.T) {
		started := make(chan struct{}, 8)
		release := make(chan struct{})
		var ran atomic.Int32
		_, r, _ := newDialectRaw(t, peer.Config{
			Params:  peer.Params{MaxPendingRequests: 2, MaxConcurrentHandlers: 1},
			Dialect: testDialect(),
		}, func(p *peer.Peer) {
			p.Handle("slow", func(ctx context.Context, _ json.RawMessage) (any, error) {
				ran.Add(1)
				started <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
				}
				return struct{}{}, nil
			})
		}, true)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"slow"}`)
		<-started
		r.send(`{"jsonrpc":"2.0","id":2,"method":"slow"}`)
		r.send(`{"jsonrpc":"2.0","id":3,"method":"slow"}`) // the queue (2) is now full
		r.send(`{"jsonrpc":"2.0","id":4,"method":"slow"}`)
		f := r.recv()
		if string(f.ID) != "4" || f.errCode() != dialectOverloaded || f.Error.Message != "overloaded" {
			t.Fatalf("got %+v, want id 4 refused with code %d and the message overloaded", f, dialectOverloaded)
		}
		close(release)
		for range 3 {
			if f := r.recv(); f.Error != nil {
				t.Fatalf("queued request failed: %+v", f)
			}
		}
		if got := ran.Load(); got != 3 {
			t.Fatalf("handler ran %d times, want 3 (never for the refused request)", got)
		}
	})

	t.Run("line too long", func(t *testing.T) {
		const max = 256
		p, r, _ := newDialectRaw(t, peer.Config{Params: peer.Params{MaxLineBytes: max}, Dialect: testDialect()}, func(p *peer.Peer) {
			p.Handle("echo", func(context.Context, json.RawMessage) (any, error) { return struct{}{}, nil })
		}, true)
		go r.toPeer.Write([]byte(strings.Repeat("a", 3*max)))
		f := r.recv()
		if f.errCode() != dialectLineTooLong || string(f.ID) != "null" {
			t.Fatalf("got %+v, want code %d with a null id", f, dialectLineTooLong)
		}
		r.expectEOF()
		waitDone(t, p)
		if err := p.Err(); err != peer.ErrLineTooLong {
			t.Fatalf("Err = %v, want ErrLineTooLong", err)
		}
	})

	t.Run("each code is replaced on its own", func(t *testing.T) {
		const max = 256
		d := peer.Dialect{LineTooLongCode: dialectLineTooLong}
		_, r, _ := newDialectRaw(t, peer.Config{Params: peer.Params{MaxLineBytes: max, MaxPendingRequests: 1, MaxConcurrentHandlers: 1}, Dialect: d}, func(p *peer.Peer) {
			p.Handle("slow", func(ctx context.Context, _ json.RawMessage) (any, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			})
		}, true)
		r.send(`{"jsonrpc":"2.0","id":1,"method":"slow"}`)
		r.send(`{"jsonrpc":"2.0","id":2,"method":"slow"}`)
		r.send(`{"jsonrpc":"2.0","id":3,"method":"slow"}`)
		// Whichever of 2 and 3 found the one-slot queue full is refused with the peer's own code.
		deadline := time.After(wait)
		for {
			select {
			case <-deadline:
				t.Fatal("no overload refusal")
			default:
			}
			f := r.recv()
			if f.errCode() == peer.CodeOverloaded {
				break
			}
			if f.Error != nil {
				t.Fatalf("got %+v, want the default overload code with only LineTooLongCode replaced", f)
			}
		}
		go r.toPeer.Write([]byte(strings.Repeat("a", 3*max)))
		for {
			f := r.recv()
			if f.errCode() == 0 {
				continue
			}
			if f.errCode() != dialectLineTooLong {
				t.Fatalf("got %+v, want code %d", f, dialectLineTooLong)
			}
			break
		}
	})
}

// What Call sends when its context ends follows the dialect, and a peer with the
// zero dialect still sends the adapter protocol's notification.
func TestCallSendsTheDialectsCancelNotification(t *testing.T) {
	leakGuard(t)
	for _, tc := range []struct {
		name       string
		dialect    peer.Dialect
		wantMethod string
		wantParams string
	}{
		{"zero dialect", peer.Dialect{}, peer.MethodCancelRequest, `{"request_id":%s}`},
		{"method and params", testDialect(), dialectCancelMethod, `{"reason":"test","requestId":%s}`},
		{"method only keeps the default params", peer.Dialect{CancelMethod: dialectCancelMethod}, dialectCancelMethod, `{"request_id":%s}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, r, _ := newDialectRaw(t, peer.Config{Dialect: tc.dialect}, nil, true)
			ctx, cancel := context.WithCancel(context.Background())
			errc := make(chan error, 1)
			go func() { errc <- p.Call(ctx, "work", nil, nil) }()
			req := r.recv()
			if req.Method != "work" || len(req.ID) == 0 {
				t.Fatalf("got %+v, want the request", req)
			}
			cancel()
			if err := <-errc; !errors.Is(err, context.Canceled) {
				t.Fatalf("Call = %v", err)
			}
			note := r.recv()
			if note.Method != tc.wantMethod || len(note.ID) != 0 {
				t.Fatalf("got %+v, want the notification %s", note, tc.wantMethod)
			}
			want := strings.Replace(tc.wantParams, "%s", string(req.ID), 1)
			var gotParams, wantParams map[string]any
			if err := json.Unmarshal(note.Params, &gotParams); err != nil {
				t.Fatalf("params %q: %v", note.Params, err)
			}
			if err := json.Unmarshal([]byte(want), &wantParams); err != nil {
				t.Fatal(err)
			}
			if got, _ := json.Marshal(gotParams); string(got) != mustMarshal(t, wantParams) {
				t.Fatalf("cancel params %s, want %s", note.Params, want)
			}
		})
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDialectCancelNotificationIsReservedAndMustBeAValidMethodName(t *testing.T) {
	p, err := peer.New(strings.NewReader(""), nopWriteCloser{}, peer.Config{Dialect: testDialect()})
	if err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]func(){
		"dialect cancel as request":  func() { p.Handle(dialectCancelMethod, pingHandler) },
		"dialect cancel as notified": func() { p.HandleNotification(dialectCancelMethod, func(context.Context, json.RawMessage) {}) },
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
	// The adapter protocol's notification is an ordinary method name under this dialect.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("registering %s under a dialect with another cancel method panicked: %v", peer.MethodCancelRequest, r)
			}
		}()
		p.Handle(peer.MethodCancelRequest, pingHandler)
	}()

	if _, err := peer.New(strings.NewReader(""), nopWriteCloser{}, peer.Config{Dialect: peer.Dialect{CancelMethod: strings.Repeat("m", 129)}}); err == nil {
		t.Error("New accepted a cancel method name that is longer than a method name may be")
	}
}

func TestDialectCancelParamsMustBuildAnObject(t *testing.T) {
	for name, tc := range map[string]struct {
		params func(peer.RequestID) any
		ok     bool
	}{
		"an object":                      {func(id peer.RequestID) any { return map[string]any{"id": id} }, true},
		"nil is sent as an empty object": {func(peer.RequestID) any { return nil }, true},
		"a number":                       {func(peer.RequestID) any { return 1 }, false},
		"a list":                         {func(id peer.RequestID) any { return []any{id} }, false},
		"a value that does not marshal":  {func(peer.RequestID) any { return make(chan int) }, false},
	} {
		_, err := peer.New(strings.NewReader(""), nopWriteCloser{}, peer.Config{Dialect: peer.Dialect{CancelParams: tc.params}})
		if tc.ok && err != nil {
			t.Errorf("%s: New refused it: %v", name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: New accepted cancel params that are not a JSON object", name)
		}
	}
}
