package peer

import (
	"context"
	"io"
	"testing"
	"time"
)

// A call refused for lack of room in the outbound queue leaves no trace: no
// pending entry that nothing would ever remove.
func TestRefusedCallLeavesNoPendingEntry(t *testing.T) {
	in, _ := io.Pipe()
	_, out := io.Pipe() // never read: the writer stalls on the first line
	p, err := New(in, out, Config{Params: Params{MaxOutbound: 2, WriteTimeout: time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	p.Start()
	defer p.Close()

	ctx := context.Background()
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
