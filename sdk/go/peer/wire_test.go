package peer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/peer"
)

// echoCounter registers "echo" and "ping" on p; echo counts its invocations.
func echoCounter(ran *atomic.Int32) func(*peer.Peer) {
	return func(p *peer.Peer) {
		p.Handle("echo", func(_ context.Context, params json.RawMessage) (any, error) {
			ran.Add(1)
			if params == nil {
				return struct{}{}, nil
			}
			return params, nil
		})
		p.Handle("ping", pingHandler)
	}
}

// requestOfLength returns a valid echo request that is exactly n bytes long.
func requestOfLength(t *testing.T, id, n int) string {
	t.Helper()
	prefix := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"echo","params":{"pad":"`, id)
	suffix := `"}}`
	pad := n - len(prefix) - len(suffix)
	if pad < 0 {
		t.Fatalf("cannot build a request of %d bytes", n)
	}
	return prefix + strings.Repeat("x", pad) + suffix
}

func TestOverlongLineClosesConnection(t *testing.T) {
	leakGuard(t)
	const max = 256
	for _, tc := range []struct {
		name  string
		bytes func() []byte
	}{
		{"terminated overlong line followed by a valid request", func() []byte {
			return []byte(requestOfLength(t, 2, max+1) + "\n" + `{"jsonrpc":"2.0","id":3,"method":"echo"}` + "\n")
		}},
		{"overlong line that never ends", func() []byte {
			return []byte(strings.Repeat("a", 3*max))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ran atomic.Int32
			p, r, _ := newRaw(t, peer.Params{MaxLineBytes: max}, echoCounter(&ran))

			// A line of exactly the limit is accepted.
			r.send(requestOfLength(t, 1, max))
			if f := r.recv(); string(f.ID) != "1" || f.Error != nil {
				t.Fatalf("a line of exactly MaxLineBytes was not served: %+v", f)
			}

			// The write may fail once the peer closes its read end; that is part of the behaviour.
			go r.toPeer.Write(tc.bytes())
			f := r.recv()
			if f.errCode() != peer.CodeLineTooLong || string(f.ID) != "null" {
				t.Fatalf("overlong line answered with %+v, want -32004 and a null id", f)
			}
			r.expectEOF()
			waitDone(t, p)
			if err := p.Err(); err != peer.ErrLineTooLong {
				t.Fatalf("Err = %v, want ErrLineTooLong", err)
			}
			if got := ran.Load(); got != 1 {
				t.Fatalf("handler ran %d times; the request after the overlong line must never be read", got)
			}
		})
	}
}

func TestInvalidJSONKeepsConnection(t *testing.T) {
	leakGuard(t)
	var ran atomic.Int32
	_, r, _ := newRaw(t, peer.Params{}, echoCounter(&ran))
	for _, line := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"echo"`,
		`not json`,
		``,
		`{"jsonrpc":"2.0","id":1,"method":"echo"} trailing`,
	} {
		r.send(line)
		f := r.recv()
		if f.errCode() != peer.CodeParseError || string(f.ID) != "null" {
			t.Fatalf("%q answered with %+v, want -32700 and a null id", line, f)
		}
	}
	r.send(`{"jsonrpc":"2.0","id":7,"method":"ping"}`)
	if f := r.recv(); string(f.ID) != "7" || f.Error != nil {
		t.Fatalf("connection did not survive invalid JSON: %+v", f)
	}
	if ran.Load() != 0 {
		t.Fatal("a handler ran for invalid JSON")
	}
}

func TestInvalidUTF8IsParseError(t *testing.T) {
	leakGuard(t)
	var ran atomic.Int32
	_, r, _ := newRaw(t, peer.Params{}, echoCounter(&ran))
	// 0xff is not UTF-8. encoding/json alone would accept this line and
	// silently turn the byte into U+FFFD.
	r.sendBytes([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"echo\",\"params\":{\"s\":\"a\xffb\"}}\n"))
	if f := r.recv(); f.errCode() != peer.CodeParseError || string(f.ID) != "null" {
		t.Fatalf("invalid UTF-8 answered with %+v, want -32700 and a null id", f)
	}
	r.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if f := r.recv(); string(f.ID) != "2" || f.Error != nil {
		t.Fatalf("connection did not survive invalid UTF-8: %+v", f)
	}
	if ran.Load() != 0 {
		t.Fatal("a handler ran for a line with invalid UTF-8")
	}
}

func TestMissingOrWrongVersionIsInvalidRequest(t *testing.T) {
	leakGuard(t)
	var ran atomic.Int32
	_, r, _ := newRaw(t, peer.Params{}, echoCounter(&ran))
	for _, tc := range []struct {
		name, line, wantID string
	}{
		{"no jsonrpc member", `{"id":1,"method":"echo"}`, "1"},
		{"jsonrpc 1.0", `{"jsonrpc":"1.0","id":2,"method":"echo"}`, "2"},
		{"jsonrpc is a number", `{"jsonrpc":2.0,"id":3,"method":"echo"}`, "3"},
		{"wrong key case", `{"JSONRPC":"2.0","id":4,"method":"echo"}`, "4"},
		// No "method" member: not recognisable as a request, so no id is echoed.
		{"method key in wrong case", `{"jsonrpc":"2.0","id":5,"Method":"echo"}`, "null"},
		{"response without jsonrpc", `{"id":9,"result":{}}`, "null"},
		{"error response with jsonrpc 1.0", `{"jsonrpc":"1.0","id":10,"error":{"code":1001,"message":"x"}}`, "null"},
		{"neither method, result nor error", `{"jsonrpc":"2.0","id":11}`, "null"},
		{"not an object", `"hello"`, "null"},
		{"a number", `42`, "null"},
		{"null", `null`, "null"},
		{"empty method", `{"jsonrpc":"2.0","id":6,"method":""}`, "6"},
		{"method is not a string", `{"jsonrpc":"2.0","id":7,"method":5}`, "7"},
		{"request with a result", `{"jsonrpc":"2.0","id":8,"method":"echo","result":{}}`, "8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r.send(tc.line)
			f := r.recv()
			if f.errCode() != peer.CodeInvalidRequest || string(f.ID) != tc.wantID {
				t.Fatalf("got %+v, want -32600 with id %s", f, tc.wantID)
			}
		})
	}
	if ran.Load() != 0 {
		t.Fatal("a handler ran for an invalid request")
	}
}

func TestBatchIsRejectedAndConnectionContinues(t *testing.T) {
	leakGuard(t)
	var ran atomic.Int32
	_, r, _ := newRaw(t, peer.Params{}, echoCounter(&ran))
	r.send(`[{"jsonrpc":"2.0","id":1,"method":"echo"},{"jsonrpc":"2.0","id":2,"method":"echo"}]`)
	if f := r.recv(); f.errCode() != peer.CodeInvalidRequest || string(f.ID) != "null" {
		t.Fatalf("batch answered with %+v, want one -32600 with a null id", f)
	}
	r.send(`[]`)
	if f := r.recv(); f.errCode() != peer.CodeInvalidRequest {
		t.Fatalf("empty batch answered with %+v", f)
	}
	r.send(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	if f := r.recv(); string(f.ID) != "3" || f.Error != nil {
		t.Fatalf("connection did not survive a batch: %+v", f)
	}
	if ran.Load() != 0 {
		t.Fatal("a handler ran for a batch")
	}
}

func TestOverloadedIsDecidedBeforeParamsAndHandler(t *testing.T) {
	leakGuard(t)
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	var ran atomic.Int32
	_, r, _ := newRaw(t, peer.Params{MaxPendingRequests: 2, MaxConcurrentHandlers: 1}, func(p *peer.Peer) {
		p.Handle("slow", func(ctx context.Context, _ json.RawMessage) (any, error) {
			ran.Add(1)
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return struct{}{}, nil
		})
	})

	r.send(`{"jsonrpc":"2.0","id":1,"method":"slow"}`)
	<-started // the only handler is busy
	r.send(`{"jsonrpc":"2.0","id":2,"method":"slow"}`)
	r.send(`{"jsonrpc":"2.0","id":3,"method":"slow"}`) // the queue (2) is now full

	// Params that would be -32602 if anything looked at them.
	r.send(`{"jsonrpc":"2.0","id":4,"method":"slow","params":"not structured"}`)
	f := r.recv()
	if string(f.ID) != "4" || f.errCode() != peer.CodeOverloaded {
		t.Fatalf("got %+v, want id 4 refused with -32001 before its params were examined", f)
	}
	if got := ran.Load(); got != 1 {
		t.Fatalf("handler ran %d times when the fourth request was refused, want 1", got)
	}

	close(release)
	answered := map[string]bool{}
	for range 3 {
		f := r.recv()
		if f.Error != nil {
			t.Fatalf("queued request failed: %+v", f)
		}
		answered[string(f.ID)] = true
	}
	if len(answered) != 3 || answered["4"] {
		t.Fatalf("answers for %v, want exactly 1, 2 and 3", answered)
	}
	if got := ran.Load(); got != 3 {
		t.Fatalf("handler ran %d times in total, want 3 (never for the refused request)", got)
	}
}

func TestCancellation(t *testing.T) {
	leakGuard(t)
	const cancel1 = `{"jsonrpc":"2.0","method":"$/cancel_request","params":{"request_id":1}}`

	t.Run("a handler that honours the cancel gets one -32800 reply", func(t *testing.T) {
		started := make(chan struct{}, 1)
		_, r, _ := newRaw(t, peer.Params{}, func(p *peer.Peer) {
			p.Handle("wait", func(ctx context.Context, _ json.RawMessage) (any, error) {
				started <- struct{}{}
				<-ctx.Done()
				return nil, ctx.Err()
			})
			p.Handle("ping", pingHandler)
		})
		r.send(`{"jsonrpc":"2.0","id":1,"method":"wait"}`)
		<-started
		r.send(cancel1)
		if f := r.recv(); string(f.ID) != "1" || f.errCode() != peer.CodeRequestCancelled {
			t.Fatalf("got %+v, want id 1 answered with -32800", f)
		}
		// Anything extra for id 1 would arrive before the ping's answer.
		r.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
		if f := r.recv(); string(f.ID) != "2" {
			t.Fatalf("got %+v, want the answer to the ping: id 1 was answered twice", f)
		}
	})

	t.Run("a handler that ignores the cancel still gets exactly one reply", func(t *testing.T) {
		started := make(chan struct{}, 1)
		release := make(chan struct{})
		_, r, _ := newRaw(t, peer.Params{}, func(p *peer.Peer) {
			p.Handle("stubborn", func(context.Context, json.RawMessage) (any, error) {
				started <- struct{}{}
				<-release
				return map[string]string{"done": "yes"}, nil
			})
			p.Handle("ping", pingHandler)
		})
		r.send(`{"jsonrpc":"2.0","id":1,"method":"stubborn"}`)
		<-started
		r.send(cancel1)
		r.expectNoLineFor(50 * time.Millisecond)
		close(release)
		f := r.recv()
		if string(f.ID) != "1" || f.Error != nil || !strings.Contains(string(f.Result), "yes") {
			t.Fatalf("got %+v, want the handler's own result", f)
		}
		r.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
		if f := r.recv(); string(f.ID) != "2" {
			t.Fatalf("got %+v: id 1 was answered twice", f)
		}
	})

	t.Run("a request cancelled while queued never starts its handler but is answered", func(t *testing.T) {
		started := make(chan struct{}, 4)
		release := make(chan struct{})
		var ran atomic.Int32
		_, r, _ := newRaw(t, peer.Params{MaxConcurrentHandlers: 1}, func(p *peer.Peer) {
			p.Handle("slow", func(context.Context, json.RawMessage) (any, error) {
				ran.Add(1)
				started <- struct{}{}
				<-release
				return struct{}{}, nil
			})
		})
		r.send(`{"jsonrpc":"2.0","id":1,"method":"slow"}`)
		<-started
		r.send(`{"jsonrpc":"2.0","id":2,"method":"slow"}`)
		r.send(`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"request_id":2}}`)
		// The read loop answers this on its own, so once the reply is here the cancel was processed.
		r.send(`{"jsonrpc":"2.0","id":3,"method":"no.such.method"}`)
		if f := r.recv(); string(f.ID) != "3" {
			t.Fatalf("got %+v", f)
		}
		close(release)
		got := map[string]int{}
		for range 2 {
			f := r.recv()
			got[string(f.ID)] = f.errCode()
		}
		if got["1"] != 0 || got["2"] != peer.CodeRequestCancelled || len(got) != 2 {
			t.Fatalf("answers %v, want id 1 ok and id 2 -32800", got)
		}
		if ran.Load() != 1 {
			t.Fatalf("handler ran %d times, want 1", ran.Load())
		}
	})

	t.Run("string ids can be cancelled and unknown ids are ignored", func(t *testing.T) {
		started := make(chan struct{}, 1)
		_, r, _ := newRaw(t, peer.Params{}, func(p *peer.Peer) {
			p.Handle("wait", func(ctx context.Context, _ json.RawMessage) (any, error) {
				started <- struct{}{}
				<-ctx.Done()
				return nil, ctx.Err()
			})
		})
		r.send(`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"request_id":99}}`)
		r.send(`{"jsonrpc":"2.0","method":"$/cancel_request","params":"junk"}`)
		r.send(`{"jsonrpc":"2.0","id":"abc","method":"wait"}`)
		<-started
		r.expectNoLineFor(50 * time.Millisecond)
		r.send(`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"request_id":"abc"}}`)
		if f := r.recv(); string(f.ID) != `"abc"` || f.errCode() != peer.CodeRequestCancelled {
			t.Fatalf("got %+v", f)
		}
	})
}

func TestCancelWithoutAUsableRequestIDCancelsNothing(t *testing.T) {
	leakGuard(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	_, r, _ := newRaw(t, peer.Params{}, func(p *peer.Peer) {
		p.Handle("wait", func(ctx context.Context, _ json.RawMessage) (any, error) {
			started <- struct{}{}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
				return struct{}{}, nil
			}
		})
	})
	// The zero value of a missing id is 0, so request 0 shows that none of these
	// is read as a cancel of it.
	r.send(`{"jsonrpc":"2.0","id":0,"method":"wait"}`)
	<-started
	for _, params := range []string{
		`{}`, `{"request_id":null}`, `{"request_id":""}`, `{"request_id":1.0}`,
		`{"request_id":9223372036854775807}`, `{"request_id":[0]}`,
		// Decoded as strictly as every other params: a repeated member, an unknown
		// member and a member in another case are not a cancel of request 0.
		`{"request_id":0,"request_id":0}`, `{"request_id":0,"extra":true}`, `{"Request_ID":0}`,
	} {
		r.send(`{"jsonrpc":"2.0","method":"$/cancel_request","params":` + params + `}`)
	}
	r.send(`{"jsonrpc":"2.0","method":"$/cancel_request"}`)
	// The read loop answers this on its own, so once the reply is here the cancels were processed.
	r.send(`{"jsonrpc":"2.0","id":1,"method":"no.such.method"}`)
	if f := r.recv(); string(f.ID) != "1" || f.errCode() != peer.CodeMethodNotFound {
		t.Fatalf("got %+v", f)
	}
	close(release)
	if f := r.recv(); string(f.ID) != "0" || f.Error != nil {
		t.Fatalf("request 0 was disturbed: %+v", f)
	}
}

func TestWriteTimeoutClosesTransportOnStalledReader(t *testing.T) {
	leakGuard(t)
	in, _ := io.Pipe() // never delivers anything
	outR, outW := io.Pipe()
	// Nobody reads outR: the first write stalls.
	p, err := peer.New(in, captured(outW), peer.Config{Params: peer.Params{WriteTimeout: 100 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	p.Start()
	t.Cleanup(func() { p.Close(); outR.Close() })

	start := time.Now()
	errc := make(chan error, 1)
	go func() { errc <- p.Call(context.Background(), "anything", nil, nil) }()
	select {
	case err := <-errc:
		if err != peer.ErrClosed {
			t.Fatalf("pending call ended with %v, want ErrClosed", err)
		}
	case <-time.After(wait):
		t.Fatal("the write timeout never fired")
	}
	waitDone(t, p)
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("closed after %v, before the write timeout", elapsed)
	}
	if err := p.Err(); err != peer.ErrWriteTimeout {
		t.Fatalf("Err = %v, want ErrWriteTimeout", err)
	}
	// The transport is closed, so the stalled writer's pipe now fails.
	if _, err := outW.Write([]byte("x")); err == nil {
		t.Fatal("transport still accepts writes")
	}
}

func TestSlowButProgressingReaderDoesNotTimeOut(t *testing.T) {
	leakGuard(t)
	in, _ := io.Pipe()
	outR, outW := io.Pipe()
	p, err := peer.New(in, captured(outW), peer.Config{Params: peer.Params{WriteTimeout: 500 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	p.Start()
	t.Cleanup(func() { p.Close(); outR.Close() })
	lines := make(chan string, 16)
	go func() {
		buf := make([]byte, 4096)
		for {
			time.Sleep(40 * time.Millisecond) // each write waits, but well inside the limit
			n, err := outR.Read(buf)
			if err != nil {
				close(lines)
				return
			}
			lines <- string(buf[:n])
		}
	}()
	for i := range 6 {
		if err := p.Notify(context.Background(), "n", map[string]int{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
	for range 6 {
		select {
		case <-lines:
		case <-time.After(wait):
			t.Fatal("line not delivered")
		}
	}
	select {
	case <-p.Done():
		t.Fatalf("peer ended: %v", p.Err())
	default:
	}
}

func TestNotificationOverflowClosesConnection(t *testing.T) {
	leakGuard(t)
	// Repeated because the check that no queued notification runs after the
	// connection ended races with the loop's own shutdown.
	for range 30 {
		started := make(chan struct{}, 8)
		release := make(chan struct{})
		var handled atomic.Int32
		p, r, _ := newRaw(t, peer.Params{MaxPendingNotifications: 2}, func(p *peer.Peer) {
			p.HandleNotification("event", func(ctx context.Context, _ json.RawMessage) {
				handled.Add(1)
				started <- struct{}{}
				<-release
			})
		})
		r.send(`{"jsonrpc":"2.0","method":"event","params":{"n":1}}`)
		<-started // being handled; the queue is empty
		r.send(`{"jsonrpc":"2.0","method":"event","params":{"n":2}}`)
		r.send(`{"jsonrpc":"2.0","method":"event","params":{"n":3}}`) // the queue (2) is full
		r.send(`{"jsonrpc":"2.0","method":"event","params":{"n":4}}`) // overflow

		waitDone(t, p)
		if err := p.Err(); err != peer.ErrNotificationOverflow {
			t.Fatalf("Err = %v, want ErrNotificationOverflow", err)
		}
		r.expectEOF() // closed, with no error reply: notifications have no id to answer
		close(release)
		time.Sleep(time.Millisecond)
		if handled.Load() != 1 {
			t.Fatalf("handled %d notifications, want only the one that was running", handled.Load())
		}
		p.Close()
	}
}

func TestNotificationsAreHandledInArrivalOrder(t *testing.T) {
	leakGuard(t)
	got := make(chan int, 300)
	_, r, _ := newRaw(t, peer.Params{}, func(p *peer.Peer) {
		p.HandleNotification("event", func(_ context.Context, params json.RawMessage) {
			var v struct{ N int }
			json.Unmarshal(params, &v)
			got <- v.N
		})
	})
	for i := range 200 {
		r.send(fmt.Sprintf(`{"jsonrpc":"2.0","method":"event","params":{"n":%d}}`, i))
	}
	for i := range 200 {
		select {
		case n := <-got:
			if n != i {
				t.Fatalf("notification %d handled at position %d", n, i)
			}
		case <-time.After(wait):
			t.Fatal("notification not handled")
		}
	}
}

func TestOutboundQueueFullReturnsErrOverloadedAndWritesNothing(t *testing.T) {
	leakGuard(t)
	in, _ := io.Pipe()
	outR, outW := io.Pipe()
	p, err := peer.New(in, captured(outW), peer.Config{Params: peer.Params{MaxOutbound: 4, WriteTimeout: time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	p.Start()
	t.Cleanup(func() { p.Close(); outR.Close() })

	ctx := context.Background()
	accepted := 0
	for {
		err := p.Notify(ctx, "n", map[string]int{"i": accepted})
		if err == peer.ErrOverloaded {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		accepted++
		if accepted > 20 {
			t.Fatal("the outbound queue never filled")
		}
	}
	// The queue holds MaxOutbound lines and the writer may hold one more.
	if accepted < 4 || accepted > 5 {
		t.Fatalf("accepted %d notifications before the queue was full, want 4 or 5", accepted)
	}
	err = p.Call(ctx, "m", nil, nil)
	if err != peer.ErrOverloaded || !peer.NoEffect(err) {
		t.Fatalf("Call on a full queue returned %v (NoEffect %v), want ErrOverloaded with no effect", err, peer.NoEffect(err))
	}

	// Drain: exactly the accepted notifications were written, none of the refused ones.
	rd := &raw{t: t, fromPeer: outR, lines: make(chan lineResult, 64)}
	rd.br = newReader(outR)
	go rd.pump()
	for i := range accepted {
		line, _, ok := rd.readLine(wait)
		if !ok || !strings.Contains(string(line), fmt.Sprintf(`"i":%d`, i)) {
			t.Fatalf("line %d: %q (ok=%v)", i, line, ok)
		}
	}
	rd.expectSilence(100 * time.Millisecond)
	// With room again, sending works.
	if err := p.Notify(ctx, "n", nil); err != nil {
		t.Fatalf("Notify after draining: %v", err)
	}
}

func TestDuplicateInFlightIDIsInvalidRequest(t *testing.T) {
	leakGuard(t)
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var ran atomic.Int32
	_, r, _ := newRaw(t, peer.Params{}, func(p *peer.Peer) {
		p.Handle("slow", func(context.Context, json.RawMessage) (any, error) {
			ran.Add(1)
			started <- struct{}{}
			<-release
			return struct{}{}, nil
		})
	})
	r.send(`{"jsonrpc":"2.0","id":1,"method":"slow"}`)
	<-started
	r.send(`{"jsonrpc":"2.0","id":1,"method":"slow"}`)
	if f := r.recv(); string(f.ID) != "1" || f.errCode() != peer.CodeInvalidRequest {
		t.Fatalf("got %+v, want -32600 for the reused in-flight id", f)
	}
	r.send(`{"jsonrpc":"2.0","id":"1","method":"slow"}`) // the string "1" is a different id
	<-started
	close(release)
	got := map[string]bool{}
	for range 2 {
		f := r.recv()
		if f.Error != nil {
			t.Fatalf("got %+v", f)
		}
		got[string(f.ID)] = true
	}
	if !got["1"] || !got[`"1"`] {
		t.Fatalf("answers for %v, want 1 and \"1\"", got)
	}
	// Once answered, the id is free again.
	r.send(`{"jsonrpc":"2.0","id":1,"method":"slow"}`)
	<-started
	if f := r.recv(); string(f.ID) != "1" || f.Error != nil {
		t.Fatalf("reuse after completion refused: %+v", f)
	}
	if ran.Load() != 3 {
		t.Fatalf("handler ran %d times, want 3", ran.Load())
	}
}

func TestInvalidRequestIDs(t *testing.T) {
	leakGuard(t)
	var ran atomic.Int32
	_, r, _ := newRaw(t, peer.Params{}, echoCounter(&ran))
	for _, tc := range []struct{ name, id string }{
		{"null", `null`},
		{"fraction", `1.5`},
		{"integral fraction", `1.0`},
		{"exponent", `1e2`},
		{"first number beyond the exact range", `9007199254740992`},
		{"last negative number beyond the exact range", `-9007199254740992`},
		{"largest int64", `9223372036854775807`},
		{"beyond int64", `9223372036854775808`},
		{"empty string", `""`},
		{"string of 129 characters", `"` + strings.Repeat("a", 129) + `"`},
		{"string of 129 two-byte characters", `"` + strings.Repeat("é", 129) + `"`},
		{"object", `{}`},
		{"array", `[1]`},
		{"boolean", `true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r.send(`{"jsonrpc":"2.0","id":` + tc.id + `,"method":"echo"}`)
			f := r.recv()
			if f.errCode() != peer.CodeInvalidRequest || string(f.ID) != "null" {
				t.Fatalf("got %+v, want -32600 with a null id", f)
			}
		})
	}
	valid := []string{
		`0`, `-0`, `-5`, `9007199254740991`, `-9007199254740991`,
		`"abc"`, `"a"`, `"` + strings.Repeat("a", 128) + `"`,
		// The limit counts characters, not bytes: 128 of them take 256 bytes.
		`"` + strings.Repeat("é", 128) + `"`,
	}
	for _, id := range valid {
		r.send(`{"jsonrpc":"2.0","id":` + id + `,"method":"echo"}`)
		f := r.recv()
		if f.Error != nil || string(f.ID) != id {
			t.Fatalf("id %.40s: got %+v, want a result echoing the id as received", id, f)
		}
	}
	if int(ran.Load()) != len(valid) {
		t.Fatalf("handler ran %d times, want %d", ran.Load(), len(valid))
	}
}

