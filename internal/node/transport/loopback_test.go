package transport_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/transport"
)

// TestNewLoopbackRejectsWhatItCannotWorkWith pins the constructor's contract: a
// loopback needs the node it stands for and a handler to hand requests to.
func TestNewLoopbackRejectsWhatItCannotWorkWith(t *testing.T) {
	for rule, build := range map[string]func(){
		"the zero node id":               func() { transport.NewLoopback(ident.NodeID{}, newTarget(), nil) },
		"a nil handler":                  func() { transport.NewLoopback(nodeOf(t, 1), nil, nil) },
		"neither of the two":             func() { transport.NewLoopback(ident.NodeID{}, nil, nil) },
		"a nil handler with knobs given": func() { transport.NewLoopback(nodeOf(t, 1), nil, transport.NewKnobs(nil)) },
	} {
		t.Run(rule, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewLoopback did not panic")
				}
			}()
			build()
		})
	}
}

type ctxKey struct{}

// ctxRecorder keeps the value the caller's context carried into each call of the
// handler, and answers with the context's error once it has ended, as a handler
// that honours its context does.
type ctxRecorder struct {
	target *target
	mu     sync.Mutex
	values []any
}

func (c *ctxRecorder) note(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values = append(c.values, ctx.Value(ctxKey{}))
}

func (c *ctxRecorder) Inbound(ctx context.Context, from transport.Origin, sender ident.EndpointID, data []byte) (transport.Receipt, error) {
	c.note(ctx)
	if err := ctx.Err(); err != nil {
		return transport.Receipt{}, err
	}
	return c.target.Inbound(ctx, from, sender, data)
}

func (c *ctxRecorder) Pull(ctx context.Context, from transport.Origin, req transport.PullRequest) (transport.PullAnswer, error) {
	c.note(ctx)
	if err := ctx.Err(); err != nil {
		return transport.PullAnswer{}, err
	}
	return c.target.Pull(ctx, from, req)
}

// TestLoopbackPassesTheCallersContext pins that the handler works under the
// context of the call that brought the request, a replay included, and that an
// ended context comes back as itself: neither ErrTimeout nor ErrUnreachable,
// because the caller ended the call and the target may or may not have seen it.
func TestLoopbackPassesTheCallersContext(t *testing.T) {
	r := newRig(t)
	rec := &ctxRecorder{target: r.target}
	l := transport.NewLoopback(r.self, rec, r.knobs)
	ctx := context.WithValue(context.Background(), ctxKey{}, "caller")

	r.knobs.Mutate("m1")
	if _, err := l.Send(ctx, r.self, ep(9), r.message(t, "m1", "hello")); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := l.Pull(ctx, r.self, transport.PullRequest{Message: "m1"}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	rec.mu.Lock()
	values := append([]any(nil), rec.values...)
	rec.mu.Unlock()
	if len(values) != 3 {
		t.Fatalf("the handler was called %d times, want the send, its replay and the pull", len(values))
	}
	for i, v := range values {
		if v != "caller" {
			t.Errorf("call %d of the handler ran under a context without the caller's value (got %v)", i, v)
		}
	}

	ended, cancel := context.WithCancel(ctx)
	cancel()
	_, err := l.Send(ended, r.self, ep(9), r.message(t, "m2", "hello"))
	if !errors.Is(err, context.Canceled) || errors.Is(err, transport.ErrTimeout) || errors.Is(err, transport.ErrUnreachable) {
		t.Errorf("send under an ended context: error %v, want context.Canceled as it is", err)
	}
	_, err = l.Pull(ended, r.self, transport.PullRequest{Message: "m1"})
	if !errors.Is(err, context.Canceled) || errors.Is(err, transport.ErrTimeout) || errors.Is(err, transport.ErrUnreachable) {
		t.Errorf("pull under an ended context: error %v, want context.Canceled as it is", err)
	}
}

// TestLoopbackWaitingIsTheKnobsOwn pins what a caller that holds only the
// loopback reads when it asks whether the transport is quiet: the count of calls
// waiting out a delay not yet due, and zero for a loopback made with no knobs.
func TestLoopbackWaitingIsTheKnobsOwn(t *testing.T) {
	r := newRig(t)
	const d = time.Minute
	r.knobs.Delay(d)
	done := make(chan error, 1)
	go func() {
		_, err := r.send(r.message(t, "m1", "hello"))
		done <- err
	}()
	waitFor(t, "the call to wait out its delay", func() bool { return r.loop.Waiting() == 1 })
	if got := r.knobs.Waiting(); got != 1 {
		t.Errorf("the loopback counts 1 waiting call and its knobs %d", got)
	}
	r.clk.Advance(d)
	if got := r.loop.Waiting(); got != 0 {
		t.Errorf("Waiting() = %d once the delay is due, want 0", got)
	}
	if err := <-done; err != nil {
		t.Errorf("delayed send: %v", err)
	}

	bare := transport.NewLoopback(r.self, newTarget(), nil)
	if got := bare.Waiting(); got != 0 {
		t.Errorf("a loopback with no knobs waits for %d calls, want 0", got)
	}
}
