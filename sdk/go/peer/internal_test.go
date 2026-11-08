package peer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

// stalledWriter blocks every Write until it is closed, and tells when the first
// one has begun.
type stalledWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (w *stalledWriter) Write([]byte) (int, error) {
	select {
	case w.entered <- struct{}{}:
	default:
	}
	<-w.release
	return 0, io.ErrClosedPipe
}

func (w *stalledWriter) Close() error { close(w.release); return nil }

// A call refused for lack of room in the outbound queue leaves no trace: no
// pending entry that nothing would ever remove.
func TestRefusedCallLeavesNoPendingEntry(t *testing.T) {
	in, _ := io.Pipe()
	out := &stalledWriter{entered: make(chan struct{}, 1), release: make(chan struct{})}
	p, err := New(in, out, Config{Params: Params{MaxOutbound: 2, WriteTimeout: time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	p.Start()
	defer p.Close()

	ctx := context.Background()
	// Let the writer take the first line and stall on it: only then does a full
	// queue stay full, whatever order the goroutines run in.
	if err := p.Notify(ctx, "n", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-out.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer never began to write")
	}
	for p.Notify(ctx, "n", nil) == nil { // fill the queue
	}
	for range 5 {
		if err := p.Call(ctx, "m", nil, nil); err != ErrOverloaded {
			t.Fatalf("Call = %v, want ErrOverloaded", err)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pending) != 0 {
		t.Fatalf("%d pending entries left behind", len(p.pending))
	}
}

// A request id stops being in flight before its reply is queued: the other side
// may reuse the id as soon as it has read the reply, and a reply that waits for
// room in the outbound queue must not keep the id taken meanwhile. The writer is
// stalled and the queue full, so the third reply cannot be queued; its id must
// already be free.
func TestRequestIDIsReleasedBeforeItsReplyIsQueued(t *testing.T) {
	in, inW := io.Pipe()
	out := &stalledWriter{entered: make(chan struct{}, 1), release: make(chan struct{})}
	p, err := New(in, out, Config{Params: Params{MaxOutbound: 1, WriteTimeout: time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	var ran sync.WaitGroup
	ran.Add(3)
	p.Handle("m", func(context.Context, json.RawMessage) (any, error) {
		defer ran.Done()
		return struct{}{}, nil
	})
	p.Start()
	defer func() { p.Close(); inW.Close() }()

	// Reply 1 is taken by the writer, which stalls on it; reply 2 fills the
	// queue; reply 3 waits in send.
	for id := 1; id <= 3; id++ {
		if _, err := fmt.Fprintf(inW, `{"jsonrpc":"2.0","id":%d,"method":"m"}`+"\n", id); err != nil {
			t.Fatal(err)
		}
		if id == 1 {
			select {
			case <-out.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("the writer never began to write")
			}
		}
	}
	ran.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p.mu.Lock()
		n := len(p.inbound)
		p.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d ids are still in flight although every handler has returned and the replies cannot be queued", n)
		}
		time.Sleep(time.Millisecond)
	}
}

// recordingWriter records every line written to it. The first Write waits for
// releaseWrite; Close waits for releaseClose.
type recordingWriter struct {
	entered      chan struct{}
	releaseWrite chan struct{}
	releaseClose chan struct{}

	mu    sync.Mutex
	lines []string
	first sync.Once
}

func (w *recordingWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	w.lines = append(w.lines, string(b))
	w.mu.Unlock()
	w.first.Do(func() {
		close(w.entered)
		<-w.releaseWrite
	})
	return len(b), nil
}

func (w *recordingWriter) Close() error { <-w.releaseClose; return nil }

func (w *recordingWriter) written() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.lines...)
}

// untilTrue polls cond, which reads state the test cannot be told about, until
// it holds.
func untilTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// A handler that sees its context end because the connection is ending may
// return an answer in the moment before the transport is closed; that answer
// must not be written, because lines not yet written are discarded once the
// connection ends. The test holds the transport open (Close blocks) and the
// writer busy with an earlier line, so the late answer is queued behind it and
// the writer reaches it while the connection is ending but not yet closed.
func TestAnswerQueuedWhileTheConnectionEndsIsNotWritten(t *testing.T) {
	in, inW := io.Pipe()
	out := &recordingWriter{
		entered:      make(chan struct{}),
		releaseWrite: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
	p, err := New(in, out, Config{Params: Params{WriteTimeout: time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	returned := make(chan struct{})
	p.Handle("quick", func(context.Context, json.RawMessage) (any, error) { return struct{}{}, nil })
	p.Handle("slow", func(ctx context.Context, _ json.RawMessage) (any, error) {
		close(started)
		<-ctx.Done()
		defer close(returned)
		return map[string]any{"late": true}, nil
	})
	p.Start()
	defer inW.Close()

	// The writer takes the answer to "quick" and stays in its Write.
	fmt.Fprintln(inW, `{"jsonrpc":"2.0","id":1,"method":"quick"}`)
	<-out.entered
	fmt.Fprintln(inW, `{"jsonrpc":"2.0","id":2,"method":"slow"}`)
	<-started

	closed := make(chan struct{})
	go func() { p.Close(); close(closed) }()
	<-returned
	untilTrue(t, "the late answer to be queued", func() bool { return len(p.out) == 1 })

	close(out.releaseWrite)
	untilTrue(t, "the writer to take the late answer", func() bool { return len(p.out) == 0 })
	close(out.releaseClose)
	<-closed

	if got := out.written(); len(got) != 1 {
		t.Fatalf("lines written = %q, want only the answer to the request that finished before the connection ended", got)
	}
}