func TestLateResponseIsDroppedAndLogged(t *testing.T) {
	leakGuard(t)
	p, r, logs := newRaw(t, peer.Params{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := p.Call(ctx, "slow.method", nil, nil)
	if err != context.DeadlineExceeded || !peer.ResultUnknown(err) {
		t.Fatalf("Call returned %v, want DeadlineExceeded with an unknown result", err)
	}
	req := r.recv()
	if req.Method != "slow.method" || len(req.ID) == 0 {
		t.Fatalf("got %+v", req)
	}
	cancelMsg := r.recv()
	if cancelMsg.Method != peer.MethodCancelRequest || !strings.Contains(string(cancelMsg.Params), `"request_id":`+string(req.ID)) {
		t.Fatalf("expected $/cancel_request for %s, got %+v", req.ID, cancelMsg)
	}
	r.send(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"late":true}}`)
	r.send(`{"jsonrpc":"2.0","id":12345,"result":{}}`)

	deadline := time.Now().Add(wait)
	for strings.Count(logs.String(), "late or unknown response dropped") < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("late responses were not logged:\n%s", logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The peer still works: a fresh call completes.
	errc := make(chan error, 1)
	go func() { errc <- p.Call(context.Background(), "again", nil, nil) }()
	again := r.recv()
	r.send(`{"jsonrpc":"2.0","id":` + string(again.ID) + `,"result":{}}`)
	if err := <-errc; err != nil {
		t.Fatalf("second call: %v", err)
	}
}

func TestUnknownMethodIsMethodNotFound(t *testing.T) {
	leakGuard(t)
	_, r, _ := newRaw(t, peer.Params{}, func(p *peer.Peer) { p.Handle("ping", pingHandler) })
	r.send(`{"jsonrpc":"2.0","id":9,"method":"no.such.method"}`)
	if f := r.recv(); string(f.ID) != "9" || f.errCode() != peer.CodeMethodNotFound {
		t.Fatalf("got %+v, want -32601", f)
	}
	// A notification-only method is not a request method either.
	r.send(`{"jsonrpc":"2.0","id":10,"method":"event"}`)
	if f := r.recv(); f.errCode() != peer.CodeMethodNotFound {
		t.Fatalf("got %+v, want -32601", f)
	}
}

func TestUnknownNotificationIsLoggedAndDropped(t *testing.T) {
	leakGuard(t)
	_, r, logs := newRaw(t, peer.Params{}, func(p *peer.Peer) { p.Handle("ping", pingHandler) })
	r.send(`{"jsonrpc":"2.0","method":"no.such.event","params":{"secret":"s3cret-body"}}`)
	r.send(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if f := r.recv(); string(f.ID) != "1" || f.Error != nil {
		t.Fatalf("got %+v: an unknown notification must produce no reply", f)
	}
	if !strings.Contains(logs.String(), "unknown notification dropped") {
		t.Fatalf("not logged:\n%s", logs.String())
	}
}

func TestParamsMustBeStructuredAndNullMeansAbsent(t *testing.T) {
	leakGuard(t)
	seen := make(chan string, 4)
	_, r, _ := newRaw(t, peer.Params{}, func(p *peer.Peer) {
		p.Handle("m", func(_ context.Context, params json.RawMessage) (any, error) {
			seen <- fmt.Sprintf("%q", params)
			return struct{}{}, nil
		})
	})
	r.send(`{"jsonrpc":"2.0","id":1,"method":"m","params":"text"}`)
	if f := r.recv(); f.errCode() != peer.CodeInvalidParams {
		t.Fatalf("got %+v, want -32602", f)
	}
	r.send(`{"jsonrpc":"2.0","id":2,"method":"m","params":null}`)
	r.recv()
	r.send(`{"jsonrpc":"2.0","id":3,"method":"m"}`)
	r.recv()
	r.send(`{"jsonrpc":"2.0","id":4,"method":"m","params":[1]}`)
	r.recv()
	want := []string{`""`, `""`, `"[1]"`}
	for _, w := range want {
		if got := <-seen; got != w {
			t.Fatalf("handler saw params %s, want %s", got, w)
		}
	}
}

func TestTruncatedFinalLineEndsConnectionWithoutRunningIt(t *testing.T) {
	leakGuard(t)
	var ran atomic.Int32
	p, r, _ := newRaw(t, peer.Params{}, echoCounter(&ran))
	r.sendBytes([]byte(`{"jsonrpc":"2.0","id":1,"method":"echo"}`)) // no newline
	r.toPeer.Close()
	waitDone(t, p)
	if err := p.Err(); err == nil || !strings.Contains(err.Error(), io.ErrUnexpectedEOF.Error()) {
		t.Fatalf("Err = %v, want an unexpected EOF", err)
	}
	if ran.Load() != 0 {
		t.Fatal("an unterminated line was executed")
	}
}

func TestRemoteCloseEndsPeer(t *testing.T) {
	leakGuard(t)
	p, r, _ := newRaw(t, peer.Params{}, nil)
	r.toPeer.Close()
	waitDone(t, p)
	if err := p.Err(); err != peer.ErrRemoteClosed {
		t.Fatalf("Err = %v, want ErrRemoteClosed", err)
	}
	r.expectEOF()
}

func TestLogsNeverContainMessageBodies(t *testing.T) {
	leakGuard(t)
	_, r, logs := newRaw(t, peer.Params{MaxPendingRequests: 1, MaxConcurrentHandlers: 1}, func(p *peer.Peer) {
		p.Handle("fail", func(context.Context, json.RawMessage) (any, error) {
			return nil, fmt.Errorf("handler failed")
		})
		p.Handle("panic", func(context.Context, json.RawMessage) (any, error) { panic("handler panic") })
	})
	const secret = "s3cret-body-marker"
	for _, tc := range []struct {
		line    string
		replies bool
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"nothing","params":{"x":"` + secret + `"}}`, true},
		{`{"jsonrpc":"2.0","method":"nothing","params":{"x":"` + secret + `"}}`, false},
		{`{"jsonrpc":"2.0","id":1.5,"method":"fail","params":{"x":"` + secret + `"}}`, true},
		{`{"jsonrpc":"1.0","id":2,"method":"fail","params":{"x":"` + secret + `"}}`, true},
		{`[{"x":"` + secret + `"}]`, true},
		{`{"jsonrpc":"2.0","id":3,"method":"fail","params":"` + secret + `"}`, true},
		{`{"jsonrpc":"2.0","id":4,"method":"fail","params":{"x":"` + secret + `"}}`, true},
		{`{"jsonrpc":"2.0","id":5,"method":"panic","params":{"x":"` + secret + `"}}`, true},
		{`{"jsonrpc":"2.0","id":99,"result":{"x":"` + secret + `"}}`, false},
		{`{"jsonrpc":"2.0","id":99,"error":{"code":1,"message":"` + secret + `"}}`, false},
		{`{"jsonrpc":"2.0","id":null,"error":{"code":1,"message":"` + secret + `"}}`, false},
		{`bad ` + secret, true},
	} {
		r.send(tc.line)
		if tc.replies {
			r.recv()
		}
	}
	r.send(`{"jsonrpc":"2.0","id":100,"method":"fail"}`)
	r.recv()
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("a message body reached the log:\n%s", logs.String())
	}
	if logs.String() == "" {
		t.Fatal("expected some log output")
	}
}

