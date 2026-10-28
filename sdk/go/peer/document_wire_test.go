package peer_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aianyai/sidera/sdk/go/peer"
)

func TestDialectDocumentRejectionHasNoSideEffects(t *testing.T) {
	leakGuard(t)
	var requests, notifications, cancellations atomic.Int32
	d := testDialect()
	d.RejectDuplicateKeysAndInvalidUnicode = true
	d.CancelTarget = func(raw json.RawMessage) (peer.RequestID, bool) {
		cancellations.Add(1)
		return lenientCancelTarget(raw)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	_, r, logs := newDialectRaw(t, peer.Config{Dialect: d}, func(p *peer.Peer) {
		p.Handle("wait", func(ctx context.Context, _ json.RawMessage) (any, error) {
			started <- struct{}{}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
				return struct{}{}, nil
			}
		})
		p.Handle("m", func(context.Context, json.RawMessage) (any, error) {
			requests.Add(1)
			return struct{}{}, nil
		})
		p.HandleNotification("note", func(context.Context, json.RawMessage) { notifications.Add(1) })
		p.Handle("ping", pingHandler)
	}, true)
	r.send(`{"jsonrpc":"2.0","id":1,"method":"wait"}`)
	awaitSignal(t, started, "the in-flight handler")
	for _, line := range []string{
		`{"jsonrpc":"2.0","id":7,"method":"m","params":{"secret-key":"secret-value","secret-key":2}}`,
		`{"jsonrpc":"2.0","id":7,"method":"unknown","future":{"a":1,"a":2}}`,
		`{"jsonrpc":"2.0","method":"note","future":"\ud800"}`,
		`{"jsonrpc":"2.0","method":"unknown","future":{"a":1,"a":2}}`,
		`{"jsonrpc":"2.0","method":"test/cancelled","params":{"requestId":1,"future":{"a":1,"a":2}}}`,
		`{"jsonrpc":"2.0","id":7,"result":{"a":1,"a":2}}`,
	} {
		r.send(line)
		if f := r.recv(); f.errCode() != peer.CodeInvalidRequest || f.Error.Message != "duplicate member or invalid Unicode" {
			t.Fatalf("got %+v for %s", f, line)
		}
	}
	if requests.Load() != 0 || notifications.Load() != 0 || cancellations.Load() != 0 {
		t.Fatalf("rejected frames ran requests=%d notifications=%d cancels=%d", requests.Load(), notifications.Load(), cancellations.Load())
	}
	if got := logs.String(); strings.Contains(got, "secret-") {
		t.Fatalf("input leaked in logs: %s", got)
	}
	close(release)
	if f := r.recv(); string(f.ID) != "1" || f.Error != nil {
		t.Fatalf("in-flight request was cancelled: %+v", f)
	}
	r.send(`{"jsonrpc":"2.0","id":8,"method":"ping"}`)
	if f := r.recv(); string(f.ID) != "8" || f.Error != nil {
		t.Fatalf("connection did not continue: %+v", f)
	}
}

func TestDialectDocumentValidationBeforeOverload(t *testing.T) {
	leakGuard(t)
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "whole document"}[reject], func(t *testing.T) {
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			d := testDialect()
			d.RejectDuplicateKeysAndInvalidUnicode = reject
			_, r, _ := newDialectRaw(t, peer.Config{Dialect: d, Params: peer.Params{MaxConcurrentHandlers: 1, MaxPendingRequests: 1}}, func(p *peer.Peer) {
				p.Handle("slow", func(ctx context.Context, _ json.RawMessage) (any, error) {
					select {
					case started <- struct{}{}:
					default:
					}
					select {
					case <-release:
					case <-ctx.Done():
					}
					return struct{}{}, nil
				})
			}, true)
			r.send(`{"jsonrpc":"2.0","id":1,"method":"slow"}`)
			awaitSignal(t, started, "the busy handler")
			r.send(`{"jsonrpc":"2.0","id":2,"method":"slow"}`)
			for _, params := range []string{`{"a":1,"a":2}`, `"wrong type"`} {
				r.send(`{"jsonrpc":"2.0","id":3,"method":"slow","params":` + params + `}`)
				want := dialectOverloaded
				if reject && params[0] == '{' {
					want = peer.CodeInvalidRequest
				}
				if f := r.recv(); string(f.ID) != "3" || f.errCode() != want {
					t.Fatalf("got %+v, want %d", f, want)
				}
			}
			close(release)
			for range 2 {
				if f := r.recv(); f.Error != nil {
					t.Fatalf("queued request failed: %+v", f)
				}
			}
		})
	}
}
