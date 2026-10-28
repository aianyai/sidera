package transport_test

import (
	"context"
	"errors"
	"slices"
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

func (c *ctxRecorder) QueryDirectory(ctx context.Context, from transport.Origin, sender ident.EndpointID, req transport.DirectoryRequest) (transport.DirectoryPage, error) {
	c.note(ctx)
	if err := ctx.Err(); err != nil {
		return transport.DirectoryPage{}, err
	}
	return c.target.QueryDirectory(ctx, from, sender, req)
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
	if _, err := l.Pull(ctx, r.self, transport.PullRequest{Message: "m1", Recipients: pulled}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if _, err := l.QueryDirectory(ctx, r.self, ep(9), transport.DirectoryRequest{}); err != nil {
		t.Fatalf("directory: %v", err)
	}
	rec.mu.Lock()
	values := append([]any(nil), rec.values...)
	rec.mu.Unlock()
	if len(values) != 4 {
		t.Fatalf("the handler was called %d times, want the send, its replay, pull and directory query", len(values))
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
	_, err = l.Pull(ended, r.self, transport.PullRequest{Message: "m1", Recipients: pulled})
	if !errors.Is(err, context.Canceled) || errors.Is(err, transport.ErrTimeout) || errors.Is(err, transport.ErrUnreachable) {
		t.Errorf("pull under an ended context: error %v, want context.Canceled as it is", err)
	}
	_, err = l.QueryDirectory(ended, r.self, ep(9), transport.DirectoryRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("directory under an ended context: error %v, want context.Canceled", err)
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

// TestLoopbackCarriesTheRecipientsAndTheNoRecordAnswer pins that the loopback
// hands the handler the request as the source made it, recipients included, and
// returns the handler's answer whole: a recipient the handler lists as having no
// record comes back listed.
func TestLoopbackCarriesTheRecipientsAndTheNoRecordAnswer(t *testing.T) {
	r := newRig(t)
	ans, err := r.pull("never-sent")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ans.NoRecord, pulled) || len(ans.Reports) != 0 {
		t.Errorf("the answer for a message the target holds none of: %+v, want every requested recipient listed as having no record", ans)
	}
	if got := r.target.pulls; len(got) != 1 || got[0].Message != "never-sent" || !slices.Equal(got[0].Recipients, pulled) {
		t.Errorf("the handler was asked %+v, want the request as it was made", got)
	}
}

// TestAHeldPullIsEmptyWhateverTheTargetWouldAnswer pins that a pull whose reports
// are held gets an answer with nothing in it, for a message the target holds no
// record of too: a held answer is "nothing yet" and must never read as "no
// record". The handler is not asked while the reports are held, and answers as
// it would once they are released.
func TestAHeldPullIsEmptyWhateverTheTargetWouldAnswer(t *testing.T) {
	r := newRig(t)
	r.knobs.HoldReports("never-sent")
	ans, err := r.pull("never-sent")
	if err != nil || len(ans.Reports) != 0 || len(ans.NoRecord) != 0 || ans.ControlReply != nil {
		t.Fatalf("the held pull: %+v, %v, want an empty answer", ans, err)
	}
	if n := r.target.pulled(); n != 0 {
		t.Errorf("the held pull reached the target %d times", n)
	}
	r.knobs.Release("never-sent")
	ans, err = r.pull("never-sent")
	if err != nil || !slices.Equal(ans.NoRecord, pulled) {
		t.Errorf("the pull after the release: %+v, %v, want the recipients listed as having no record", ans, err)
	}
}