// The error replies the read loop owes are never dropped: when the outbound
// queue is full the read loop waits, so a counterpart that does not read its
// replies cannot make the peer consume its flood.
func TestReadLoopWaitsWhenErrorRepliesCannotBeQueued(t *testing.T) {
	leakGuard(t)
	const lines = 40
	_, r, _ := newRawUndrained(t, peer.Params{MaxOutbound: 2, WriteTimeout: time.Minute})

	var sent atomic.Int32
	go func() {
		for i := range lines {
			r.toPeer.Write([]byte(fmt.Sprintf("bad line %d\n", i)))
			sent.Add(1)
		}
	}()
	time.Sleep(300 * time.Millisecond)
	// The queue holds 2, the writer holds 1, the read loop holds 1: a handful of lines at most.
	if n := sent.Load(); n > 6 {
		t.Fatalf("the peer consumed %d of the flood although none of its replies could be queued", n)
	}

	// Once the replies are read, every one of them arrives, in order.
	go r.pump()
	for i := range lines {
		f := r.recv()
		if f.errCode() != peer.CodeParseError {
			t.Fatalf("reply %d: %+v", i, f)
		}
	}
	if int(sent.Load()) != lines {
		t.Fatalf("flood not fully consumed: %d", sent.Load())
	}
}

