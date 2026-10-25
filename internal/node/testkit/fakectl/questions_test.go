package fakectl_test

import "testing"

// TestPeerCloseConcurrentWithStartIsRaceFree records a question for the owner of
// sdk/go/peer, which this package does not decide: may Close be called from one
// goroutine while another calls Start on the same peer? Today the race detector
// reports a data race between the two (a write in Close against a read inside
// the sync.Once of Start). The server of this package avoids the question by
// starting a peer before anything else can close it. Whether the peer should
// make the pair safe, or say in its README that Start and Close must not be
// concurrent, is the owner's decision.
func TestPeerCloseConcurrentWithStartIsRaceFree(t *testing.T) {
	t.Skip("open question for sdk/go/peer: Start and Close from different goroutines race; see the doc comment")
}
