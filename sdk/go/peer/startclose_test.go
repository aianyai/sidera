package peer_test

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aianyai/sidera/sdk/go/peer"
)

// watchedReader is a closable reader that counts how often the peer read from
// it and how many reads are running right now. Close ends a blocked Read.
type watchedReader struct {
	src    *io.PipeReader
	reads  atomic.Int64
	active atomic.Int64
}

func newWatchedReader() *watchedReader {
	r, _ := io.Pipe()
	return &watchedReader{src: r}
}

func (r *watchedReader) Read(p []byte) (int, error) {
	r.reads.Add(1)
	r.active.Add(1)
	defer r.active.Add(-1)
	return r.src.Read(p)
}

func (r *watchedReader) Close() error { return r.src.Close() }

// TestStartAndCloseAreSafeConcurrently: a supervisor may close a peer on one
// goroutine while another is still starting it. Whichever wins, there is no
// data race, Close returns only once the peer's reader has stopped, the
// connection ends with ErrClosed and no goroutine is left behind.
func TestStartAndCloseAreSafeConcurrently(t *testing.T) {
	leakGuard(t)
	const rounds = 50
	for range rounds {
		r := newWatchedReader()
		p := newPeerOver(t, r)

		var starters, closer sync.WaitGroup
		gate := make(chan struct{})
		for range 2 { // two Starts also race each other
			starters.Add(1)
			go func() { defer starters.Done(); <-gate; p.Start() }()
		}
		closer.Add(1)
		go func() {
			defer closer.Done()
			<-gate
			p.Close()
			if n := r.active.Load(); n != 0 {
				t.Errorf("Close returned while %d read(s) of the peer were still running", n)
			}
		}()
		close(gate)
		starters.Wait()
		closer.Wait()

		select {
		case <-p.Done():
		default:
			t.Fatal("Done is not closed after Close returned")
		}
		if err := p.Err(); !errors.Is(err, peer.ErrClosed) {
			t.Fatalf("Err = %v, want ErrClosed", err)
		}
		p.Close() // still idempotent
	}
}

// TestStartAfterCloseDoesNothing: a Start that comes after Close, including one
// that lost the race to it, launches nothing. A launched reader would read from
// the transport, so the count of reads says whether anything ran.
func TestStartAfterCloseDoesNothing(t *testing.T) {
	leakGuard(t)
	r := newWatchedReader()
	p := newPeerOver(t, r)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p.Start()
	p.Start()
	if left := settledPeerGoroutines(); len(left) != 0 {
		t.Fatalf("%d peer goroutines alive after a Start that came after Close:\n%v", len(left), left)
	}
	if n := r.reads.Load(); n != 0 {
		t.Fatalf("the peer read from its transport %d time(s) after Close", n)
	}
	if err := p.Err(); !errors.Is(err, peer.ErrClosed) {
		t.Fatalf("Err = %v, want ErrClosed", err)
	}
}

// newPeerOver creates an unstarted peer that reads from r and writes nowhere,
// and closes it when the test ends.
func newPeerOver(t *testing.T, r io.Reader) *peer.Peer {
	t.Helper()
	p, err := peer.New(r, nopWriteCloser{}, peer.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}