// A stalled reader must not keep the connection open when the last line to
// write is the reply to an overlong line: the write timeout still applies.
func TestOverlongLineWithStalledReaderStillCloses(t *testing.T) {
	leakGuard(t)
	p, r, _ := newRawUndrained(t, peer.Params{MaxLineBytes: 64, WriteTimeout: 100 * time.Millisecond})
	go r.toPeer.Write([]byte(strings.Repeat("x", 500) + "\n"))
	waitDone(t, p)
	if err := p.Err(); err != peer.ErrLineTooLong {
		t.Fatalf("Err = %v, want ErrLineTooLong", err)
	}
}

// Both sides have a call 5 in flight: the other side's request 5 is being
// handled here, and this side's own call 5 waits for its answer. A malformed
// message that looks like a response to 5 must not be answered with id 5: that
// error would be taken for the answer to the other side's request, whose
// handler is still running and may yet have an effect.
func TestMalformedResponseIsNeverAnsweredWithItsID(t *testing.T) {
	leakGuard(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	p, r, _ := newRaw(t, peer.Params{}, func(p *peer.Peer) {
		p.Handle("slow", func(context.Context, json.RawMessage) (any, error) {
			started <- struct{}{}
			<-release
			return map[string]string{"done": "yes"}, nil
		})
	})
	ctx := context.Background()

	// This side's calls 1 to 4 complete; call 5 stays in flight.
	for i := 1; i <= 4; i++ {
		done := make(chan error, 1)
		go func() { done <- p.Call(ctx, "other", nil, nil) }()
		if f := r.recv(); string(f.ID) != fmt.Sprint(i) {
			t.Fatalf("call %d went out with id %s", i, f.ID)
		}
		r.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{}}`, i))
		if err := <-done; err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	call5 := make(chan error, 1)
	var result struct{ Echo string }
	go func() { call5 <- p.Call(ctx, "other", nil, &result) }()
	if f := r.recv(); string(f.ID) != "5" {
		t.Fatalf("the fifth call went out with id %s", f.ID)
	}

	// The other side's request 5 is in flight here too.
	r.send(`{"jsonrpc":"2.0","id":5,"method":"slow"}`)
	<-started

	for _, malformed := range []string{
		`{"id":5,"result":{}}`,
		`{"jsonrpc":"1.0","id":5,"result":{}}`,
		`{"jsonrpc":"2.0","id":5}`,
		`{"id":5,"error":{"code":-32603,"message":"x"}}`,
	} {
		r.send(malformed)
		f := r.recv()
		if f.errCode() != peer.CodeInvalidRequest || string(f.ID) != "null" {
			t.Fatalf("%s was answered with %+v, want -32600 with a null id", malformed, f)
		}
	}

	// Neither request 5 nor call 5 was disturbed.
	select {
	case err := <-call5:
		t.Fatalf("call 5 ended on a malformed message: %v", err)
	default:
	}
	close(release)
	if f := r.recv(); string(f.ID) != "5" || f.Error != nil || !strings.Contains(string(f.Result), `"done"`) {
		t.Fatalf("request 5 was answered with %+v, want its result", f)
	}
	r.send(`{"jsonrpc":"2.0","id":5,"result":{"echo":"ok"}}`)
	if err := <-call5; err != nil || result.Echo != "ok" {
		t.Fatalf("call 5 = %v, %+v", err, result)
	}
}

func TestRepeatedFrameMemberIsInvalidRequest(t *testing.T) {
	leakGuard(t)
	var ran atomic.Int32
	_, r, _ := newRaw(t, peer.Params{}, echoCounter(&ran))
	for _, tc := range []struct{ name, line string }{
		{"jsonrpc", `{"jsonrpc":"2.0","jsonrpc":"2.0","id":1,"method":"echo"}`},
		{"id", `{"jsonrpc":"2.0","id":1,"id":2,"method":"echo"}`},
		{"method", `{"jsonrpc":"2.0","id":1,"method":"echo","method":"ping"}`},
		{"params", `{"jsonrpc":"2.0","id":1,"method":"echo","params":{},"params":{}}`},
		{"a notification", `{"jsonrpc":"2.0","method":"echo","method":"echo"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r.send(tc.line)
			// The id cannot be trusted when members repeat, so the reply has none.
			if f := r.recv(); f.errCode() != peer.CodeInvalidRequest || string(f.ID) != "null" {
				t.Fatalf("got %+v, want -32600 with a null id", f)
			}
		})
	}
	// A repeat inside params is not the frame's business: the handler is run.
	r.send(`{"jsonrpc":"2.0","id":9,"method":"echo","params":{"a":1,"a":2}}`)
	if f := r.recv(); string(f.ID) != "9" || f.Error != nil {
		t.Fatalf("a repeat inside params was refused by the frame layer: %+v", f)
	}
	if ran.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1", ran.Load())
	}
}
