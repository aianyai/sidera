package peer

import (
	"context"
	"io"
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
