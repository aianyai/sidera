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
